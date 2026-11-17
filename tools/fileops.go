package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)


type DeleteFileTool struct{ _FsTool }

func NewDeleteFileTool(workspace string) *DeleteFileTool {
	return &DeleteFileTool{_FsTool{workspace: workspace}}
}

func (t *DeleteFileTool) Name() string { return "delete_file" }
func (t *DeleteFileTool) Description() string {
	return "Delete a file, or a directory. Use recursive=true to delete a directory together with all of its contents."
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
		n, err := removeTree(ctx, fp)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return NewTextResult(fmt.Sprintf("Interrupted: delete of %s was cancelled after removing %d entries. The tree is PARTIALLY deleted; re-run the delete to finish it or verify the remainder before assuming it is gone.", path, n)), nil
			}
			return NewTextResult(fmt.Sprintf("Error deleting directory tree (removed %d entries before failing): %s", n, err)), nil
		}
		return NewTextResult(fmt.Sprintf("Successfully deleted directory tree %s", fp)), nil
	}

	if err := os.Remove(fp); err != nil {
		return NewTextResult(fmt.Sprintf("Error deleting file: %s", err)), nil
	}
	return NewTextResult(fmt.Sprintf("Successfully deleted file %s", fp)), nil
}

func removeTree(ctx context.Context, dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			m, err := removeTree(ctx, p)
			n += m
			if err != nil {
				return n, err
			}
		} else {
			if err := os.Remove(p); err != nil {
				return n, err
			}
			n++
		}
	}
	if err := os.Remove(dir); err != nil {
		return n, err
	}
	return n, nil
}

type MoveFileTool struct{ _FsTool }

func NewMoveFileTool(workspace string) *MoveFileTool {
	return &MoveFileTool{_FsTool{workspace: workspace}}
}

func (t *MoveFileTool) Name() string { return "move_file" }
func (t *MoveFileTool) Description() string {
	return "Move or rename a file or directory. The destination parent directory is created when missing. If the destination is an existing directory (or ends with a path separator), the source is moved into it."
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
		if cerr := copyTree(sf, df); cerr != nil {
			return NewTextResult(fmt.Sprintf("Error moving %s: %s (rename failed: %s)", src, cerr, err)), nil
		}
	}
	return NewTextResult(fmt.Sprintf("Successfully moved %s -> %s", sf, df)), nil
}

type CopyFileTool struct{ _FsTool }

func NewCopyFileTool(workspace string) *CopyFileTool {
	return &CopyFileTool{_FsTool{workspace: workspace}}
}

func (t *CopyFileTool) Name() string { return "copy_file" }
func (t *CopyFileTool) Description() string {
	return "Copy a file or directory to a new path. The source is left in place. Use recursive=true to copy a directory together with all of its contents. The destination parent directory is created when missing. If the destination is an existing directory (or ends with a path separator), the source is copied into it, keeping its base name."
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

	dinfo, err := os.Stat(df)
	if (err == nil && dinfo.IsDir()) ||
		strings.HasSuffix(dst, string(os.PathSeparator)) ||
		strings.HasSuffix(dst, "/") {
		df = filepath.Join(df, filepath.Base(sf))
	}

	if sf == df {
		return NewTextResult(fmt.Sprintf("Nothing to do: source and destination are the same path (%s)", sf)), nil
	}
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

func CopyTree(src, dst string) error { return copyTreeKeep(src, dst) }

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
