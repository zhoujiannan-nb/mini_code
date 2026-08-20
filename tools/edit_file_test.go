package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupEditTest creates a temp workspace with the given file content and
// returns the workspace dir and the relative file path.
func setupEditTest(t *testing.T, name, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, name)
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, name
}

func runEdit(t *testing.T, ws, path, oldText, newText string, replaceAll bool) string {
	t.Helper()
	tool := NewEditFileTool(ws)
	params := map[string]interface{}{
		"path":     path,
		"old_text": oldText,
		"new_text": newText,
	}
	if replaceAll {
		params["replace_all"] = true
	}
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return res.Text
}

func readBack(t *testing.T, ws, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditFileExactMatchUnchanged(t *testing.T) {
	file := "func main() {\n\tx := 1\n\t_ = x\n}\n"
	ws, name := setupEditTest(t, "a.go", file)

	out := runEdit(t, ws, name, "x := 1", "x := 2", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "func main() {\n\tx := 2\n\t_ = x\n}\n"
	if got != want {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditFileIndentationTolerant(t *testing.T) {
	// File uses 4-space indentation; old_text guesses 2 spaces.
	file := "def add(a, b):\n    return a + b\n"
	ws, name := setupEditTest(t, "a.py", file)

	out := runEdit(t, ws, name, "def add(a, b):\n  return a + b", "def add(a, b):\n    return a + b + 1", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected whitespace-tolerant success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "def add(a, b):\n    return a + b + 1\n"
	if got != want {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditFileTabsVsSpaces(t *testing.T) {
	file := "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n"
	ws, name := setupEditTest(t, "b.go", file)

	// old_text uses spaces where the file uses tabs.
	out := runEdit(t, ws, name, "func f() {\n    if x {\n        return 1\n    }\n}", "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected whitespace-tolerant success, got: %s", out)
	}
	got := readBack(t, ws, name)
	if !strings.Contains(got, "return 2") {
		t.Fatalf("expected 'return 2' in result, got: %q", got)
	}
}

func TestEditFileStrayBlankLines(t *testing.T) {
	// The file has a blank line inside the block that old_text omits.
	file := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	ws, name := setupEditTest(t, "c.go", file)

	oldText := "func main() {\n\tfmt.Println(\"hi\")\n}"
	newText := "func main() {\n\tfmt.Println(\"hello\")\n}"
	out := runEdit(t, ws, name, oldText, newText, false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected whitespace-tolerant success, got: %s", out)
	}
	got := readBack(t, ws, name)
	if !strings.Contains(got, `fmt.Println("hello")`) {
		t.Fatalf("expected hello in result, got: %q", got)
	}
	if !strings.Contains(got, "import \"fmt\"") {
		t.Fatalf("rest of file must be preserved, got: %q", got)
	}
}

func TestEditFileNoMatchStillErrors(t *testing.T) {
	file := "alpha\nbeta\ngamma\n"
	ws, name := setupEditTest(t, "d.txt", file)

	out := runEdit(t, ws, name, "delta", "x", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	got := readBack(t, ws, name)
	if got != file {
		t.Fatalf("file must be untouched, got: %q", got)
	}
}

func TestEditFileAmbiguousWithoutReplaceAll(t *testing.T) {
	file := "x := 1\ny := 2\nx := 1\n"
	ws, name := setupEditTest(t, "e.txt", file)

	out := runEdit(t, ws, name, "x := 1", "x := 9", false)
	if !strings.Contains(out, "appears 2 times") {
		t.Fatalf("expected ambiguity warning, got: %s", out)
	}
}

func TestEditFileAmbiguousReportsLineNumbers(t *testing.T) {
	// The ambiguity warning must name the lines of every occurrence so the
	// model can disambiguate without re-reading the file.
	file := "alpha\nx := 1\nbeta\nx := 1\ngamma\n"
	ws, name := setupEditTest(t, "e2.txt", file)

	out := runEdit(t, ws, name, "x := 1", "x := 9", false)
	if !strings.Contains(out, "appears 2 times") {
		t.Fatalf("expected ambiguity warning, got: %s", out)
	}
	if !strings.Contains(out, "at lines 2, 4") {
		t.Fatalf("expected 'at lines 2, 4' in warning, got: %s", out)
	}
	got := readBack(t, ws, name)
	if got != file {
		t.Fatalf("file must be untouched, got: %q", got)
	}
}

func TestEditFileAmbiguousMultiLineOldTextLines(t *testing.T) {
	// A multi-line old_text spanning occurrences: the reported line number
	// is the starting line of each occurrence.
	file := "a\none\ntwo\nb\none\ntwo\nc\n"
	ws, name := setupEditTest(t, "e3.txt", file)

	out := runEdit(t, ws, name, "one\ntwo", "ONE\nTWO", false)
	if !strings.Contains(out, "appears 2 times") {
		t.Fatalf("expected ambiguity warning, got: %s", out)
	}
	if !strings.Contains(out, "at lines 2, 5") {
		t.Fatalf("expected 'at lines 2, 5' in warning, got: %s", out)
	}
}

func TestEditFileWhitespaceTolerantReplaceAll(t *testing.T) {
	// Two occurrences with different indentation; replace_all must hit both.
	file := "a = 1\n  b = 2\n\nc = 3\n\tb = 2\n"
	ws, name := setupEditTest(t, "f.txt", file)

	out := runEdit(t, ws, name, "b = 2", "b = 99", true)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected replace_all success, got: %s", out)
	}
	got := readBack(t, ws, name)
	if strings.Contains(got, "b = 2") {
		t.Fatalf("expected both occurrences replaced, got: %q", got)
	}
	if strings.Count(got, "b = 99") != 2 {
		t.Fatalf("expected two 'b = 99', got: %q", got)
	}
	if !strings.Contains(got, "a = 1") || !strings.Contains(got, "c = 3") {
		t.Fatalf("unrelated lines must be preserved, got: %q", got)
	}
}

func TestEditFileFuzzyDoesNotMatchDifferentContent(t *testing.T) {
	// "return x" must NOT fuzzy-match "return x + 1" (line content differs).
	// oldText uses 2 spaces where the file uses a tab, so the exact path
	// misses and only the whitespace-tolerant path is exercised.
	file := "func f() int {\n\treturn x + 1\n}\n"
	ws, name := setupEditTest(t, "g.go", file)

	out := runEdit(t, ws, name, "  return x", "return y", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected no-match error, got: %s", out)
	}
}

func TestEditFileNoMatchClosestLineHint(t *testing.T) {
	// "betta" is a one-letter typo of the file's "beta" (line 2): the error
	// must point at that line so the model can fix old_text without re-reading.
	file := "alpha\nbeta\ngamma\n"
	ws, name := setupEditTest(t, "d2.txt", file)

	out := runEdit(t, ws, name, "betta", "x", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	if !strings.Contains(out, "line 2") {
		t.Fatalf("expected closest-line hint pointing at line 2, got: %s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Fatalf("expected the closest line text in the hint, got: %s", out)
	}
	got := readBack(t, ws, name)
	if got != file {
		t.Fatalf("file must be untouched, got: %q", got)
	}
}

func TestEditFileNoMatchNoHintWhenUnrelated(t *testing.T) {
	// No line in the file is close to old_text: the error must not carry a
	// misleading closest-line hint.
	file := "alpha\nbeta\ngamma\n"
	ws, name := setupEditTest(t, "d3.txt", file)

	out := runEdit(t, ws, name, "zzzqqqxxxwww", "x", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	if strings.Contains(out, "Closest line") {
		t.Fatalf("did not expect a closest-line hint, got: %s", out)
	}
}

func TestEditFileClosestHintPunctuationDiff(t *testing.T) {
	// Single-quote vs double-quote: near-identical line, hint must fire.
	file := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	ws, name := setupEditTest(t, "d4.go", file)

	out := runEdit(t, ws, name, `fmt.Println('hi')`, `fmt.Println("yo")`, false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	if !strings.Contains(out, "line 4") {
		t.Fatalf("expected closest-line hint pointing at line 4, got: %s", out)
	}
}

func TestEditFileClosestHintPicksLongestOldLine(t *testing.T) {
	// old_text has two lines; the longest one (the distinctive call) must
	// drive the hint, not the short generic "helper()" line.
	file := "helper()\ndoSomethingWithAVeryLongName(42)\n"
	ws, name := setupEditTest(t, "d5.go", file)

	out := runEdit(t, ws, name, "helper()\ndoSomethingWithAVeryLongName(41)", "x", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	if !strings.Contains(out, "line 2") {
		t.Fatalf("expected hint to point at line 2 (the long call), got: %s", out)
	}
}

func TestEditFilePreservesCRLF(t *testing.T) {
	// Editing a CRLF file must keep CRLF line endings byte-for-byte.
	file := "alpha\r\nbeta\r\ngamma\r\n"
	ws, name := setupEditTest(t, "h.txt", file)

	out := runEdit(t, ws, name, "beta", "BETA", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "alpha\r\nBETA\r\ngamma\r\n"
	if got != want {
		t.Fatalf("CRLF must be preserved:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditFilePreservesLF(t *testing.T) {
	// Editing an LF file must keep LF line endings.
	file := "alpha\nbeta\ngamma\n"
	ws, name := setupEditTest(t, "i.txt", file)

	out := runEdit(t, ws, name, "beta", "BETA", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "alpha\nBETA\ngamma\n"
	if got != want {
		t.Fatalf("LF must be preserved:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditFileCRLFWhitespaceTolerant(t *testing.T) {
	// The whitespace-tolerant path on a CRLF file must also keep CRLF.
	file := "def f():\r\n    return 1\r\n"
	ws, name := setupEditTest(t, "j.py", file)

	out := runEdit(t, ws, name, "def f():\n  return 1", "def f():\n    return 2", false)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected whitespace-tolerant success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "def f():\r\n    return 2\r\n"
	if got != want {
		t.Fatalf("CRLF must be preserved:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditFileCRLFReplaceAll(t *testing.T) {
	file := "x = 1\r\ny = 2\r\nx = 1\r\n"
	ws, name := setupEditTest(t, "k.txt", file)

	out := runEdit(t, ws, name, "x = 1", "x = 9", true)
	if !strings.Contains(out, "Successfully edited") {
		t.Fatalf("expected replace_all success, got: %s", out)
	}
	got := readBack(t, ws, name)
	want := "x = 9\r\ny = 2\r\nx = 9\r\n"
	if got != want {
		t.Fatalf("CRLF must be preserved with replace_all:\ngot:  %q\nwant: %q", got, want)
	}
}
