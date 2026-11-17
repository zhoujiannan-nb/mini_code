package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/user/mini_code/util"
)

type _FsTool struct {
	workspace    string
	allowedDir   string
	extraAllowed []string
}

func (t *_FsTool) resolvePath(path string) (string, error) {
	p := path
	if !filepath.IsAbs(p) && t.workspace != "" {
		p = filepath.Join(t.workspace, p)
	}
	p = filepath.Clean(p)
	if t.allowedDir != "" {
		allDirs := append([]string{t.allowedDir}, t.extraAllowed...)
		allowed := false
		for _, d := range allDirs {
			rel, err := filepath.Rel(filepath.Clean(d), p)
			if err == nil && !strings.HasPrefix(rel, "..") {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("path %s is outside allowed directory %s", path, t.allowedDir)
		}
	}
	return p, nil
}

type ReadFileTool struct{ _FsTool }

func NewReadFileTool(workspace string) *ReadFileTool {
	return &ReadFileTool{_FsTool{workspace: workspace}}
}

func (t *ReadFileTool) Name() string { return "read" }

func (t *ReadFileTool) Description() string                { return dshDesc("read") }
func (t *ReadFileTool) IsHidden() bool                     { return false }
func (t *ReadFileTool) Parameters() map[string]interface{} { return dshParams("read") }

func (t *ReadFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	log := _FsTool{workspace: t.workspace}
	path := strParam(params, "file_path", "path")
	if strings.TrimSpace(path) == "" {
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	offset, limit := 1, readMaxLimit
	if v, present := params["offset"]; present {
		n, ok := positiveInt(v)
		if !ok {
			return NewTextResult("Error: invalid arguments: offset must be a positive integer"), nil
		}
		offset = n
	}
	if v, present := params["limit"]; present {
		n, ok := positiveInt(v)
		if !ok {
			return NewTextResult("Error: invalid arguments: limit must be a positive integer"), nil
		}
		if n > readMaxLimit {
			return NewTextResult(fmt.Sprintf("Error: invalid arguments: limit must be less than or equal to %d", readMaxLimit)), nil
		}
		limit = n
	}
	tail := intParam(params, "tail", 0)
	fp, err := log.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(fp)
	if err != nil {
		msg := fmt.Sprintf("Error: cannot read %q: not found", path)
		if hint := fileNotFoundHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if info.IsDir() {
		msg := fmt.Sprintf("Error: cannot read %q: not a regular file", path)
		if hint := dirNotFileHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}

	if info.Size() > largeFileThreshold {
		return t.readLargeWindow(ctx, fp, path, offset, limit, tail), nil
	}

	raw, err := os.ReadFile(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err)), nil
	}

	if bytes.IndexByte(raw[:min(8192, len(raw))], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: cannot read %q: binary file", path)), nil
	}

	content, _ := util.DecodeText(raw)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 1 && lines[0] == "" && len(raw) == 0 {
		lines = nil
	}
	total := len(lines)

	if tail > 0 {
		offset = total - tail + 1
		if offset < 1 {
			offset = 1
		}
		limit = tail
	}
	if offset > total && !(total == 0 && offset == 1) {
		return NewTextResult(fmt.Sprintf("Error: offset %d is out of range for %q (%d lines)", offset, path, total)), nil
	}

	start := offset - 1
	end := start + limit
	if end > total {
		end = total
	}
	body, shownEnd, _, capped := renderReadWindow(lines[start:end], offset)
	var footer string
	switch {
	case capped:
		footer = readCappedFooter(offset, shownEnd, shownEnd+1)
	case shownEnd < total:
		footer = fmt.Sprintf("(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, shownEnd, total, shownEnd+1)
	default:
		footer = fmt.Sprintf("(End of file - total %d lines)", total)
	}
	return NewTextResult(formatReadOutput(fp, body, footer)), nil
}

func positiveInt(v interface{}) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	if f != float64(int(f)) || int(f) < 1 {
		return 0, false
	}
	return int(f), true
}

const largeFileThreshold = 10 << 20

func (t *ReadFileTool) readLargeWindow(ctx context.Context, fp, path string, offset, limit, tail int) *ToolResult {
	f, err := os.Open(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err))
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	head := make([]byte, 8192)
	n, _ := io.ReadFull(br, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: cannot read %q: binary file", path))
	}
	if bytes.HasPrefix(head[:n], []byte{0xEF, 0xBB, 0xBF}) {
		n -= 3
		copy(head[:n], head[3:])
	}
	src := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(head[:n]), br), 256*1024)

	lineNo := 0
	var window, ring []string
	for {
		if lineNo&1023 == 0 && ctx.Err() != nil {
			return NewTextResult("Interrupted while scanning the file.")
		}
		raw, rerr := src.ReadString('\n')
		if len(raw) > 0 {
			line := util.DecodeToUTF8([]byte(raw))
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			lineNo++
			if tail > 0 {
				ring = append(ring, line)
				if len(ring) > tail {
					ring = ring[1:]
				}
			} else if lineNo >= offset && len(window) < limit {
				window = append(window, line)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return NewTextResult(fmt.Sprintf("Error reading file: %s", rerr))
		}
	}
	total := lineNo

	if tail > 0 {
		start := total - len(ring) + 1
		if start < 1 {
			start = 1
		}
		body, _, _, _ := renderReadWindow(ring, start)
		return NewTextResult(formatReadOutput(fp, body, fmt.Sprintf("(End of file - total %d lines)", total)))
	}

	if len(window) == 0 {
		return NewTextResult(fmt.Sprintf("Error: offset %d is out of range for %q (%d lines)", offset, path, total))
	}
	body, shownEnd, _, capped := renderReadWindow(window, offset)
	var footer string
	switch {
	case capped:
		footer = readCappedFooter(offset, shownEnd, shownEnd+1)
	case shownEnd < total:
		footer = fmt.Sprintf("(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, shownEnd, total, shownEnd+1)
	default:
		footer = fmt.Sprintf("(End of file - total %d lines)", total)
	}
	return NewTextResult(formatReadOutput(fp, body, footer))
}

type WriteFileTool struct{ _FsTool }

func NewWriteFileTool(workspace string) *WriteFileTool {
	return &WriteFileTool{_FsTool{workspace: workspace}}
}

func (t *WriteFileTool) Name() string                       { return "write" }
func (t *WriteFileTool) Description() string                { return dshDesc("write") }
func (t *WriteFileTool) IsHidden() bool                     { return false }
func (t *WriteFileTool) Parameters() map[string]interface{} { return dshParams("write") }

func (t *WriteFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path := strParam(params, "file_path", "path")
	content, _ := params["content"].(string)
	appendMode, _ := params["append"].(bool)
	if strings.TrimSpace(path) == "" {
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	srcFormat, _ := existingTextFormat(fp)
	if appendMode {
		data := encodeFor(srcFormat, content)
		f, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", err)), nil
		}
		n, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", werr)), nil
		}
		if cerr != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", cerr)), nil
		}
		if info, serr := os.Stat(fp); serr == nil {
			return NewTextResult(fmt.Sprintf("Successfully appended %d bytes to %s (file is now %d bytes)", n, fp, info.Size())), nil
		}
		return NewTextResult(fmt.Sprintf("Successfully appended %d bytes to %s", n, fp)), nil
	}
	_, existed := existingTextFormat(fp)
	data := encodeFor(srcFormat, content)
	if err := os.WriteFile(fp, data, 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	verb := "Created file"
	if existed {
		verb = "Updated file"
	}
	return NewTextResult("<path>" + fp + "</path>\n<type>file</type>\n<content>\n" + verb + "\n</content>"), nil
}

