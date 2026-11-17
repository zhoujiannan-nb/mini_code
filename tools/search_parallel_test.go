package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSearchParallelIsDeterministic(t *testing.T) {
	dir := t.TempDir()
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
		if !strings.Contains(res.Text, fmt.Sprintf("Found %d matches", files)) {
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

	var last string
	for _, line := range strings.Split(first, "\n") {
		if line == "" || strings.HasPrefix(line, "Line ") || strings.HasPrefix(line, "Found ") {
			continue
		}
		if last != "" && line < last {
			t.Fatalf("results out of order: %q after %q", line, last)
		}
		last = line
	}
}

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
