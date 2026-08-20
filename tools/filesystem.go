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
	"strconv"
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

// ReadFileTool
type ReadFileTool struct{ _FsTool }

func NewReadFileTool(workspace string) *ReadFileTool {
	return &ReadFileTool{_FsTool{workspace: workspace}}
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read file contents with line numbers. Supports pagination via offset/limit, and tail=N to read the last N lines (useful for logs). For images, use the readimg tool instead."
}
func (t *ReadFileTool) IsHidden() bool { return false }
func (t *ReadFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":   map[string]interface{}{"type": "string", "description": "File path to read"},
			"offset": map[string]interface{}{"type": "integer", "description": "Start line (1-indexed)", "minimum": 1},
			"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to return (default 2000)", "minimum": 1},
			"tail":   map[string]interface{}{"type": "integer", "description": "Return only the last N lines of the file (overrides offset/limit; useful for checking the end of logs)", "minimum": 1},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	log := _FsTool{workspace: t.workspace}
	path, _ := params["path"].(string)
	if path == "" {
		return NewTextResult("Error reading file: unknown path"), nil
	}
	fp, err := log.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(fp)
	if err != nil {
		// Point the model at the closest real names in the same directory so
		// a mistyped path (case, extension, version suffix) is fixed in one
		// step instead of costing an extra list_dir turn.
		msg := fmt.Sprintf("Error: file not found: %s", path)
		if hint := fileNotFoundHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if info.IsDir() {
		msg := fmt.Sprintf("Error: not a file: %s", path)
		if hint := dirNotFileHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}

	offset := intParam(params, "offset", 1)
	limit := intParam(params, "limit", 2000)
	tail := intParam(params, "tail", 0)
	if offset < 1 {
		offset = 1
	}

	// Large files: stream the requested window instead of loading the whole
	// file into memory (see readLargeWindow). The output format matches the
	// small-file path below.
	if info.Size() > largeFileThreshold {
		return t.readLargeWindow(fp, path, offset, limit, tail), nil
	}

	raw, err := os.ReadFile(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err)), nil
	}
	if len(raw) == 0 {
		return NewTextResult(fmt.Sprintf("(Empty file: %s)", path)), nil
	}

	// Binary guard: a NUL byte in the first 8KB means this is a binary file
	// (exe, image, db, zip, ...). Dumping it as text would produce U+FFFD
	// noise that wastes context and confuses the model.
	if bytes.IndexByte(raw[:min(8192, len(raw))], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: %s is a binary file, not readable as text. Inspect it via exec (e.g. a small script) if needed.", path)), nil
	}

	// DecodeToUTF8: text files on Windows are often GBK (ANSI) encoded;
	// without this the raw bytes become U+FFFD noise once the tool result
	// is JSON-marshaled for the LLM or persisted.
	content := util.DecodeToUTF8(raw)
	// Normalize CRLF to LF for display. CRLF is the dominant line ending on
	// Windows; without this, every line the model sees carries a trailing
	// "\r" (rendered as a "\r" escape in JSON), which wastes tokens,
	// distorts the model's view of the file, and makes the read output
	// inconsistent with edit_file's matching semantics (edit_file already
	// normalizes CRLF to LF before matching). Display only: the file on
	// disk is untouched, and edit_file restores the file's original
	// line-ending style when writing.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	// A trailing newline terminates the last line; it does not create an
	// extra empty line. Without this, "a\nb\n" would report 3 lines and
	// offset=3 would return a phantom empty line 3.
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)

	// tail mode: the last N lines win over offset/limit.
	if tail > 0 {
		offset = total - tail + 1
		if offset < 1 {
			offset = 1
		}
		limit = tail
	}
	if offset > total {
		return NewTextResult(fmt.Sprintf("Error: offset %d is beyond end of file (%d lines)", offset, total)), nil
	}

	start := offset - 1
	end := start + limit
	if end > total {
		end = total
	}

	var numbered []string
	for i := start; i < end; i++ {
		numbered = append(numbered, fmt.Sprintf("%d| %s", i+1, lines[i]))
	}
	result := strings.Join(numbered, "\n")

	if end < total {
		result += fmt.Sprintf("\n\n(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, end, total, end+1)
	} else if tail > 0 && offset > 1 {
		// tail mode that did not reach the start of the file: say so, and
		// keep the same footer the large-file path uses.
		result += fmt.Sprintf("\n\n(Showing lines %d-%d of %d — last %d lines)", offset, end, total, tail)
	} else {
		result += fmt.Sprintf("\n\n(End of file — %d lines total)", total)
	}
	return NewTextResult(result), nil
}