func existingTextFormat(fp string) (util.TextFormat, bool) {
	info, err := os.Stat(fp)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return util.TextFormat{Encoding: util.EncUTF8}, false
	}
	f, err := os.Open(fp)
	if err != nil {
		return util.TextFormat{Encoding: util.EncUTF8}, false
	}
	defer f.Close()
	limit := int64(8 << 20)
	if info.Size() < limit {
		limit = info.Size()
	}
	buf := make([]byte, limit)
	n, _ := io.ReadFull(f, buf)
	return util.DetectTextFormat(buf[:n]), true
}

func encodeFor(f util.TextFormat, s string) []byte {
	data, ok := f.Encode(s)
	if ok {
		return data
	}
	return []byte(s)
}

func countTextLines(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	s := string(raw)
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

type EditFileTool struct{ _FsTool }

func NewEditFileTool(workspace string) *EditFileTool {
	return &EditFileTool{_FsTool{workspace: workspace}}
}

func (t *EditFileTool) Name() string                       { return "edit" }
func (t *EditFileTool) Description() string                { return dshDesc("edit") }
func (t *EditFileTool) IsHidden() bool                     { return false }
func (t *EditFileTool) Parameters() map[string]interface{} { return dshParams("edit") }

func (t *EditFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path := strParam(params, "file_path", "path")
	oldText := strParam(params, "old_string", "old_text")
	newText, ok := params["new_string"].(string)
	if !ok {
		newText, _ = params["new_text"].(string)
	}
	replaceAll, _ := params["replace_all"].(bool)

	if strings.TrimSpace(path) == "" {
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	if oldText == "" {
		return NewTextResult("Error: invalid arguments: old_string must be a non-empty string"), nil
	}
	if oldText == newText {
		return NewTextResult("Error: invalid arguments: old_string and new_string must differ"), nil
	}

	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		msg := fmt.Sprintf("Error: cannot read %q: not found", path)
		if os.IsNotExist(err) {
			if hint := fileNotFoundHint(fp); hint != "" {
				msg += "\n" + hint
			}
		}
		return NewTextResult(msg), nil
	}

	content, srcFormat := util.DecodeText(raw)
	crlf := strings.Contains(content, "\r\n")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

	count := strings.Count(content, oldNorm)
	if count == 0 {
		msg := fmt.Sprintf("Error: old_string was not found in %q", path)
		if hint := closestLineHint(strings.Split(content, "\n"), oldNorm); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if count > 1 && !replaceAll {
		return NewTextResult(fmt.Sprintf("Error: old_string matched %d times in %q; provide a more specific old_string or set replace_all to true", count, path)), nil
	}
	newContent := strings.ReplaceAll(content, oldNorm, newNorm)
	if crlf {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
	}
	data := encodeFor(srcFormat, newContent)
	if err := os.WriteFile(fp, data, 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error editing file: %s", err)), nil
	}
	if replaceAll {
		return NewTextResult(fmt.Sprintf("The file %s has been updated. All occurrences were successfully replaced.", fp)), nil
	}
	return NewTextResult(fmt.Sprintf("The file %s has been updated successfully.", fp)), nil
}

func wsNormalizeLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}


const (
	closestHintLineCap  = 20000
	closestHintTextCap  = 400
	closestHintMinScore = 0.5
)

func closestLineHint(fileLines []string, oldText string) string {
	var target string
	for _, l := range strings.Split(oldText, "\n") {
		n := wsNormalizeLine(l)
		if len([]rune(n)) > len([]rune(target)) {
			target = n
		}
	}
	if len([]rune(target)) < 4 {
		return ""
	}

	targetRunes := []rune(target)
	if len(targetRunes) > closestHintTextCap {
		targetRunes = targetRunes[:closestHintTextCap]
	}
	targetBigrams := bigramCount(targetRunes)
	if len(targetBigrams) == 0 {
		return ""
	}

	bestScore := 0.0
	var bestLineNo int
	var bestLine string
	lim := len(fileLines)
	if lim > closestHintLineCap {
		lim = closestHintLineCap
	}
	for i := 0; i < lim; i++ {
		n := wsNormalizeLine(fileLines[i])
		if n == "" {
			continue
		}
		r := []rune(n)
		if len(r) > closestHintTextCap {
			r = r[:closestHintTextCap]
		}
		if len(r) < 4 {
			continue
		}
		score := bigramDice(targetBigrams, bigramCount(r))
		if score > bestScore {
			bestScore = score
			bestLineNo = i + 1
			bestLine = n
		}
	}
	if bestScore < closestHintMinScore {
		return ""
	}
	if len([]rune(bestLine)) > 120 {
		bestLine = string([]rune(bestLine)[:120]) + "…"
	}
	return fmt.Sprintf("Closest line in the file (line %d): %q — compare it with your old_text (check indentation, quotes, punctuation, spelling) and retry with the exact text.", bestLineNo, bestLine)
}

func bigramCount(s []rune) map[string]int {
	m := make(map[string]int, len(s))
	for i := 0; i+1 < len(s); i++ {
		m[string(s[i:i+2])]++
	}
	return m
}

func bigramDice(a, b map[string]int) float64 {
	inter := 0
	for bg, ca := range a {
		if cb, ok := b[bg]; ok {
			if ca < cb {
				inter += ca
			} else {
				inter += cb
			}
		}
	}
	total := len(a) + len(b)
	if total == 0 {
		return 0
	}
	return 2.0 * float64(inter) / float64(total)
}

type ListDirTool struct{ _FsTool }

var ignoreDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true,
	"venv": true, "dist": true, "build": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, ".coverage": true, "htmlcov": true,
}

