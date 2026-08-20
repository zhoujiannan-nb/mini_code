package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SearchTool is a grep-like tool: it locates matching lines across the
// workspace without the model having to read whole files or fight shell
// quoting (findstr on Windows). It returns "path:line: content" entries,
// capped by max_results, plus a total count of matching lines.
type SearchTool struct{ _FsTool }

func NewSearchTool(workspace string) *SearchTool {
	return &SearchTool{_FsTool{workspace: workspace}}
}

func (t *SearchTool) Name() string { return "search_files" }

func (t *SearchTool) Description() string {
	return "Search for a pattern (plain text or regex) across files in the workspace. " +
		"Returns matching lines as 'path:line: content' with a total match count. " +
		"Set context=N to also show N lines before/after each match (like grep -C): " +
		"context lines are printed as 'path-line: content' and groups are separated by '--'. " +
		"Use it to locate definitions, usages, constants or occurrences without reading whole files."
}

func (t *SearchTool) IsHidden() bool { return false }

func (t *SearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Pattern to search for: plain text, or a regex (RE2 syntax) if it compiles",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory or file to search in (default: the workspace root)",
			},
			"include": map[string]interface{}{
				"type":        "string",
				"description": "Only search files whose name matches this glob, e.g. \"*.go\" or \"*.log\"",
			},
			"ignore_case": map[string]interface{}{
				"type":        "boolean",
				"description": "Case-insensitive matching (default false)",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of matching lines to return (default 100, max 500)",
				"minimum":     1,
				"maximum":     500,
			},
			"context": map[string]interface{}{
				"type":        "integer",
				"description": "Number of context lines to show before and after each match, like grep -C (default 0 = match lines only, max 20)",
				"minimum":     0,
				"maximum":     20,
			},
		},
		"required": []string{"pattern"},
	}
}

const (
	searchDefaultMax = 100
	searchHardMax    = 500
	searchLineCap    = 240  // per-line display cap
	searchFileCap    = 200000
	searchMaxContext = 20   // max context lines per match (like grep -C)
)

// intParam reads a positive integer parameter that may arrive as float64
// (JSON unmarshal), int or int64 (programmatic callers).
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
	pattern, _ := params["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return NewTextResult("Error: missing pattern"), nil
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

	var include string
	if v, ok := params["include"].(string); ok {
		include = strings.TrimSpace(v)
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

	// Compile as regex when possible; otherwise fall back to a literal
	// (quoted) match so plain-text patterns always work.
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

	type fileMatch struct {
		file     string
		lines    []string
		matchIdx []int // 0-based line indices of matching lines
	}
	var files []fileMatch
	total := 0
	filesScanned := 0
	truncatedFiles := false

	visitFile := func(fp string) {
		if ctx.Err() != nil {
			return
		}
		if include != "" {
			ok, _ := filepath.Match(include, filepath.Base(fp))
			if !ok {
				return
			}
		}
		f, err := os.Open(fp)
		if err != nil {
			return
		}
		defer f.Close()

		// Skip binary files: NUL byte in the first 8KB.
		head := make([]byte, 8192)
		n, _ := f.Read(head)
		if bytes.IndexByte(head[:n], 0) >= 0 {
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return
		}
		filesScanned++

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var lines []string
		var matchIdx []int
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			if lineNo > searchFileCap {
				truncatedFiles = true
				break
			}
			line := scanner.Text()
			// Strip the trailing \r of CRLF files (the Windows default):
			// without this every displayed line ends with a "\r" escape,
			// which wastes tokens and distorts the model's view. Display
			// only; the file on disk is untouched.
			line = strings.TrimSuffix(line, "\r")
			lines = append(lines, line)
			if re.MatchString(line) {
				total++
				matchIdx = append(matchIdx, lineNo-1)
			}
		}
		if len(matchIdx) == 0 {
			return
		}
		rel := fp
		if r, err := filepath.Rel(t.workspace, fp); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		files = append(files, fileMatch{file: rel, lines: lines, matchIdx: matchIdx})
	}

	if info.IsDir() {
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				if p != root && ignoreDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			visitFile(p)
			return nil
		})
	} else {
		visitFile(root)
	}

	if total == 0 {
		return NewTextResult(fmt.Sprintf("No matches for %q in %s (%d files scanned).", pattern, root, filesScanned)), nil
	}

	// Deterministic ordering: by file, then line number.
	sort.Slice(files, func(i, j int) bool { return files[i].file < files[j].file })

	var b strings.Builder
	shown := 0
	if contextLines == 0 {
		// Match lines only (legacy output, no context, no separators).
		for _, fm := range files {
			for _, mi := range fm.matchIdx {
				if shown >= maxResults {
					break
				}
				b.WriteString(formatMatchLine(fm.file, mi, fm.lines[mi]))
				shown++
			}
			if shown >= maxResults {
				break
			}
		}
	} else {
		// Match lines plus N context lines each side; groups separated by "--".
		for _, fm := range files {
			if shown >= maxResults {
				break
			}
			matchSet := make(map[int]bool, len(fm.matchIdx))
			for _, mi := range fm.matchIdx {
				matchSet[mi] = true
			}
			for _, rng := range mergeContextRanges(fm.matchIdx, contextLines, len(fm.lines), maxResults-shown) {
				if b.Len() > 0 {
					b.WriteString("--\n")
				}
				for i := rng.start; i < rng.end; i++ {
					if matchSet[i] {
						b.WriteString(formatMatchLine(fm.file, i, fm.lines[i]))
						shown++
					} else {
						b.WriteString(formatContextLine(fm.file, i, fm.lines[i]))
					}
				}
			}
		}
	}

	if b.Len() == 0 {
		return NewTextResult(fmt.Sprintf("No matches for %q in %s (%d files scanned).", pattern, root, filesScanned)), nil
	}

	fmt.Fprintf(&b, "\n(%d matching lines total, %d files scanned", total, filesScanned)
	if total > shown {
		fmt.Fprintf(&b, "; showing first %d", shown)
	}
	if truncatedFiles {
		b.WriteString("; some very long files were truncated")
	}
	b.WriteString(")")
	return NewTextResult(b.String()), nil
}

// formatMatchLine renders a matching line as "path:lineNo: text".
func formatMatchLine(file string, idx int, text string) string {
	if len([]rune(text)) > searchLineCap {
		text = string([]rune(text)[:searchLineCap]) + "…"
	}
	return fmt.Sprintf("%s:%d: %s\n", file, idx+1, text)
}

// formatContextLine renders a context line as "path-lineNo: text".
func formatContextLine(file string, idx int, text string) string {
	if len([]rune(text)) > searchLineCap {
		text = string([]rune(text)[:searchLineCap]) + "…"
	}
	return fmt.Sprintf("%s-%d: %s\n", file, idx+1, text)
}

type lineRange struct{ start, end int } // [start, end), 0-based line indices

// mergeContextRanges builds display ranges for the first `limit` matches,
// each expanded by ctxLines on both sides and clipped to [0, totalLines).
// Overlapping or touching ranges are merged into one.
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
