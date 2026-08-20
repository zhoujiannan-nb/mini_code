package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSearchToolPlainText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world\nfoo bar\nhello again\n")
	writeFile(t, dir, "sub/b.txt", "nothing here\nHELLO world\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "a.txt:1: hello world") {
		t.Fatalf("missing a.txt:1 match: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.txt:3: hello again") {
		t.Fatalf("missing a.txt:3 match: %s", res.Text)
	}
	if strings.Contains(res.Text, "sub/b.txt") {
		t.Fatalf("case-sensitive search should not match HELLO: %s", res.Text)
	}
	if !strings.Contains(res.Text, "2 matching lines total") {
		t.Fatalf("expected total count 2: %s", res.Text)
	}
}

func TestSearchToolIgnoreCase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "Hello World\nhello again\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "hello", "ignore_case": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "a.txt:1") || !strings.Contains(res.Text, "a.txt:2") {
		t.Fatalf("expected both lines: %s", res.Text)
	}
	if !strings.Contains(res.Text, "2 matching lines total") {
		t.Fatalf("expected total 2: %s", res.Text)
	}
}

func TestSearchToolRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "log.txt", "2026-01-01 error boom\n2026-01-02 info ok\n2026-01-03 error again\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": `error \w+`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "log.txt:1") || !strings.Contains(res.Text, "log.txt:3") {
		t.Fatalf("regex should match lines 1 and 3: %s", res.Text)
	}
	if strings.Contains(res.Text, "log.txt:2") {
		t.Fatalf("line 2 should not match: %s", res.Text)
	}
}

func TestSearchToolIncludeGlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func Target() {}\n")
	writeFile(t, dir, "a.txt", "func Target() {}\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "Target", "include": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "a.go:1") {
		t.Fatalf("expected a.go match: %s", res.Text)
	}
	if strings.Contains(res.Text, "a.txt") {
		t.Fatalf("a.txt should be excluded by include glob: %s", res.Text)
	}
}

func TestSearchToolMaxResults(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("needle line\n")
	}
	writeFile(t, dir, "big.txt", b.String())

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "needle", "max_results": 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "50 matching lines total") {
		t.Fatalf("total should be 50: %s", res.Text)
	}
	if !strings.Contains(res.Text, "showing first 10") {
		t.Fatalf("should note truncation: %s", res.Text)
	}
	if c := strings.Count(res.Text, "needle line"); c != 10 {
		t.Fatalf("expected 10 shown lines, got %d", c)
	}
}

func TestSearchToolNoMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "abc\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "zzz-not-there"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "No matches") {
		t.Fatalf("expected no-match message: %s", res.Text)
	}
}

func TestSearchToolSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "one.txt", "alpha\nbeta\nalpha\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "alpha", "path": p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "one.txt:1") || !strings.Contains(res.Text, "one.txt:3") {
		t.Fatalf("expected lines 1 and 3: %s", res.Text)
	}
}

func TestSearchToolBadPath(t *testing.T) {
	dir := t.TempDir()
	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "x", "path": "no/such/dir"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Error") {
		t.Fatalf("expected error for bad path: %s", res.Text)
	}
}

func TestSearchToolContextBasic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "line1\nline2\nTARGET\nline4\nline5\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "TARGET", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	// Match line uses "path:line:", context lines use "path-line:".
	if !strings.Contains(res.Text, "a.txt:3: TARGET") {
		t.Fatalf("missing match line: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.txt-2: line2") {
		t.Fatalf("missing context line before: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.txt-4: line4") {
		t.Fatalf("missing context line after: %s", res.Text)
	}
	if !strings.Contains(res.Text, "1 matching lines total") {
		t.Fatalf("expected total 1: %s", res.Text)
	}
}

func TestSearchToolContextMerge(t *testing.T) {
	dir := t.TempDir()
	// Two matches 2 lines apart with context=2 should merge into ONE group
	// (no "--" separator between them).
	writeFile(t, dir, "a.txt", "a1\nT1\nx\nT2\na5\na6\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "T", "context": 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "--") {
		t.Fatalf("close matches should merge into one group, got separator: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.txt:2: T1") || !strings.Contains(res.Text, "a.txt:4: T2") {
		t.Fatalf("missing both matches: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.txt-1: a1") || !strings.Contains(res.Text, "a.txt-6: a6") {
		t.Fatalf("missing outer context: %s", res.Text)
	}
}

func TestSearchToolContextSeparate(t *testing.T) {
	dir := t.TempDir()
	// Two matches far apart with context=1 should produce two groups with "--".
	writeFile(t, dir, "a.txt", "T1\nb2\nb3\nb4\nb5\nT6\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "T", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "--") {
		t.Fatalf("distant matches should be separated by --: %s", res.Text)
	}
	if c := strings.Count(res.Text, "\n--\n"); c != 1 {
		t.Fatalf("expected exactly one separator, got %d: %s", c, res.Text)
	}
}

func TestSearchToolContextBoundary(t *testing.T) {
	dir := t.TempDir()
	// Match on the very first line: no context before it.
	writeFile(t, dir, "a.txt", "TARGET\nline2\nline3\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "TARGET", "context": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "a.txt:1: TARGET") {
		t.Fatalf("missing match: %s", res.Text)
	}
	// Context after should show lines 2 and 3, but nothing before line 1.
	if !strings.Contains(res.Text, "a.txt-2: line2") || !strings.Contains(res.Text, "a.txt-3: line3") {
		t.Fatalf("missing trailing context: %s", res.Text)
	}
	if strings.Contains(res.Text, "a.txt-0") {
		t.Fatalf("should not show a line 0: %s", res.Text)
	}
}

func TestSearchToolContextDefaultZero(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "line1\nTARGET\nline3\n")

	st := NewSearchTool(dir)
	res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "TARGET"})
	if err != nil {
		t.Fatal(err)
	}
	// Default (no context param) must behave as before: only the match line.
	if !strings.Contains(res.Text, "a.txt:2: TARGET") {
		t.Fatalf("missing match: %s", res.Text)
	}
	if strings.Contains(res.Text, "line1") || strings.Contains(res.Text, "line3") {
		t.Fatalf("default should not show context lines: %s", res.Text)
	}
	if strings.Contains(res.Text, "--") {
		t.Fatalf("default should not show separators: %s", res.Text)
	}
}