func NewListDirTool(workspace string) *ListDirTool {
	return &ListDirTool{_FsTool{workspace: workspace}}
}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "List directory contents. File entries show their size in parentheses (e.g. \"notes.txt (2.3 KB)\"). Set recursive=true to list nested contents."
}
func (t *ListDirTool) IsHidden() bool { return false }
func (t *ListDirTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":      map[string]interface{}{"type": "string", "description": "Directory path to list"},
			"recursive": map[string]interface{}{"type": "boolean", "description": "List recursively (default false)"},
			"max_depth": map[string]interface{}{"type": "integer", "description": "Max recursion depth (default 3)", "minimum": 1, "maximum": 10},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("unknown path")
	}
	dp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(dp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: directory not found: %s", path)), nil
	}
	if !info.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: not a directory: %s", path)), nil
	}

	recursive, _ := params["recursive"].(bool)
	depthLimit := intParam(params, "max_depth", 3)

	var items []string
	total := 0
	cap := 400

	if recursive {
		baseDepth := len(strings.Split(dp, string(os.PathSeparator)))
		filepath.Walk(dp, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if p == dp {
				return nil
			}
			currentDepth := len(strings.Split(p, string(os.PathSeparator))) - baseDepth
			if currentDepth > depthLimit {
				return filepath.SkipDir
			}
			rel, rerr := filepath.Rel(dp, p)
			if rerr != nil {
				return nil
			}
			for _, part := range strings.Split(rel, string(os.PathSeparator)) {
				if ignoreDirs[part] {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			total++
			if total <= cap {
				if info.IsDir() {
					items = append(items, rel+"/")
				} else {
					items = append(items, rel+" ("+humanSize(info.Size())+")")
				}
			}
			return nil
		})
	} else {
		entries, err := os.ReadDir(dp)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error listing directory: %s", err)), nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if ignoreDirs[e.Name()] {
				continue
			}
			total++
			if total <= cap {
				if e.IsDir() {
					items = append(items, e.Name()+"/")
				} else {
					name := e.Name()
					if fi, ierr := e.Info(); ierr == nil {
						name += " (" + humanSize(fi.Size()) + ")"
					}
					items = append(items, name)
				}
			}
		}
	}

	if len(items) == 0 {
		return NewTextResult(fmt.Sprintf("Directory %s is empty", path)), nil
	}
	result := strings.Join(items, "\n")
	if total > cap {
		result += fmt.Sprintf("\n\n(truncated, showing first %d of %d entries)", cap, total)
	}
	return NewTextResult(result), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
