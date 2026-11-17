package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/user/mini_code/util"
)

type SearchTool struct{ _FsTool }

func NewSearchTool(workspace string) *SearchTool {
	return &SearchTool{_FsTool{workspace: workspace}}
}

func (t *SearchTool) Name() string                       { return "grep" }
func (t *SearchTool) Description() string                { return dshDesc("grep") }
func (t *SearchTool) IsHidden() bool                     { return false }
func (t *SearchTool) Parameters() map[string]interface{} { return dshParams("grep") }

const (
	searchDefaultMax = 250
	searchHardMax    = 500
	searchLineBytes  = 2000
	searchFileCap    = 200000
	searchMaxContext = 20
)

func intParam(params map[string]interface{}, key string, def int) int {
	switch v := params[key].(type) {
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if int(v) > 0 {
			return int(v)
		}
	}
	return def
}

func (t *SearchTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	pattern, isStr := params["pattern"].(string)
	if !isStr || len(pattern) == 0 {
		return NewTextResult("Error: invalid arguments: pattern must be a non-empty string"), nil
	}
	if p, present := params["path"]; present {
		ps, _ := p.(string)
		if strings.TrimSpace(ps) == "" {
			return NewTextResult("Error: invalid arguments: path must be a non-empty string when given"), nil
		}
	}
	var include string
	if v, present := params["include"]; present {
		include, _ = v.(string)
		if strings.TrimSpace(include) == "" {
			return NewTextResult("Error: invalid arguments: include must be a non-empty glob when given"), nil
		}
		if strings.HasPrefix(strings.TrimSpace(include), "!") {
			return NewTextResult(`Error: invalid arguments: include must be a positive glob filter; negated patterns ("!…") are not supported`), nil
		}
		braceDepth := 0
		for _, ch := range include {
			switch ch {
			case '{':
				braceDepth++
			case '}':
				if braceDepth > 0 {
					braceDepth--
				}
			case ',':
				if braceDepth == 0 {
					return NewTextResult("Error: invalid arguments: include must be one glob, not a comma-separated list (use {a,b} alternation instead)"), nil
				}
			}
		}
		include = strings.TrimSpace(include)
	}

	root := t.workspace
	if p, ok := params["path"].(string); ok && strings.TrimSpace(p) != "" {
		rp, err := t.resolvePath(strings.TrimSpace(p))
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
		}
		root = rp
	}
	info, err := os.Stat(root)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: path not found: %s", root)), nil
	}

	ignoreCase, _ := params["ignore_case"].(bool)
	maxResults := intParam(params, "max_results", searchDefaultMax)
	if maxResults > searchHardMax {
		maxResults = searchHardMax
	}
	contextLines := intParam(params, "context", 0)
	if contextLines > searchMaxContext {
		contextLines = searchMaxContext
	}

	var re *regexp.Regexp
	var compileErr error
	if ignoreCase {
		re, compileErr = regexp.Compile("(?i)" + pattern)
	} else {
		re, compileErr = regexp.Compile(pattern)
	}
	if compileErr != nil {
		if ignoreCase {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
		} else {
			re = regexp.MustCompile(regexp.QuoteMeta(pattern))
		}
	}

	type fileHit struct {
		abs   string
		rel   string
		idxs  []int
		texts []string
	}
	var (
		hits          []fileHit
		mu            sync.Mutex
		wg           sync.WaitGroup
		total        int
	)
	var includeAlts []string
	if include != "" {
		includeAlts = braceExpand(include)
	}

	scanOne := func(fp string) {
		if ctx.Err() != nil {
			return
		}
		if len(includeAlts) > 0 {
			ok := false
			for _, alt := range includeAlts {
				if m, _ := filepath.Match(alt, filepath.Base(fp)); m {
					ok = true
					break
				}
			}
			if !ok {
				return
			}
		}
		f, err := os.Open(fp)
		if err != nil {
			return
		}
		defer f.Close()

		head := make([]byte, 8192)
		n, _ := f.Read(head)
		if bytes.IndexByte(head[:n], 0) >= 0 {
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var idxs []int
		var texts []string
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			if lineNo > searchFileCap {
				break
			}
			line := util.DecodeToUTF8(scanner.Bytes())
			line = strings.TrimSuffix(line, "\r")
			if re.MatchString(line) {
				idxs = append(idxs, lineNo-1)
				texts = append(texts, line)
			}
		}
		if len(idxs) == 0 {
			return
		}
		rel := fp
		if r, err := filepath.Rel(t.workspace, fp); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		mu.Lock()
		total += len(idxs)
		hits = append(hits, fileHit{abs: fp, rel: rel, idxs: idxs, texts: texts})
		mu.Unlock()
	}

	if info.IsDir() {
		paths := make(chan string, 256)
		workers := searchWorkers
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range paths {
					scanOne(p)
				}
			}()
		}
		walkErr := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if fi.IsDir() {
				if p != root && ignoreDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			paths <- p
			return nil
		})
		close(paths)
		wg.Wait()
		if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
			return NewTextResult(fmt.Sprintf("Error: %s", walkErr)), nil
		}
	} else {
		scanOne(root)
	}

	if total == 0 {
		return NewTextResult("No matches found"), nil
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].rel < hits[j].rel })

	var b strings.Builder
	shown := 0
	if contextLines == 0 {
		var sections []string
		for _, fm := range hits {
			if shown >= maxResults {
				break
			}
			var rows []string
			for k := range fm.idxs {
				if shown >= maxResults {
					break
				}
				rows = append(rows, formatMatchLine(fm.idxs[k]+1, capLineBytes(fm.texts[k])))
				shown++
			}
			sections = append(sections, fm.rel+"\n"+strings.Join(rows, "\n"))
		}
		header := fmt.Sprintf("Found %d %s", total, grepMatchNoun(total))
		if total > shown {
			header = fmt.Sprintf("Found %d of %d matches", shown, total)
		}
		b.WriteString(header)
		b.WriteString("\n\n")
		b.WriteString(strings.Join(sections, "\n\n"))
		if total > shown {
			var full strings.Builder
			for _, fm := range hits {
				var rows []string
				for k := range fm.idxs {
					rows = append(rows, formatMatchLine(fm.idxs[k]+1, capLineBytes(fm.texts[k])))
				}
				full.WriteString(fm.rel + "\n" + strings.Join(rows, "\n") + "\n\n")
			}
			b.WriteString("\n\n")
			if spool := spoolWrite("grep", full.String()); spool != "" {
				fmt.Fprintf(&b, "(Full grep result stored at: %s. Use read with offset/limit, or grep this path to search within it.)", spool)
			} else {
				b.WriteString("(The complete result could not be saved; narrow pattern, path, or include to see more.)")
			}
		}
		return NewTextResult(b.String()), nil
	}

	var matchCount int
	for _, fm := range hits {
		if matchCount >= maxResults {
			break
		}
		lines := readLinesForContext(fm.abs)
		if len(lines) == 0 {
			continue
		}
		matchSet := make(map[int]bool, len(fm.idxs))
		for _, mi := range fm.idxs {
			matchSet[mi] = true
		}
		for _, rng := range mergeContextRanges(fm.idxs, contextLines, len(lines), maxResults-matchCount) {
			if b.Len() > 0 {
				b.WriteString("--\n")
			}
			for i := rng.start; i < rng.end; i++ {
				if matchSet[i] {
					b.WriteString(formatLegacyLine(fm.rel, i, lines[i]))
					matchCount++
				} else {
					b.WriteString(formatContextLine(fm.rel, i, lines[i]))
				}
			}
		}
	}
	if b.Len() == 0 {
		return NewTextResult("No matches found"), nil
	}
	header := fmt.Sprintf("Found %d %s", total, grepMatchNoun(total))
	if total > matchCount {
		header = fmt.Sprintf("Found %d of %d matches", matchCount, total)
	}
	return NewTextResult(header + "\n\n" + b.String()), nil
}

