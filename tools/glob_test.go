package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		"node_modules/junk.go",
		".hidden.txt",
		".git/config",
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

func TestGlobSingleStarMatchesAnyDepth(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.go", nil)
	for _, want := range []string{"main.go", "test_main.go", "src/app.go", "src/util/helper.go", "src/util/deep/deeper.go", "node_modules/junk.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q (basename match at any depth), got: %s", want, out)
		}
	}
	if strings.Contains(out, ".git/config") {
		t.Fatalf("VCS metadata must be pruned, got: %s", out)
	}
	if strings.Contains(out, "(10 matches)") || strings.Contains(out, "matches)") {
		t.Fatalf("unexpected count trailer, got: %s", out)
	}
}

func TestGlobDoubleStarRecurses(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "**/*.go", nil)
	for _, want := range []string{"main.go", "src/app.go", "src/util/helper.go", "src/util/deep/deeper.go", "test_main.go", "node_modules/junk.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in result, got: %s", want, out)
		}
	}
}

func TestGlobIncludesIgnoredFilesPrunesVCSOnly(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*", nil)
	if !strings.Contains(out, "node_modules/junk.go") {
		t.Fatalf("node_modules must be included, got: %s", out)
	}
	if !strings.Contains(out, ".hidden.txt") {
		t.Fatalf("hidden files must be included, got: %s", out)
	}
	if strings.Contains(out, ".git/config") {
		t.Fatalf("only VCS metadata is pruned, got: %s", out)
	}
}

func TestGlobScopedSubdir(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.go", map[string]interface{}{"path": "src"})
	for _, want := range []string{"src/app.go", "src/util/helper.go", "src/util/deep/deeper.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q, got: %s", want, out)
		}
	}
	if strings.Contains(out, "main.go") || strings.Contains(out, "node_modules") {
		t.Fatalf("must not match outside src, got: %s", out)
	}
}

func TestGlobQuestionMarkAndClass(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "test_?ain.go", nil)
	if !strings.Contains(out, "test_main.go") {
		t.Fatalf("expected test_main.go via ?, got: %s", out)
	}
	out2 := runGlob(t, dir, "[mn]otes.md", nil)
	if !strings.Contains(out2, "notes.md") {
		t.Fatalf("expected notes.md via [...], got: %s", out2)
	}
}

func TestGlobFilesOnly(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "src/*", nil)
	if !strings.Contains(out, "src/app.go") {
		t.Fatalf("expected src/app.go, got: %s", out)
	}
	if strings.Contains(out, "src/util") {
		t.Fatalf("directories must never be returned, got: %s", out)
	}
}

func TestGlobDoubleStarDeepAbsorption(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "src/**/*.go", nil)
	for _, want := range []string{"src/app.go", "src/util/helper.go", "src/util/deep/deeper.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q, got: %s", want, out)
		}
	}
}

func TestGlobNoMatch(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.rs", nil)
	if out != "No files found" {
		t.Fatalf("expected no-match message, got: %s", out)
	}
}

func TestGlobBadPath(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "*.go", map[string]interface{}{"path": "does-not-exist"})
	if !strings.Contains(out, "Error") {
		t.Fatalf("expected error for bad path, got: %s", out)
	}
}

func TestGlobEmptyPatternRejected(t *testing.T) {
	dir := buildGlobTree(t)
	out := runGlob(t, dir, "   ", nil)
	if !strings.Contains(out, "pattern must be a non-empty string") {
		t.Fatalf("expected dsh schema text, got: %s", out)
	}
}

func TestGlobTruncatesAt100WithSpillNotice(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 105; i++ {
		fp := filepath.Join(dir, "f"+runeDigit(i)+".txt")
		if err := os.WriteFile(fp, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out := runGlob(t, dir, "*.txt", nil)
	if strings.Count(out, "\n") != 100 {
		t.Fatalf("expected 100 kept paths, got %d lines:\n%s", strings.Count(out, "\n")+1, out)
	}
	if !strings.Contains(out, "(Full sorted result stored at: ") ||
		!strings.Contains(out, ". Read it or narrow pattern or path to see more.)") {
		t.Fatalf("expected the spill notice, got:\n%s", out[len(out)-200:])
	}
}

func runeDigit(i int) string {
	return strings.Replace(strings.Replace(string(rune('a'+i/100)), "", "", -1)+
		string(rune('a'+(i/10)%10))+string(rune('a'+i%10)), "\x00", "", -1)
}
