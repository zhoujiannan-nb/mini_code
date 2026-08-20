package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GlobTool locates files and directories by name pattern (glob). Before this
// tool, "find every *.py under the workspace" had to be done with a shell
// command (dir /s /b on Windows, find on POSIX) — which is fragile for the
// model (quoting, wildcard dialects, and dir's header/size/date output is
// hard to parse) and behaves differently per OS. A native glob tool gives a
// clean, platform-independent, quote-safe way to list matching paths.
//
// Pattern semantics (like gitignore / doublestar):
//   - '*'  matches any run of characters within one path segment (no '/')
//   - '?'  matches exactly one character within one segment
//   - '[...]' character class (leading '!' or '^' negates)
//   - '**' matches any run of characters INCLUDING '/' (crosses directories)
//   - a pattern with no '**' matches only within the search root (no recursion)
type GlobTool struct{ _FsTool }

func NewGlobTool(workspace string) *GlobTool {
	return &GlobTool{_FsTool{workspace: workspace}}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files and directories by name pattern (glob). " +
		"'*' matches within one path segment, '?' one character, '[...]' a character class, " +
		"'**' crosses directories (e.g. '**/*.go', 'src/**/*.py'). " +
		"A pattern without '**' matches only inside the search root. " +
		"Returns matching paths relative to the search root (directories end with '/'), sorted, capped by max_results. " +
		"Prefer this over shell dir/find when locating files by name."
}

func (t *GlobTool) IsHidden() bool { return false }

func (t *GlobTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern, e.g. '*.go', 'src/*.py', '**/*.md'",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in (default: the workspace root)",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of paths to return (default 200, max 1000)",
				"minimum":     1,
				"maximum":     1000,
			},
		},
		"required": []string{"pattern"},
	}
}

const (
	globDefaultMax = 200
	globHardMax    = 1000
)

func (t *GlobTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
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
	if !info.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: not a directory: %s", root)), nil
	}

	maxResults := intParam(params, "max_results", globDefaultMax)
	if maxResults > globHardMax {
		maxResults = globHardMax
	}

	re, err := globToRegex(pattern)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: invalid pattern %q: %s", pattern, err)), nil
	}

	var matches []string
	total := 0
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if p == root {
			return nil
		}
		// Skip the same noise directories the other fs tools ignore.
		if fi.IsDir() && ignoreDirs[fi.Name()] {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if re.MatchString(relSlash) {
			total++
			if len(matches) < maxResults {
				if fi.IsDir() {
					matches = append(matches, relSlash+"/")
				} else {
					matches = append(matches, relSlash)
				}
			}
		}
		return nil
	})

	if len(matches) == 0 {
		return NewTextResult(fmt.Sprintf("No files match %q in %s.", pattern, root)), nil
	}
	sort.Strings(matches)
	result := strings.Join(matches, "\n")
	if total > len(matches) {
		result += fmt.Sprintf("\n\n(truncated, showing first %d of %d matches)", len(matches), total)
	} else {
		result += fmt.Sprintf("\n\n(%d matches)", total)
	}
	return NewTextResult(result), nil
}

// globToRegex converts a glob pattern into an anchored regular expression.
// It handles '*', '?', '[...]' and the recursive '**' (which may span '/').
func globToRegex(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			// '**' crosses directory boundaries.
			b.WriteString(".*")
			i += 2
			// A following '/' after '**' is optional so '**/x' also matches 'x'.
			if i < len(pattern) && pattern[i] == '/' {
				b.WriteString("(?:/)?")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
			i++
		case c == '?':
			b.WriteString("[^/]")
			i++
		case c == '[':
			j := i + 1
			b.WriteByte('[')
			if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
				b.WriteByte('^')
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				b.WriteByte(pattern[j])
				j++
			}
			if j < len(pattern) {
				b.WriteByte(']')
				j++
			}
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
