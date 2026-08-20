package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRead(t *testing.T, ws, path string, extra map[string]interface{}) string {
	t.Helper()
	params := map[string]interface{}{"path": path}
	for k, v := range extra {
		params[k] = v
	}
	tool := NewReadFileTool(ws)
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return res.Text
}

func writeTempFile(t *testing.T, name, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, name)
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, name
}

func TestReadFileTrailingNewlineLineCount(t *testing.T) {
	// "alpha\nbeta\n" is 2 lines; the trailing newline must not create a
	// phantom empty line 3.
	dir, name := writeTempFile(t, "a.txt", "alpha\nbeta\n")
	out := runRead(t, dir, name, nil)
	if !strings.Contains(out, "2 lines total") {
		t.Fatalf("expected '2 lines total', got: %s", out)
	}
	if strings.Contains(out, "3| ") {
		t.Fatalf("phantom line 3 present: %s", out)
	}
}

func TestReadFileNoTrailingNewlineLineCount(t *testing.T) {
	dir, name := writeTempFile(t, "b.txt", "alpha\nbeta")
	out := runRead(t, dir, name, nil)
	if !strings.Contains(out, "2 lines total") {
		t.Fatalf("expected '2 lines total', got: %s", out)
	}
}

func TestReadFileBlankLastLine(t *testing.T) {
	// "a\n\n" is 2 lines: "a" and a genuinely empty second line.
	dir, name := writeTempFile(t, "c.txt", "a\n\n")
	out := runRead(t, dir, name, nil)
	if !strings.Contains(out, "2 lines total") {
		t.Fatalf("expected '2 lines total', got: %s", out)
	}
}

func TestReadFileSingleNewline(t *testing.T) {
	// "\n" is exactly one (empty) line.
	dir, name := writeTempFile(t, "d.txt", "\n")
	out := runRead(t, dir, name, nil)
	if !strings.Contains(out, "1 lines total") {
		t.Fatalf("expected '1 lines total', got: %s", out)
	}
}

func TestReadFileOffsetBeyondEndUsesFixedCount(t *testing.T) {
	dir, name := writeTempFile(t, "e.txt", "a\nb\n")
	out := runRead(t, dir, name, map[string]interface{}{"offset": float64(3)})
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected offset error, got: %s", out)
	}
	if !strings.Contains(out, "2 lines") {
		t.Fatalf("expected error to mention 2 lines, got: %s", out)
	}
}

func TestReadFilePaginationContinuesAtRightLine(t *testing.T) {
	// 5 lines with trailing newline; limit=2 must continue at offset=3.
	dir, name := writeTempFile(t, "f.txt", "l1\nl2\nl3\nl4\nl5\n")
	out := runRead(t, dir, name, map[string]interface{}{"limit": float64(2)})
	if !strings.Contains(out, "of 5") {
		t.Fatalf("expected total of 5 lines in pagination footer, got: %s", out)
	}
	if !strings.Contains(out, "offset=3") {
		t.Fatalf("expected continuation hint offset=3, got: %s", out)
	}
	out2 := runRead(t, dir, name, map[string]interface{}{"offset": float64(3), "limit": float64(2)})
	if !strings.Contains(out2, "3| l3") || !strings.Contains(out2, "4| l4") {
		t.Fatalf("expected lines 3-4 on continuation, got: %s", out2)
	}
}