// largeFileThreshold: files bigger than this are served by readLargeWindow,
// a streaming scan that keeps only the requested window in memory instead of
// the whole file. Loading a 100MB log (or worse, a 1GB file) byte-for-byte
// into a []string slice is the difference between a fast tool call and a
// multi-second stall / out-of-memory crash of the whole agent process.
const largeFileThreshold = 1 << 20 // 1 MiB

// readLargeWindow serves read_file for files above largeFileThreshold with a
// streaming line scan:
//   - memory is O(window) (or O(tail)), never O(file);
//   - a head read (offset=1) stops as soon as the window is full, so reading
//     the first 2000 lines of a 100MB log touches only those 2000 lines;
//   - tail=N keeps a ring buffer of the last N lines while counting the rest,
//     so "show me the end of the log" is one cheap call.
//
// Line semantics match the small-file path exactly: a line is a "\n"-
// terminated segment or a final unterminated segment; a trailing newline does
// not create a phantom empty line; CRLF is normalized to LF. When the scan
// stops early the total line count is unknown and the footer says so.
func (t *ReadFileTool) readLargeWindow(fp, path string, offset, limit, tail int) *ToolResult {
	f, err := os.Open(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err))
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	// Binary guard (same as the small-file path): NUL byte in the first 8KB.
	head := make([]byte, 8192)
	n, _ := io.ReadFull(br, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: %s is a binary file, not readable as text. Inspect it via exec (e.g. a small script) if needed.", path))
	}
	// Re-play the 8KB head and continue with the buffered reader: the line
	// stream starts at byte 0, and br still holds everything after the head.
	src := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(head[:n]), br), 256*1024)

	lineNo := 0
	var window, ring []string
	earlyStopped := false
	for {
		raw, rerr := src.ReadString('\n')
		if len(raw) > 0 {
			// DecodeToUTF8 per line: large Windows text files are often GBK;
			// line-by-line decoding is safe (a GBK lead byte never follows a
			// 0x0A newline byte).
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
				if len(window) == limit {
					earlyStopped = true
					break // window full: the rest of the file cannot change the answer
				}
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
		numbered := make([]string, len(ring))
		for i, l := range ring {
			numbered[i] = fmt.Sprintf("%d| %s", start+i, l)
		}
		result := strings.Join(numbered, "\n")
		if start == 1 {
			result += fmt.Sprintf("\n\n(End of file — %d lines total)", total)
		} else {
			result += fmt.Sprintf("\n\n(Showing lines %d-%d of %d — last %d lines)", start, total, total, len(ring))
		}
		return NewTextResult(result)
	}

	if len(window) == 0 {
		// The scan reached EOF without collecting the window: offset is past
		// the end of the file.
		return NewTextResult(fmt.Sprintf("Error: offset %d is beyond end of file (%d lines)", offset, total))
	}
	start := offset
	end := offset + len(window) - 1
	numbered := make([]string, len(window))
	for i, l := range window {
		numbered[i] = fmt.Sprintf("%d| %s", start+i, l)
	}
	result := strings.Join(numbered, "\n")
	switch {
	case earlyStopped:
		result += fmt.Sprintf("\n\n(Showing lines %d-%d; file is large, total line count unknown. Use offset=%d to continue, or tail=N for the last lines.)", start, end, end+1)
	case end < total:
		result += fmt.Sprintf("\n\n(Showing lines %d-%d of %d. Use offset=%d to continue.)", start, end, total, end+1)
	default:
		result += fmt.Sprintf("\n\n(End of file — %d lines total)", total)
	}
	return NewTextResult(result)
}

// WriteFileTool
type WriteFileTool struct{ _FsTool }

