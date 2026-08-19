package tools

import (
	"context"
	"fmt"
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

// ReadFileTool
type ReadFileTool struct{ _FsTool }

func NewReadFileTool(workspace string) *ReadFileTool {
	return &ReadFileTool{_FsTool{workspace: workspace}}
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read file contents with line numbers. Supports pagination via offset/limit. For images, use the readimg tool instead."
}
func (t *ReadFileTool) IsHidden() bool { return false }
func (t *ReadFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":   map[string]interface{}{"type": "string", "description": "File path to read"},
			"offset": map[string]interface{}{"type": "integer", "description": "Start line (1-indexed)", "minimum": 1},
			"limit":  map[string]interface{}{"type": "integer", "description": "Max lines to return (default 2000)", "minimum": 1},
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
		return NewTextResult(fmt.Sprintf("Error: file not found: %s", path)), nil
	}
	if info.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: not a file: %s", path)), nil
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err)), nil
	}
	if len(raw) == 0 {
		return NewTextResult(fmt.Sprintf("(Empty file: %s)", path)), nil
	}

	// DecodeToUTF8: text files on Windows are often GBK (ANSI) encoded;
	// without this the raw bytes become U+FFFD noise once the tool result
	// is JSON-marshaled for the LLM or persisted.
	content := util.DecodeToUTF8(raw)
	lines := strings.Split(content, "\n")
	total := len(lines)

	offset := 1
	if v, ok := params["offset"].(float64); ok && int(v) > 0 {
		offset = int(v)
	}
	limit := 2000
	if v, ok := params["limit"].(float64); ok && int(v) > 0 {
		limit = int(v)
	}

	if offset < 1 {
		offset = 1
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
	} else {
		result += fmt.Sprintf("\n\n(End of file — %d lines total)", total)
	}
	return NewTextResult(result), nil
}

// WriteFileTool
type WriteFileTool struct{ _FsTool }

func NewWriteFileTool(workspace string) *WriteFileTool {
	return &WriteFileTool{_FsTool{workspace: workspace}}
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Write content to a file. Creates parent directories as needed."
}
func (t *WriteFileTool) IsHidden() bool { return false }
func (t *WriteFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]interface{}{"type": "string", "description": "File path to write to"},
			"content": map[string]interface{}{"type": "string", "description": "Content to write"},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
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
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	return NewTextResult(fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), fp)), nil
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
		return NewTextResult(fmt.Sprintf("Error: file not found: %s", path)), nil
	}

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

	if !strings.Contains(content, oldNorm) {
		return NewTextResult(fmt.Sprintf("Error: old_text not found in %s. Verify the file content.", path)), nil
	}

	count := strings.Count(content, oldNorm)
	if count > 1 && !replaceAll {
		return NewTextResult(fmt.Sprintf("Warning: old_text appears %d times. Provide more context or set replace_all=true.", count)), nil
	}

	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldNorm, newNorm)
	} else {
		newContent = strings.Replace(content, oldNorm, newNorm, 1)
	}

	if err := os.WriteFile(fp, []byte(newContent), 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error editing file: %s", err)), nil
	}
	return NewTextResult(fmt.Sprintf("Successfully edited %s", fp)), nil
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
	return "List directory contents. Set recursive=true for nested listing."
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
	depthLimit := 3
	if v, ok := params["max_depth"].(float64); ok && int(v) > 0 {
		depthLimit = int(v)
	}

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
					items = append(items, rel)
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
					items = append(items, e.Name())
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