func grepMatchNoun(count int) string {
	if count == 1 {
		return "match"
	}
	return "matches"
}

func braceExpand(pattern string) []string {
	depth := 0
	start := -1
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				body := pattern[start+1 : i]
				prefix, suffix := pattern[:start], pattern[i+1:]
				var out []string
				for _, alt := range strings.Split(body, ",") {
					for _, rest := range braceExpand(prefix + alt + suffix) {
						out = append(out, rest)
					}
				}
				return out
			}
		}
	}
	return []string{pattern}
}

const searchWorkers = 8

func readLinesForContext(fp string) []string {
	f, err := os.Open(fp)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		if len(lines) >= searchFileCap {
			break
		}
		line := util.DecodeToUTF8(scanner.Bytes())
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines
}

func formatMatchLine(lineNo int, text string) string {
	return fmt.Sprintf("Line %d: %s", lineNo, text)
}

func formatLegacyLine(file string, idx int, text string) string {
	return fmt.Sprintf("%s:%d: %s\n", file, idx+1, capLineBytes(text))
}

func formatContextLine(file string, idx int, text string) string {
	return fmt.Sprintf("%s-%d: %s\n", file, idx+1, capLineBytes(text))
}

func capLineBytes(text string) string {
	if len(text) <= searchLineBytes {
		return text
	}
	cut := searchLineBytes
	for cut > 0 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return text[:cut] + " (line truncated)"
}

type lineRange struct{ start, end int }

func mergeContextRanges(matchIdx []int, ctxLines, totalLines, limit int) []lineRange {
	var ranges []lineRange
	for _, mi := range matchIdx {
		if len(ranges) >= limit {
			break
		}
		start := mi - ctxLines
		if start < 0 {
			start = 0
		}
		end := mi + ctxLines + 1
		if end > totalLines {
			end = totalLines
		}
		ranges = append(ranges, lineRange{start, end})
	}
	if len(ranges) == 0 {
		return nil
	}
	merged := []lineRange{ranges[0]}
	for _, r := range ranges[1:] {
		cur := &merged[len(merged)-1]
		if r.start <= cur.end {
			if r.end > cur.end {
				cur.end = r.end
			}
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}
