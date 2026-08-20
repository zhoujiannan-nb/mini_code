package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{3584, "3.5 KB"},
		{1048576, "1.0 MB"},
		{5347737, "5.1 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestListDirShowsFileSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 3*1024+512) // 3.5 KB
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := NewListDirTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": "."})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	out := res.Text
	if !strings.Contains(out, "small.txt (5 B)") {
		t.Errorf("expected 'small.txt (5 B)' in listing, got:\n%s", out)
	}
	if !strings.Contains(out, "big.bin (3.5 KB)") {
		t.Errorf("expected 'big.bin (3.5 KB)' in listing, got:\n%s", out)
	}
	if !strings.Contains(out, "sub/") {
		t.Errorf("expected directory entry 'sub/', got:\n%s", out)
	}
}

func TestListDirRecursiveShowsFileSizes(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deep.log"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewListDirTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": ".", "recursive": true})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	out := res.Text
	// Path separators are platform-dependent (rel on Windows uses '\');
	// only assert the leaf entry carries its size.
	if !strings.Contains(out, "deep.log (10 B)") {
		t.Errorf("expected 'deep.log (10 B)' in recursive listing, got:\n%s", out)
	}
}
