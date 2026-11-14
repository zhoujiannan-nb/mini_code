package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// GlobTool finds files by name pattern — the dsh-aligned shape of it: the
// model sees `rg --files --hidden --no-ignore` results (files only, ignored
// and hidden files included, only VCS metadata directories pruned), sorted
// by modification time, capped at 100 with the complete list spilled to a
// file it can read. The old mc shape (sorted paths, "(N matches)" trailer,
// node_modules silently skipped) made searches come back empty for files the
// model then had to hunt for with a second call.
type GlobTool struct{ _FsTool }

func NewGlobTool(workspace string) *GlobTool {
	return &GlobTool{_FsTool{workspace: workspace}}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files, not directories, whose paths match a glob pattern, including hidden and ignored files. Returns up to 100 paths in modification-time order; a larger result keeps the first paths and reports where the complete list was saved."
}

func (t *GlobTool) IsHidden() bool { return false }

func (t *GlobTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to match file paths against (e.g. \"**/*.ts\", \"src/**/*.test.js\"). A pattern with no \"/\" matches the basename at any depth, so \"*\" and \"*.ts\" both search the whole tree; include a separator to anchor the depth.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in. Defaults to the session workspace; a relative path resolves against it.",
			},
		},
		"required": []string{"pattern"},
	}
}

// globMaxResults is the dsh default cap (globSchema MAX_RESULT_PATHS): 100.
const globMaxResults = 100

// vcsMetaDirs are the only directories the glob walk prunes — ripgrep skips
// VCS metadata unconditionally and everything else (node_modules, ignored,
// hidden) is part of the result. Pruning more was mc-only and made the model
// re-search for files it had been told did not exist.
var vcsMetaDirs = map[string]bool{".git": true, ".hg": true, ".svn": true, ".bzr": true, "_darcs": true}

// globEntry pairs a matched path with its modification time for ordering.
type globEntry struct {
	rel string
	mod time.Time
}

func (t *GlobTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	pattern := strings.TrimSpace(strParam(params, "pattern"))
	// dsh glob.js schema texts, verbatim.
	if pattern == "" {
		return NewTextResult("Error: invalid arguments: pattern must be a non-empty string"), nil
	}

	root := t.workspace
	// dsh's glob.js 原文："path must be a non-empty string when given"。
	if _, present := params["path"]; present {
		p := strings.TrimSpace(strParam(params, "path"))
		if p == "" {
			return NewTextResult("Error: invalid arguments: path must be a non-empty string when given"), nil
		}
		rp, err := t.resolvePath(p)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error: invalid arguments: %s", err)), nil
		}
		root = rp
	}
	info, err := os.Stat(root)
	if err != nil {
		return NewTextResult("Error: invalid arguments: path must be a directory"), nil
	}
	if !info.IsDir() {
		return NewTextResult("Error: invalid arguments: path must be a directory"), nil
	}

	maxResults := globMaxResults

	matched, err := matchedFuncFor(pattern)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: invalid pattern %q: %s", pattern, err)), nil
	}

	var found []globEntry
	filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d == nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		// A cancelled call must stop walking: on a large tree this loop is
		// the difference between "stop" taking effect now and taking seconds.
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if p == root {
			return nil
		}
		if d.IsDir() && vcsMetaDirs[d.Name()] {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil // dsh's glob returns files, not directories
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		// Matching sees the path relative to the search root (that is what
		// rg's pattern is anchored to); the displayed path stays relative to
		// the workspace root so it can be read back directly.
		relScope, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if matched(filepath.ToSlash(relScope)) {
			rel := filepath.ToSlash(relScope)
			if root != t.workspace {
				if relRoot, rerr2 := filepath.Rel(t.workspace, p); rerr2 == nil {
					rel = filepath.ToSlash(relRoot)
				}
			}
			found = append(found, globEntry{rel: rel, mod: fi.ModTime()})
		}
		return nil
	})

	if len(found) == 0 {
		return NewTextResult("No files found"), nil
	}
	// Modification-time order, newest first; ties broken by path so two runs
	// over the same tree are byte-identical (stable model-visible output).
	sort.Slice(found, func(i, j int) bool {
		if !found[i].mod.Equal(found[j].mod) {
			return found[i].mod.After(found[j].mod)
		}
		return found[i].rel < found[j].rel
	})

	total := len(found)
	rels := make([]string, total)
	for i, e := range found {
		rels[i] = e.rel
	}
	if total > maxResults {
		// dsh names the file holding the COMPLETE sorted list, so narrowing
		// or paging the result costs one read instead of another search.
		text := strings.Join(rels[:maxResults], "\n")
		if loc := spoolWrite("glob", strings.Join(rels, "\n")+"\n"); loc != "" {
			text += fmt.Sprintf("\n(Full sorted result stored at: %s. Read it or narrow pattern or path to see more.)", loc)
		}
		return NewTextResult(text), nil
	}
	return NewTextResult(strings.Join(rels, "\n")), nil
}

