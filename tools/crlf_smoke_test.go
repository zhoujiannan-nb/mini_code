package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCRLFSmokeReadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "crlf.txt")
	// CRLF file: 3 lines
	if err := os.WriteFile(p, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tl := NewReadFileTool(dir)
	res, err := tl.Execute(context.Background(), map[string]interface{}{"path": "crlf.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "\r") {
		t.Fatalf("read_file output still contains CR: %q", res.Text)
	}
	if !strings.Contains(res.Text, "1| alpha") || !strings.Contains(res.Text, "3| gamma") {
		t.Fatalf("unexpected read_file output: %q", res.Text)
	}
	if !strings.Contains(res.Text, "3 lines total") {
		t.Fatalf("expected 3 lines total, got: %q", res.Text)
	}

	// search_files must also be CR-free
	st := NewSearchTool(dir)
	res2, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res2.Text, "\r") {
		t.Fatalf("search_files output still contains CR: %q", res2.Text)
	}
	if !strings.Contains(res2.Text, "crlf.txt:2: beta") {
		t.Fatalf("unexpected search_files output: %q", res2.Text)
	}

	// edit_file on the CRLF file: old_text copied from the (now clean) read
	// output must match, and the file must keep CRLF endings.
	et := NewEditFileTool(dir)
	res3, err := et.Execute(context.Background(), map[string]interface{}{
		"path": "crlf.txt", "old_text": "beta", "new_text": "BETA",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res3.Text, "Successfully edited") {
		t.Fatalf("edit failed: %q", res3.Text)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "alpha\r\nBETA\r\ngamma\r\n") {
		t.Fatalf("CRLF endings not preserved: %q", string(raw))
	}
}