func NewWriteFileTool(workspace string) *WriteFileTool {
	return &WriteFileTool{_FsTool{workspace: workspace}}
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Write content to a file. Creates parent directories as needed. Set append=true to add content to the END of the file instead of overwriting it (the file is created if it does not exist) — use it to build large files in several smaller chunks."
}
func (t *WriteFileTool) IsHidden() bool { return false }
func (t *WriteFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]interface{}{"type": "string", "description": "File path to write to"},
			"content": map[string]interface{}{"type": "string", "description": "Content to write (or append when append=true)"},
			"append":  map[string]interface{}{"type": "boolean", "description": "Append to the end of the file instead of overwriting it (default false; the file is created if it does not exist)"},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	appendMode, _ := params["append"].(bool)
	if path == "" {
		return nil, fmt.Errorf("unknown path")
	}
	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	if appendMode {
		// Append mode: add content to the end of the file (create it when
		// missing). This is the reliable way to build a large file in
		// several smaller write calls — a single oversized write can be
		// cut off by the output token limit, and re-writing the whole file
		// for every chunk would double the output cost.
		f, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", err)), nil
		}
		n, werr := f.WriteString(content)
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
	// Overwrite guard: before replacing an existing non-empty file, record
	// what it held. A model that meant to append (or that silently drops
	// lines it had just read) can only notice the loss if the result says
	// the file previously existed with content; the note then points it at
	// the recovery path (it still holds the old content in context, or can
	// re-read a backup) and at the append flag for next time.
	prevNote := overwriteNote(fp)
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	msg := fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), fp)
	if prevNote != "" {
		msg += " " + prevNote
	}
	return NewTextResult(msg), nil
}

// overwriteNote returns a short note describing the content an overwrite is
// about to replace, or "" when the target does not exist, is a directory, or
// is empty. It is advisory text on the success path only: the write happens
// exactly as before, the note just makes a silent clobber visible so the
// model can catch an accidental data loss (e.g. it meant to append, or it
// rewrote a file without the lines it had read) and recover.
func overwriteNote(fp string) string {
	info, err := os.Stat(fp)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return ""
	}
	size := info.Size()
	// Counting lines requires reading the file; skip it for large files so
	// the note stays cheap (a large clobber is reported by size alone).
	if size > 1<<20 { // 1 MiB
		return fmt.Sprintf("Note: this OVERWROTE an existing file (previously %s). If you meant to keep the old content, re-read it from your earlier result and write it back, or use append=true.", humanSize(size))
	}
	raw, rerr := os.ReadFile(fp)
	if rerr != nil {
		return fmt.Sprintf("Note: this OVERWROTE an existing file (previously %s). If you meant to keep the old content, re-read it from your earlier result and write it back, or use append=true.", humanSize(size))
	}
	return fmt.Sprintf("Note: this OVERWROTE an existing file (previously %s, %d lines). If you meant to keep the old content, re-read it from your earlier result and write it back, or use append=true.", humanSize(size), countTextLines(raw))
}

// countTextLines counts lines the same way read_file does: a trailing newline
// terminates the last line and does not create a phantom empty line, and a
// final line without a trailing newline still counts.
func countTextLines(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	s := string(raw)
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++ // final line without a trailing newline
	}
	return n
}

// EditFileTool
type EditFileTool struct{ _FsTool }

func NewEditFileTool(workspace string) *EditFileTool {
	return &EditFileTool{_FsTool{workspace: workspace}}
}

func (t *EditFileTool) Name() string { return "edit_file" }
func (t *EditFileTool) Description() string {
	return "Replace text in a file. Tolerates minor whitespace differences. Set replace_all=true to replace every occurrence."
}
func (t *EditFileTool) IsHidden() bool { return false }
func (t *EditFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":        map[string]interface{}{"type": "string", "description": "File path to edit"},
			"old_text":    map[string]interface{}{"type": "string", "description": "Text to find and replace"},
			"new_text":    map[string]interface{}{"type": "string", "description": "Replacement text"},
			"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default false)"},
		},
		"required": []string{"path", "old_text", "new_text"},
	}
}

