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
		"context lines are printed as 'path-line: content' and groups are separated by '--'."
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
	searchLineCap    = 240 // per-line display cap
	searchFileCap    = 200000
	searchMaxContext = 20 // max context lines per match (like grep -C)
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

	// Files are scanned by a small worker pool: search is the tool the model
	// reaches for on every unfamiliar tree, and a serial filepath.Walk on a
	// warm repo (~50k files) spends most of its time waiting on page cache and
	// antivirus, not on the regex. Results are collected per file and sorted
	// afterwards, so parallelism never changes the output order.
	type fileHit struct {
		abs   string
		rel   string
		idxs  []int    // 0-based matched line indices
		texts []string // matched line text, parallel to idxs
	}
	var (
		hits          []fileHit
		mu            sync.Mutex
		wg            sync.WaitGroup
		total         int
		filesScanned  int
		truncatedFile bool
	)
	noteScan := func(scanned, truncated bool) {
		mu.Lock()
		if scanned {
			filesScanned++
		}
		if truncated {
			truncatedFile = true
		}
		mu.Unlock()
	}
	scanOne := func(fp string) {
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

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		var idxs []int
		var texts []string
		lineNo := 0
		truncated := false
		for scanner.Scan() {
			lineNo++
			if lineNo > searchFileCap {
				truncated = true
				break
			}
			// Per-line decode: a GBK lead byte never spans the 0x0A that
			// split the line, so decoding line by line is exact (the same
			// reasoning as read_file's streaming path). Without this a GBK
			// file's matches reach the model as U+FFFD noise.
			line := util.DecodeToUTF8(scanner.Bytes())
			// Strip the trailing \r of CRLF files (the Windows default):
			// without this every displayed line ends with a "\r" escape,
			// which wastes tokens and distorts the model's view. Display
			// only; the file on disk is untouched.
			line = strings.TrimSuffix(line, "\r")
			if re.MatchString(line) {
				idxs = append(idxs, lineNo-1)
				texts = append(texts, line)
			}
		}
		noteScan(true, truncated)
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
		// Walk on the caller goroutine: it only reads directory entries, and
		// doing it here keeps cancellation immediate (SkipAll on ctx).
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
		return NewTextResult(fmt.Sprintf("No matches for %q in %s (%d files scanned).", pattern, root, filesScanned)), nil
	}

	// Deterministic ordering: by file, then line number. The scan itself is
	// concurrent, so this sort is what keeps the output identical to the
	// serial version regardless of which worker finished first.
	sort.Slice(hits, func(i, j int) bool { return hits[i].rel < hits[j].rel })

	var b strings.Builder
	shown := 0
	if contextLines == 0 {
		// Match lines only (legacy output, no context, no separators).
		for _, fm := range hits {
			for k, mi := range fm.idxs {
				if shown >= maxResults {
					break
				}
				b.WriteString(formatMatchLine(fm.rel, mi, fm.texts[k]))
				shown++
			}
			if shown >= maxResults {
				break
			}
		}
	} else {
		// Match lines plus N context lines each side; groups separated by "--".
		// Context needs the surrounding lines, which the scan no longer keeps
		// for every file (that was O(all lines of every matching file) memory);
		// only the few files that actually matched are re-read here.
		for _, fm := range hits {
			if shown >= maxResults {
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
			for _, rng := range mergeContextRanges(fm.idxs, contextLines, len(lines), maxResults-shown) {
				if b.Len() > 0 {
					b.WriteString("--\n")
				}
				for i := rng.start; i < rng.end; i++ {
					if matchSet[i] {
						b.WriteString(formatMatchLine(fm.rel, i, lines[i]))
						shown++
					} else {
						b.WriteString(formatContextLine(fm.rel, i, lines[i]))
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
	if truncatedFile {
		b.WriteString("; some very long files were truncated")
	}
	b.WriteString(")")
	return NewTextResult(b.String()), nil
}

// searchWorkers bounds the file-scan pool. Reading a file is dominated by I/O
// and page-cache waits, so the pool can be a little wider than the CPU count
// without hurting anything; it must stay bounded so a search over a huge tree
// does not open thousands of descriptors at once.
const searchWorkers = 8

// readLinesForContext re-reads one matching file to supply the lines around a
// match. Only files that actually matched are re-read (the scan keeps just the
// matching lines), which is why the whole scan no longer holds every line of
// every file in memory.
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
