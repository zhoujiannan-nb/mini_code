package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildGlobTree creates a small nested tree in a temp dir and returns the dir.
func buildGlobTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := []string{
		"main.go",
		"notes.md",
		"src/app.go",
		"src/util/helper.go",
		"src/util/deep/deeper.go",
		"docs/readme.md",
		"docs/api/v1.md",
		"test_main.go",
		"node_modules/junk.go", // must be ignored
	}
	for _, f := range files {
		fp := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runGlob(t *testing.T, ws, pattern string, extra map[string]interface{}) string {
	t.Helper()
	params := map[string]interface{}{"pattern": pattern}
	for k, v := range extra {
		params[k] = v
	}
	tool := NewGlobTool(ws)
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return res.Text
}

func TestGlobSingleStarNoRecursion(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.go", nil)
	// Only top-level .go files: main.go and test_main.go.
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "test_main.go") {
		t.Fatalf("expected top-level go files, got: %s", out)
	}
	if strings.Contains(out, "src/") {
		t.Fatalf("single * must not recurse, got: %s", out)
	}
	if !strings.Contains(out, "(2 matches)") {
		t.Fatalf("expected 2 matches, got: %s", out)
	}
}

func TestGlobDoubleStarRecurses(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "**/*.go", nil)
	for _, want := range []string{"main.go", "src/app.go", "src/util/helper.go", "src/util/deep/deeper.go", "test_main.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in result, got: %s", want, out)
		}
	}
	if !strings.Contains(out, "(5 matches)") {
		t.Fatalf("expected 5 matches, got: %s", out)
	}
}

func TestGlobIgnoresNoiseDirs(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "**/*.go", nil)
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules must be skipped, got: %s", out)
	}
}

func TestGlobScopedSubdir(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "src/**/*.go", nil)
	for _, want := range []string{"src/app.go", "src/util/helper.go", "src/util/deep/deeper.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q, got: %s", want, out)
		}
	}
	if strings.Contains(out, "main.go") {
		t.Fatalf("must not match outside src, got: %s", out)
	}
	if !strings.Contains(out, "(3 matches)") {
		t.Fatalf("expected 3 matches, got: %s", out)
	}
}

func TestGlobQuestionMarkAndClass(t *testing.T) {
	dir := buildGlobTree(t)
	// ? matches exactly one char: "test_main.go" -> test_?ain.go
	out := runGlob(t, dir, "test_?ain.go", nil)
	if !strings.Contains(out, "test_main.go") || !strings.Contains(out, "(1 matches)") {
		t.Fatalf("expected test_main.go via ?, got: %s", out)
	}
	// Character class: [mn]otes.md -> notes.md
	out2 := runGlob(t, dir, "[mn]otes.md", nil)
	if !strings.Contains(out2, "notes.md") || !strings.Contains(out2, "(1 matches)") {
		t.Fatalf("expected notes.md via [...], got: %s", out2)
	}
}

func TestGlobDirectoriesMarked(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "src/*", nil)
	// src contains app.go (file) and util/ (dir) at this level.
	if !strings.Contains(out, "src/app.go") {
		t.Fatalf("expected src/app.go, got: %s", out)
	}
	if !strings.Contains(out, "src/util/") {
		t.Fatalf("expected directory marker src/util/, got: %s", out)
	}
}

func TestGlobNoMatch(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.rs", nil)
	if !strings.Contains(out, "No files match") {
		t.Fatalf("expected no-match message, got: %s", out)
	}
}

func TestGlobMaxResults(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "**/*.go", map[string]interface{}{"max_results": float64(2)})
	if !strings.Contains(out, "truncated, showing first 2 of 5") {
		t.Fatalf("expected truncation notice, got: %s", out)
	}
}

func TestGlobBadPath(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.go", map[string]interface{}{"path": "does-not-exist"})
	if !strings.Contains(out, "Error") {
		t.Fatalf("expected error for bad path, got: %s", out)
	}
}
