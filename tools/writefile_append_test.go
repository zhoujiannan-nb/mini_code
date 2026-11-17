package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWrite(t *testing.T, ws, path, content string, extra map[string]interface{}) string {
	t.Helper()
	params := map[string]interface{}{"path": path, "content": content}
	for k, v := range extra {
		params[k] = v
	}
	tool := NewWriteFileTool(ws)
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return res.Text
}

func readFileRaw(t *testing.T, fp string) string {
	t.Helper()
	b, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("reading %s: %v", fp, err)
	}
	return string(b)
}

func TestWriteFileAppendCreates(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "new.txt")
	out := runWrite(t, dir, "new.txt", "first-chunk\n", map[string]interface{}{"append": true})
	if !strings.Contains(out, "appended") {
		t.Fatalf("expected append confirmation, got: %s", out)
	}
	if got := readFileRaw(t, fp); got != "first-chunk\n" {
		t.Fatalf("file content = %q, want %q", got, "first-chunk\n")
	}
}

func TestWriteFileAppendAppends(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(fp, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runWrite(t, dir, "data.txt", "gamma\n", map[string]interface{}{"append": true})
	if !strings.Contains(out, "appended") {
		t.Fatalf("expected append confirmation, got: %s", out)
	}
	if got := readFileRaw(t, fp); got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("file content = %q, want %q", got, "alpha\nbeta\ngamma\n")
	}
}

func TestWriteFileAppendMultiple(t *testing.T) {
	dir := t.TempDir()
	runWrite(t, dir, "log.txt", "line1\n", map[string]interface{}{"append": true})
	runWrite(t, dir, "log.txt", "line2\n", map[string]interface{}{"append": true})
	runWrite(t, dir, "log.txt", "line3\n", map[string]interface{}{"append": true})
	if got := readFileRaw(t, filepath.Join(dir, "log.txt")); got != "line1\nline2\nline3\n" {
		t.Fatalf("file content = %q, want %q", got, "line1\nline2\nline3\n")
	}
}

func TestWriteFileOverwriteUnchanged(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "ow.txt")
	runWrite(t, dir, "ow.txt", "original content\n", nil)
	out := runWrite(t, dir, "ow.txt", "replaced\n", nil)
	if !strings.Contains(out, "Updated file") {
		t.Fatalf("expected the update envelope on overwrite, got: %s", out)
	}
	if got := readFileRaw(t, fp); got != "replaced\n" {
		t.Fatalf("file content = %q, want %q", got, "replaced\n")
	}
}

func TestWriteFileAppendCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a", "b", "c.txt")
	runWrite(t, dir, filepath.Join("a", "b", "c.txt"), "chunk1\n", map[string]interface{}{"append": true})
	runWrite(t, dir, filepath.Join("a", "b", "c.txt"), "chunk2\n", map[string]interface{}{"append": true})
	if got := readFileRaw(t, fp); got != "chunk1\nchunk2\n" {
		t.Fatalf("file content = %q, want %q", got, "chunk1\nchunk2\n")
	}
}

func TestWriteFileAppendNoTrailingSplit(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "raw.txt")
	runWrite(t, dir, "raw.txt", "abc", map[string]interface{}{"append": true})
	runWrite(t, dir, "raw.txt", "def", map[string]interface{}{"append": true})
	if got := readFileRaw(t, fp); got != "abcdef" {
		t.Fatalf("file content = %q, want %q", got, "abcdef")
	}
}

func TestWriteFileOverwriteExistingReportsNote(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "ow2.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runWrite(t, dir, "ow2.txt", "fresh\n", nil)
	if !strings.Contains(out, "Updated file") {
		t.Fatalf("expected the update envelope, got: %s", out)
	}
	if got := readFileRaw(t, fp); got != "fresh\n" {
		t.Fatalf("file content = %q, want %q", got, "fresh\n")
	}
}

func TestWriteFileOverwriteNewFileNoNote(t *testing.T) {
	dir := t.TempDir()
	out := runWrite(t, dir, "brandnew.txt", "hello\n", nil)
	if strings.Contains(out, "OVERWROTE") {
		t.Fatalf("unexpected overwrite note for a new file: %s", out)
	}
}

func TestWriteFileOverwriteEmptyFileNoNote(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	out := runWrite(t, dir, "empty.txt", "now has content\n", nil)
	if strings.Contains(out, "OVERWROTE") {
		t.Fatalf("unexpected overwrite note for an empty file: %s", out)
	}
}

func TestCountTextLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n", 1},
		{"a\r\nb\r\n", 2},
	}
	for _, c := range cases {
		if got := countTextLines([]byte(c.in)); got != c.want {
			t.Fatalf("countTextLines(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestWriteFileAppendSchema(t *testing.T) {
	wt := NewWriteFileTool(t.TempDir())
	props, ok := wt.Parameters()["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() has no properties object")
	}
	if _, ok := props["append"]; ok {
		t.Fatal("Parameters() must match the dsh write schema (no 'append' property)")
	}
	for _, want := range []string{"file_path", "content"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("Parameters() is missing the %q property", want)
		}
	}
}
