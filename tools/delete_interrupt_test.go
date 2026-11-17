package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildDeleteTree(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		d := filepath.Join(dir, fmt.Sprintf("sub%02d", i%10))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f%03d.dat", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestDeleteRecursiveHonoursCancellation(t *testing.T) {
	ws := t.TempDir()
	tree := filepath.Join(ws, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	buildDeleteTree(t, tree, 200)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewDeleteFileTool(ws)
	res, err := tool.Execute(ctx, map[string]interface{}{"path": tree, "recursive": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Interrupted") || !strings.Contains(res.Text, "PARTIALLY deleted") {
		t.Fatalf("expected an interrupted partial-delete report, got %q", res.Text)
	}
	if got := countFiles(t, tree); got != 200 {
		t.Fatalf("pre-cancelled context must delete nothing, files left = %d", got)
	}
}

func TestDeleteRecursiveStillCompletes(t *testing.T) {
	ws := t.TempDir()
	tree := filepath.Join(ws, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	buildDeleteTree(t, tree, 200)

	tool := NewDeleteFileTool(ws)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": tree, "recursive": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Successfully deleted directory tree") {
		t.Fatalf("expected success text, got %q", res.Text)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("tree must be gone, stat err = %v", err)
	}
}
