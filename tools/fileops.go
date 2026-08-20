package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// This file adds the missing native file-operation tools: delete_file and
// move_file. Before this, deleting or renaming a file required the exec
// tool (del/move in cmd.exe) — which is fragile for the model (quoting,
// syntax) and is partly blocked by the dangerous-command guard (del /f and
// rmdir /s are denied), so "delete this directory tree" had no reliable
// path at all. Native tools make these operations as safe and predictable
// as read_file / write_file / edit_file.

// DeleteFileTool
type DeleteFileTool struct{ _FsTool }

func NewDeleteFileTool(workspace string) *DeleteFileTool {
	return &DeleteFileTool{_FsTool{workspace: workspace}}
}

func (t *DeleteFileTool) Name() string { return "delete_file" }
func (t *DeleteFileTool) Description() string {
	return "Delete a file, or a directory. Use recursive=true to delete a directory together with all of its contents. Prefer this tool over shell commands (del/rm) for reliable deletion."
}
func (t *DeleteFileTool) IsHidden() bool { return false }
func (t *DeleteFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory path to delete",
			},
			"recursive": map[string]interface{}{
				"type":        "boolean",
				"description": "Required when deleting a non-empty directory: deletes the whole tree. Default false.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *DeleteFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("missing path")
	}
	recursive, _ := params["recursive"].(bool)

	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: path not found: %s", path)), nil
	}

	if info.IsDir() {
		if !recursive {
			entries, _ := os.ReadDir(fp)
			if len(entries) > 0 {
				return NewTextResult(fmt.Sprintf("Error: %s is a non-empty directory. Re-run with recursive=true to delete it and everything inside.", path)), nil
			}
			if err := os.Remove(fp); err != nil {
				return NewTextResult(fmt.Sprintf("Error deleting directory: %s", err)), nil
			}
			return NewTextResult(fmt.Sprintf("Successfully deleted empty directory %s", fp)), nil
		}
		if err := os.RemoveAll(fp); err != nil {
			return NewTextResult(fmt.Sprintf("Error deleting directory tree: %s", err)), nil
		}
		return NewTextResult(fmt.Sprintf("Successfully deleted directory tree %s", fp)), nil
	}

	if err := os.Remove(fp); err != nil {
		return NewTextResult(fmt.Sprintf("Error deleting file: %s", err)), nil
	}
	return NewTextResult(fmt.Sprintf("Successfully deleted file %s", fp)), nil
}

// MoveFileTool
type MoveFileTool struct{ _FsTool }

func NewMoveFileTool(workspace string) *MoveFileTool {
	return &MoveFileTool{_FsTool{workspace: workspace}}
}

func (t *MoveFileTool) Name() string { return "move_file" }
func (t *MoveFileTool) Description() string {
	return "Move or rename a file or directory. The destination parent directory is created when missing. If the destination is an existing directory (or ends with a path separator), the source is moved into it. Prefer this tool over shell move/rename commands."
}
func (t *MoveFileTool) IsHidden() bool { return false }
func (t *MoveFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source": map[string]interface{}{
				"type":        "string",
				"description": "Path of the file or directory to move",
			},
			"destination": map[string]interface{}{
				"type":        "string",
				"description": "New path (or an existing directory to move into)",
			},
		},
		"required": []string{"source", "destination"},
	}
}

func (t *MoveFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	src, _ := params["source"].(string)
	dst, _ := params["destination"].(string)
	if src == "" || dst == "" {
		return nil, fmt.Errorf("missing required parameters (source, destination)")
	}

	sf, err := t.resolvePath(src)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	df, err := t.resolvePath(dst)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}

	if _, err := os.Stat(sf); err != nil {
		return NewTextResult(fmt.Sprintf("Error: source not found: %s", src)), nil
	}

	// Destination is a directory (existing, or marked with a trailing
	// separator) -> move the source INTO it, keeping its base name.
	dinfo, err := os.Stat(df)
	if (err == nil && dinfo.IsDir()) ||
		strings.HasSuffix(dst, string(os.PathSeparator)) ||
		strings.HasSuffix(dst, "/") {
		df = filepath.Join(df, filepath.Base(sf))
	}

	if sf == df {
		return NewTextResult(fmt.Sprintf("Nothing to do: source and destination are the same path (%s)", sf)), nil
	}
	if _, err := os.Stat(df); err == nil {
		return NewTextResult(fmt.Sprintf("Error: destination already exists: %s", df)), nil
	}
	if err := os.MkdirAll(filepath.Dir(df), 0755); err != nil {
		return NewTextResult(fmt.Sprintf("Error moving %s: %s", src, err)), nil
	}

	if err := os.Rename(sf, df); err != nil {
		// Cross-filesystem move (e.g. across drives on Windows) cannot be a
		// rename; fall back to copy + delete.
		if cerr := copyTree(sf, df); cerr != nil {
			return NewTextResult(fmt.Sprintf("Error moving %s: %s (rename failed: %s)", src, cerr, err)), nil
		}
	}
	return NewTextResult(fmt.Sprintf("Successfully moved %s -> %s", sf, df)), nil
}

