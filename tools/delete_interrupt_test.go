package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildDeleteTree makes dir with n files spread over 10 subdirectories.
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

// TestDeleteRecursiveHonoursCancellation: a pre-cancelled context must stop
// the recursive delete before it removes anything, and the result must say
// the tree is PARTIALLY deleted — os.RemoveAll could not be interrupted at
// all, which made delete_file the one tool that ignored the stop button.
func TestDeleteRecursiveHonoursCancellation(t *testing.T) {
	ws := t.TempDir()
	tree := filepath.Join(ws, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	buildDeleteTree(t, tree, 200)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first entry is touched
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

// TestDeleteRecursiveStillCompletes: the interruptible walk must not change
// the success path — a full recursive delete leaves nothing behind.
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
