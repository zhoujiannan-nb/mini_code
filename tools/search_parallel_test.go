package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSearchParallelIsDeterministic: the scan runs on a worker pool, so the
// only thing that keeps the model's view stable is that results are collected
// and sorted afterwards. Repeated runs over a wide tree must be byte-identical
// and in path order — a search whose output order depends on which worker
// finished first is a search the model cannot diff between calls.
func TestSearchParallelIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	// Enough files to spread over every worker several times, plus nesting so
	// the walk itself is non-trivial.
	const files = 120
	for i := 0; i < files; i++ {
		sub := fmt.Sprintf("pkg%02d", i%7)
		name := fmt.Sprintf("%s/file%03d.go", sub, i)
		writeFile(t, dir, name, fmt.Sprintf("package p\n\nfunc Target%03d() {}\n", i))
	}
	st := NewSearchTool(dir)

	var first string
	for run := 0; run < 3; run++ {
		res, err := st.Execute(context.Background(), map[string]interface{}{"pattern": "func Target", "max_results": 500})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if !strings.Contains(res.Text, fmt.Sprintf("%d matching lines total", files)) {
			t.Fatalf("run %d: expected %d matches: %s", run, files, res.Text)
		}
		if run == 0 {
			first = res.Text
			continue
		}
		if res.Text != first {
			t.Fatalf("run %d output differs from run 0:\n--- run 0 ---\n%s\n--- run %d ---\n%s", run, first, run, res.Text)
		}
	}

	// Path order: every result line's path must be >= the previous one's.
	var last string
	for _, line := range strings.Split(first, "\n") {
		if !strings.Contains(line, ":") || strings.HasPrefix(line, "(") {
			continue
		}
		path, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if last != "" && path < last {
			t.Fatalf("results out of order: %q after %q", path, last)
		}
		last = path
	}
}

// TestSearchParallelContextAndInclude: context mode re-reads only the files
// that matched (the scan itself no longer keeps every line of every file), so
// the surrounding lines have to still be exact.
func TestSearchParallelContextAndInclude(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "line one\nline two\nneedle here\nline four\n")
	writeFile(t, dir, "b.txt", "needle in a txt file\n")
	writeFile(t, dir, "c.go", "nothing\n")

	res, err := NewSearchTool(dir).Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"include": "*.go",
		"context": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text
	if !strings.Contains(text, "a.go:3: needle here") {
		t.Fatalf("missing match line: %s", text)
	}
	if !strings.Contains(text, "a.go-2: line two") || !strings.Contains(text, "a.go-4: line four") {
		t.Fatalf("context lines missing: %s", text)
	}
	if strings.Contains(text, "b.txt") {
		t.Fatalf("include filter ignored: %s", text)
	}
}

// TestSearchCancelledReturnsPromptly: the walk and the workers both observe the
// context, so cancelling a search over a large tree must not wait for it to
// finish scanning.
func TestSearchCancelledReturnsPromptly(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 200; i++ {
		writeFile(t, dir, fmt.Sprintf("d%02d/f.txt", i), "needle\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewSearchTool(dir).Execute(ctx, map[string]interface{}{"pattern": "needle"})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled search did not return promptly")
	}
}