// CopyFileTool
type CopyFileTool struct{ _FsTool }

func NewCopyFileTool(workspace string) *CopyFileTool {
	return &CopyFileTool{_FsTool{workspace: workspace}}
}

func (t *CopyFileTool) Name() string { return "copy_file" }
func (t *CopyFileTool) Description() string {
	return "Copy a file or directory to a new path. The source is left in place. Use recursive=true to copy a directory together with all of its contents. The destination parent directory is created when missing. If the destination is an existing directory (or ends with a path separator), the source is copied into it, keeping its base name. Prefer this tool over shell copy/xcopy commands."
}
func (t *CopyFileTool) IsHidden() bool { return false }
func (t *CopyFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source": map[string]interface{}{
				"type":        "string",
				"description": "Path of the file or directory to copy",
			},
			"destination": map[string]interface{}{
				"type":        "string",
				"description": "New path (or an existing directory to copy into)",
			},
			"recursive": map[string]interface{}{
				"type":        "boolean",
				"description": "Required when copying a non-empty directory: copies the whole tree. Default false.",
			},
		},
		"required": []string{"source", "destination"},
	}
}

func (t *CopyFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	src, _ := params["source"].(string)
	dst, _ := params["destination"].(string)
	recursive, _ := params["recursive"].(bool)
	if src == "" || dst == "" {
		return nil, fmt.Errorf("missing required parameters (source, destination)")
	}

	sf, err := t.resolvePath(src)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	df, err := t.resolvePath(dst)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}

	sinfo, err := os.Stat(sf)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: source not found: %s", src)), nil
	}

	// Destination is a directory (existing, or marked with a trailing
	// separator) -> copy the source INTO it, keeping its base name.
	dinfo, err := os.Stat(df)
	if (err == nil && dinfo.IsDir()) ||
		strings.HasSuffix(dst, string(os.PathSeparator)) ||
		strings.HasSuffix(dst, "/") {
		df = filepath.Join(df, filepath.Base(sf))
	}

	if sf == df {
		return NewTextResult(fmt.Sprintf("Nothing to do: source and destination are the same path (%s)", sf)), nil
	}
	// Copying a directory into itself (or into one of its subdirectories)
	// would recurse forever; reject it up front.
	if sinfo.IsDir() {
		if rel, rerr := filepath.Rel(sf, df); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return NewTextResult(fmt.Sprintf("Error: destination %s is inside the source directory %s — copying a directory into itself is not allowed.", df, sf)), nil
		}
	}
	if _, err := os.Stat(df); err == nil {
		return NewTextResult(fmt.Sprintf("Error: destination already exists: %s (delete it first or choose a different name)", df)), nil
	}
	if err := os.MkdirAll(filepath.Dir(df), 0755); err != nil {
		return NewTextResult(fmt.Sprintf("Error copying %s: %s", src, err)), nil
	}

	if sinfo.IsDir() {
		if !recursive {
			entries, _ := os.ReadDir(sf)
			if len(entries) > 0 {
				return NewTextResult(fmt.Sprintf("Error: %s is a non-empty directory. Re-run with recursive=true to copy it and everything inside.", src)), nil
			}
			if err := os.MkdirAll(df, sinfo.Mode()); err != nil {
				return NewTextResult(fmt.Sprintf("Error copying directory: %s", err)), nil
			}
			return NewTextResult(fmt.Sprintf("Successfully copied empty directory %s -> %s", sf, df)), nil
		}
		if err := copyTreeKeep(sf, df); err != nil {
			return NewTextResult(fmt.Sprintf("Error copying directory tree: %s", err)), nil
		}
		return NewTextResult(fmt.Sprintf("Successfully copied directory tree %s -> %s (%d files)", sf, df, countTree(df))), nil
	}

	if err := copyFile(sf, df); err != nil {
		return NewTextResult(fmt.Sprintf("Error copying file: %s", err)), nil
	}
	return NewTextResult(fmt.Sprintf("Successfully copied %s -> %s", sf, df)), nil
}

// copyTreeKeep copies a file or a whole directory tree from src to dst,
// leaving the source in place (unlike copyTree, the move fallback, which
// deletes the source afterwards).
func copyTreeKeep(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTreeKeep(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// countTree counts the regular files under root (recursively).
func countTree(root string) int {
	n := 0
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !fi.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// copyTree copies a file or a whole directory tree from src to dst. It is
// the fallback for os.Rename when the move crosses filesystems.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return os.RemoveAll(src)
}

// copyFile copies a single file, preserving the content bytes.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