func (t *EditFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	oldText, _ := params["old_text"].(string)
	newText, _ := params["new_text"].(string)
	replaceAll, _ := params["replace_all"].(bool)

	if path == "" || oldText == "" {
		return nil, fmt.Errorf("missing required parameters")
	}

	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		// Same recovery hint as read_file: a mistyped path names the closest
		// real files instead of forcing an extra list_dir turn.
		msg := fmt.Sprintf("Error: file not found: %s", path)
		if os.IsNotExist(err) {
			if hint := fileNotFoundHint(fp); hint != "" {
				msg += "\n" + hint
			}
		}
		return NewTextResult(msg), nil
	}

	rawStr := string(raw)
	// Detect the file's line-ending style so an edit does not silently
	// convert a CRLF file to LF: byte-exact checks, git diffs and Windows
	// tooling all notice the change. Files without CRLF stay LF.
	crlf := strings.Contains(rawStr, "\r\n")
	content := strings.ReplaceAll(rawStr, "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

	// finishEdit writes the edited content back, restoring the file's
	// original line-ending style. It returns a ToolResult on failure.
	finishEdit := func(newContent string) *ToolResult {
		if crlf {
			newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
		}
		if err := os.WriteFile(fp, []byte(newContent), 0644); err != nil {
			return NewTextResult(fmt.Sprintf("Error editing file: %s", err))
		}
		return nil
	}

	// Pass 1: exact substring match (fast, byte-for-byte).
	if strings.Contains(content, oldNorm) {
		count := strings.Count(content, oldNorm)
		if count > 1 && !replaceAll {
			// Point at every occurrence so the model can add context (or
			// pick replace_all) immediately instead of re-reading the file.
			return NewTextResult(fmt.Sprintf("Warning: old_text appears %d times %s. Provide more context to disambiguate, or set replace_all=true.", count, lineNoPhrase(occurrenceLineNos(content, oldNorm, 10), count))), nil
		}
		var newContent string
		if replaceAll {
			newContent = strings.ReplaceAll(content, oldNorm, newNorm)
		} else {
			newContent = strings.Replace(content, oldNorm, newNorm, 1)
		}
		if r := finishEdit(newContent); r != nil {
			return r, nil
		}
		return NewTextResult(fmt.Sprintf("Successfully edited %s", fp)), nil
	}

	// Pass 2: whitespace-tolerant line match. The tool promises to tolerate
	// minor whitespace differences (indentation, tabs vs spaces, stray blank
	// lines); exact matching alone makes the model re-read the file and retry
	// on every indentation guess.
	fileLines := strings.Split(content, "\n")
	ranges := findWhitespaceTolerantRanges(fileLines, oldNorm)
	if len(ranges) == 0 {
		msg := fmt.Sprintf("Error: old_text not found in %s (tried exact and whitespace-tolerant match). Verify the file content.", path)
		if hint := closestLineHint(fileLines, oldNorm); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if len(ranges) > 1 && !replaceAll {
		starts := make([]int, len(ranges))
		for i, r := range ranges {
			starts[i] = r[0] + 1
		}
		return NewTextResult(fmt.Sprintf("Warning: old_text appears %d times %s (whitespace-tolerant). Provide more context to disambiguate, or set replace_all=true.", len(ranges), lineNoPhrase(starts, len(ranges)))), nil
	}

	newLines := strings.Split(newNorm, "\n")
	newContent := spliceLineRanges(fileLines, ranges, newLines)
	if r := finishEdit(newContent); r != nil {
		return r, nil
	}
	return NewTextResult(fmt.Sprintf("Successfully edited %s (whitespace-tolerant match)", fp)), nil
}

// wsNormalizeLine trims a line and collapses runs of spaces/tabs into a
// single space, so "  a\tb  " and "a b" compare equal.
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

// occurrenceLineNos returns the 1-based starting line of each
// non-overlapping occurrence of needle in content (LF-normalized), in file
// order, capped at cap entries. Line numbers are counted on the normalized
// content, which has the same line structure as the original file.
func occurrenceLineNos(content, needle string, cap int) []int {
	var nos []int
	start := 0
	for len(nos) < cap {
		idx := strings.Index(content[start:], needle)
		if idx < 0 {
			break
		}
		abs := start + idx
		nos = append(nos, strings.Count(content[:abs], "\n")+1)
		start = abs + len(needle)
	}
	return nos
}

// lineNoPhrase renders a short "at lines ..." phrase for the ambiguous-edit
// warning, e.g. "at lines 3 and 17" or "at lines 2, 9, 41 (showing first 3
// of 12)". It returns "" when there are no line numbers.
func lineNoPhrase(nos []int, total int) string {
	if len(nos) == 0 {
		return ""
	}
	parts := make([]string, len(nos))
	for i, n := range nos {
		parts[i] = strconv.Itoa(n)
	}
	phrase := "at lines " + strings.Join(parts, ", ")
	if total > len(nos) {
		phrase += fmt.Sprintf(" (showing first %d of %d)", len(nos), total)
	}
	return phrase
}

// findWhitespaceTolerantRanges locates oldText inside fileLines ignoring
// per-line indentation, tabs-vs-spaces and blank lines. It returns the
// original line ranges [start, end) each match spans, in file order.
// Matching is whole-line based: every non-blank line of oldText must equal
// (after normalization) a consecutive run of non-blank file lines.
func findWhitespaceTolerantRanges(fileLines []string, oldText string) [][2]int {
	oldLines := strings.Split(oldText, "\n")
	oldNorm := make([]string, len(oldLines))
	for i, l := range oldLines {
		oldNorm[i] = wsNormalizeLine(l)
	}
	// Non-blank old lines only; blank lines in oldText are simply ignored.
	var oldIdx []int
	for i, t := range oldNorm {
		if t != "" {
			oldIdx = append(oldIdx, i)
		}
	}
	if len(oldIdx) == 0 {
		return nil
	}

	type normLine struct {
		text string
		orig int
	}
	var norm []normLine
	for i, l := range fileLines {
		t := wsNormalizeLine(l)
		if t == "" {
			continue
		}
		norm = append(norm, normLine{text: t, orig: i})
	}

	m := len(oldIdx)
	var ranges [][2]int
	for i := 0; i+m <= len(norm); i++ {
		match := true
		for j := 0; j < m; j++ {
			if norm[i+j].text != oldNorm[oldIdx[j]] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		ranges = append(ranges, [2]int{norm[i].orig, norm[i+m-1].orig + 1})
		i += m - 1 // skip past this match to avoid overlapping hits
	}
	return ranges
}

// spliceLineRanges replaces each [start, end) line range with newLines.
// Ranges must not overlap; they are applied in order.
func spliceLineRanges(fileLines []string, ranges [][2]int, newLines []string) string {
	var out []string
	prev := 0
	for _, r := range ranges {
		out = append(out, fileLines[prev:r[0]]...)
		out = append(out, newLines...)
		prev = r[1]
	}
	out = append(out, fileLines[prev:]...)
	return strings.Join(out, "\n")
}

// --- closest-line hint for failed edits ---
//
// When old_text matches nothing (exact or whitespace-tolerant), the model's
// next move is usually "re-read the whole file and try again" — a full extra
// turn. If the file actually contains a near-identical line (a typo, a
// different quote, a renamed identifier), pointing at it lets the model fix
// old_text immediately. The hint is advisory text on the error path only;
// the success path is byte-for-byte unchanged.

const (
	closestHintLineCap    = 20000 // only scan the first N file lines
	closestHintTextCap    = 400   // per-line comparison cap (runes)
	closestHintMinScore   = 0.5   // below this the "closest" line is noise
)

// closestLineHint finds the file line most similar to the most distinctive
// line of oldText and renders a short hint naming its line number. It
// returns "" when nothing is close enough to be worth mentioning.
func closestLineHint(fileLines []string, oldText string) string {
	// Pick the longest non-blank old line: it is the most distinctive and
	// the most likely to have a near-twin in the file.
	var target string
	for _, l := range strings.Split(oldText, "\n") {
		n := wsNormalizeLine(l)
		if len([]rune(n)) > len([]rune(target)) {
			target = n
		}
	}
	if len([]rune(target)) < 4 {
		return "" // too short to be meaningful
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

// bigramCount counts adjacent-rune pairs of s.
func bigramCount(s []rune) map[string]int {
	m := make(map[string]int, len(s))
	for i := 0; i+1 < len(s); i++ {
		m[string(s[i:i+2])]++
	}
	return m
}

// bigramDice is the Dice coefficient over rune-bigram multisets:
// 2*|A∩B| / (|A|+|B|). 1.0 = identical, 0.0 = disjoint.
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

// ListDirTool
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
	return "List directory contents. File entries show their size in parentheses (e.g. \"notes.txt (2.3 KB)\"), so size questions (largest file, empty files, total size) can be answered from this listing alone. Set recursive=true for nested listing."
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
			if p == dp {
				return nil
			}
			currentDepth := len(strings.Split(p, string(os.PathSeparator))) - baseDepth
			if currentDepth > depthLimit {
				return filepath.SkipDir
			}
			parts := strings.Split(p, string(os.PathSeparator))
			for _, part := range parts {
				if ignoreDirs[part] {
					return filepath.SkipDir
				}
			}
			total++
			if total <= cap {
				rel, _ := filepath.Rel(dp, p)
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
					// e.Info() can fail (broken symlink, permission error);
					// then the entry is shown without a size rather than
					// dropping it from the listing.
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

// humanSize renders a byte count in human-readable form using 1024-based
// units: "512 B", "3.4 KB", "1.2 MB", "2.0 GB". Sub-unit precision is
// dropped at KB and above (one decimal) to keep listing lines short.
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