// matchedFuncFor compiles pattern into a matcher over a workspace-relative
// path, mirroring rg --glob's rules: a pattern without '/' matches the
// basename at any depth; a pattern with '/' is anchored at the search root
// and '*' never crosses '/'; "**" crosses separators.
func matchedFuncFor(pattern string) (func(string) bool, error) {
	pattern = filepath.ToSlash(pattern)
	hasSlash := strings.Contains(pattern, "/")
	segments := strings.Split(pattern, "/")
	depth := len(segments)

	compileSegment := func(seg string) (*regexp.Regexp, error) {
		if seg == "" || seg == "**" {
			return nil, nil
		}
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(seg); i++ {
			switch c := seg[i]; c {
			case '*':
				b.WriteString("[^/]*")
			case '?':
				b.WriteString("[^/]")
			case '[':
				end := i + 1
				if end < len(seg) && (seg[end] == '!' || seg[end] == '^') {
					end++
				}
				for end < len(seg) && seg[end] != ']' {
					end++
				}
				if end >= len(seg) {
					b.WriteString(regexp.QuoteMeta("["))
				} else {
					charClass := seg[i+1 : end]
					if strings.HasPrefix(charClass, "!") {
						charClass = "^" + charClass[1:]
					}
					b.WriteString("[" + charClass + "]")
					i = end
				}
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteString("$")
		return regexp.Compile(b.String())
	}

	segRes := make([]*regexp.Regexp, 0, len(segments))
	for _, seg := range segments {
		re, err := compileSegment(seg)
		if err != nil {
			return nil, err
		}
		segRes = append(segRes, re)
	}

	literal := stripGlobMeta(pattern)
	hasMeta := literal != pattern

	return func(rel string) bool {
		parts := strings.Split(rel, "/")
		if hasSlash {
			// Anchored pattern: segment-wise compare; "**" matches any run
			// of segments. Trailing "**" may match fewer segments than the
			// pattern lists only when "**" occupies it.
			if len(parts) < len(segments) {
				// "src/**/*.go" against "src/app.go": allowed when the "**"
				// segment collapses to zero.
				if !segmentsCollapse(segments, len(parts)) {
					return false
				}
			}
			pi := 0
			for si := 0; si < len(segments); si++ {
				if segments[si] != "**" {
					if pi >= len(parts) {
						return false
					}
					if segRes[si] != nil && !segRes[si].MatchString(parts[pi]) {
						return false
					}
					pi++
					continue
				}
				// "**": consume segments until the next literal segment
				// matches (greedy backtracking is overkill for typical
				// patterns; a simple scan finds the earliest match).
				if si == len(segments)-1 {
					return true // trailing ** matches the rest
				}
				next := segRes[si+1]
				foundIdx := false
				for pi < len(parts) {
					if next == nil || next.MatchString(parts[pi]) {
						foundIdx = true
						break
					}
					pi++
				}
				if !foundIdx {
					return false
				}
			}
			return pi == len(parts) || segments[len(segments)-1] == "**"
		}
		// Basename pattern: the last segment against the last part, at any
		// depth. A fully literal pattern falls back to substring matching of
		// the name (mc's historical "*" behavior stays).
		name := parts[len(parts)-1]
		seg := segRes[depth-1]
		if seg != nil && !seg.MatchString(name) {
			return false
		}
		if !hasMeta {
			return strings.Contains(name, literal)
		}
		return true
	}, nil
}

// segmentsCollapse reports whether pattern segments can legally match a path
// with n parts (a "**" segment can absorb or vanish).
func segmentsCollapse(segments []string, n int) bool {
	stars := 0
	for _, s := range segments {
		if s == "**" {
			stars++
		}
	}
	return n >= len(segments)-stars
}

// stripGlobMeta removes glob metacharacters from a pattern.
func stripGlobMeta(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		if r == '*' || r == '?' || r == '[' || r == ']' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
