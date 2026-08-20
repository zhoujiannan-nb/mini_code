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

// TestWriteFileAppendCreates: append=true on a missing file must create it.
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

// TestWriteFileAppendAppends: append=true must add to the end, keeping the
// existing content byte-for-byte.
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

// TestWriteFileAppendMultiple: several appends must arrive in order.
func TestWriteFileAppendMultiple(t *testing.T) {
	dir := t.TempDir()
	runWrite(t, dir, "log.txt", "line1\n", map[string]interface{}{"append": true})
	runWrite(t, dir, "log.txt", "line2\n", map[string]interface{}{"append": true})
	runWrite(t, dir, "log.txt", "line3\n", map[string]interface{}{"append": true})
	if got := readFileRaw(t, filepath.Join(dir, "log.txt")); got != "line1\nline2\nline3\n" {
		t.Fatalf("file content = %q, want %q", got, "line1\nline2\nline3\n")
	}
}

// TestWriteFileOverwriteUnchanged: without append, a second write must still
// fully overwrite (regression guard for the default path).
func TestWriteFileOverwriteUnchanged(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "ow.txt")
	runWrite(t, dir, "ow.txt", "original content\n", nil)
	out := runWrite(t, dir, "ow.txt", "replaced\n", nil)
	if !strings.Contains(out, "wrote") {
		t.Fatalf("expected write confirmation, got: %s", out)
	}
	if got := readFileRaw(t, fp); got != "replaced\n" {
		t.Fatalf("file content = %q, want %q", got, "replaced\n")
	}
}

// TestWriteFileAppendCreatesParentDirs: append must create missing parent
// directories, like the overwrite path does.
func TestWriteFileAppendCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a", "b", "c.txt")
	runWrite(t, dir, filepath.Join("a", "b", "c.txt"), "chunk1\n", map[string]interface{}{"append": true})
	runWrite(t, dir, filepath.Join("a", "b", "c.txt"), "chunk2\n", map[string]interface{}{"append": true})
	if got := readFileRaw(t, fp); got != "chunk1\nchunk2\n" {
		t.Fatalf("file content = %q, want %q", got, "chunk1\nchunk2\n")
	}
}

// TestWriteFileAppendNoTrailingSplit: appending a chunk that does not end
// with a newline must not insert one (byte-exact concatenation).
func TestWriteFileAppendNoTrailingSplit(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "raw.txt")
	runWrite(t, dir, "raw.txt", "abc", map[string]interface{}{"append": true})
	runWrite(t, dir, "raw.txt", "def", map[string]interface{}{"append": true})
	if got := readFileRaw(t, fp); got != "abcdef" {
		t.Fatalf("file content = %q, want %q", got, "abcdef")
	}
}

// TestWriteFileOverwriteExistingReportsNote: overwriting an existing
// non-empty file must still succeed, and the result must flag that it
// replaced prior content (so a model that meant to append can notice).
func TestWriteFileOverwriteExistingReportsNote(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "ow2.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runWrite(t, dir, "ow2.txt", "fresh\n", nil)
	if !strings.Contains(out, "wrote") {
		t.Fatalf("expected write confirmation, got: %s", out)
	}
	if !strings.Contains(out, "OVERWROTE") {
		t.Fatalf("expected overwrite note, got: %s", out)
	}
	if !strings.Contains(out, "3 lines") {
		t.Fatalf("expected previous line count in note, got: %s", out)
	}
	// The write itself must have happened (content replaced).
	if got := readFileRaw(t, fp); got != "fresh\n" {
		t.Fatalf("file content = %q, want %q", got, "fresh\n")
	}
}

// TestWriteFileOverwriteNewFileNoNote: writing a brand-new file must NOT
// carry the overwrite note (there was nothing to clobber).
func TestWriteFileOverwriteNewFileNoNote(t *testing.T) {
	dir := t.TempDir()
	out := runWrite(t, dir, "brandnew.txt", "hello\n", nil)
	if strings.Contains(out, "OVERWROTE") {
		t.Fatalf("unexpected overwrite note for a new file: %s", out)
	}
}

// TestWriteFileOverwriteEmptyFileNoNote: overwriting an existing but EMPTY
// file must not carry the note (no content was lost).
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

// TestCountTextLines: line counting must match read_file semantics (trailing
// newline does not create a phantom line; a final line without a newline
// still counts).
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

// TestWriteFileAppendSchema: the tool schema must expose the append flag so
// the model knows the capability exists.
func TestWriteFileAppendSchema(t *testing.T) {
	wt := NewWriteFileTool(t.TempDir())
	props, ok := wt.Parameters()["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() has no properties object")
	}
	if _, ok := props["append"]; !ok {
		t.Fatal("Parameters() is missing the 'append' property")
	}
	if !strings.Contains(wt.Description(), "append") {
		t.Fatal("Description() does not mention append")
	}
}
