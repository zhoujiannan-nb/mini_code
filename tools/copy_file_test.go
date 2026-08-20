package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCopy invokes the copy_file tool with workspace-relative paths and
// returns the result text.
func runCopy(t *testing.T, ws, src, dst string, recursive bool) string {
	t.Helper()
	tool := NewCopyFileTool(ws)
	params := map[string]interface{}{
		"source":      src,
		"destination": dst,
	}
	if recursive {
		params["recursive"] = true
	}
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return res.Text
}

func TestCopyFileBasic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a.txt", "b.txt", false)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success, got: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatalf("copy missing: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("content mismatch: %q", got)
	}
	// The source must remain in place.
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("source must remain, got: %v", err)
	}
}

func TestCopyFileCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a.txt", "deep/nested/dir/b.txt", false)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "deep", "nested", "dir", "b.txt")); err != nil {
		t.Fatalf("copy missing: %v", err)
	}
}

func TestCopyFileIntoExistingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Destination is an existing directory: copy into it, keeping base name.
	out := runCopy(t, dir, "a.txt", "sub", false)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "a.txt")); err != nil {
		t.Fatalf("expected copy at sub/a.txt: %v", err)
	}
	// Trailing separator means the same.
	out = runCopy(t, dir, "a.txt", "sub2/", false)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success with trailing separator, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub2", "a.txt")); err != nil {
		t.Fatalf("expected copy at sub2/a.txt: %v", err)
	}
}

func TestCopyFileDestExistsErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a.txt", "b.txt", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected overwrite refusal, got: %s", out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(got) != "y" {
		t.Fatalf("existing destination must be untouched, got: %q", got)
	}
}

func TestCopyFileSourceMissing(t *testing.T) {
	dir := t.TempDir()
	out := runCopy(t, dir, "nope.txt", "b.txt", false)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected error, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err == nil {
		t.Fatalf("no destination must be created on missing source")
	}
}

func TestCopyFileSamePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a.txt", "a.txt", false)
	if strings.Contains(out, "Successfully copied") {
		t.Fatalf("same-path copy must not succeed, got: %s", out)
	}
}

func TestCopyDirNonEmptyWithoutRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "inner"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "inner", "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a", "b", false)
	if !strings.Contains(out, "recursive=true") {
		t.Fatalf("expected non-empty-dir error asking for recursive, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "b")); err == nil {
		t.Fatalf("no destination must be created without recursive")
	}
}

func TestCopyDirRecursive(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, content string) {
		t.Helper()
		fp := filepath.Join(dir, "a", rel)
		if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mk("f1.txt", "one")
	mk("sub/f2.txt", "two")
	mk("sub/deep/f3.txt", "three")

	out := runCopy(t, dir, "a", "b", true)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success, got: %s", out)
	}
	if !strings.Contains(out, "3 files") {
		t.Fatalf("expected file count in result, got: %s", out)
	}
	for _, rel := range []string{"f1.txt", filepath.Join("sub", "f2.txt"), filepath.Join("sub", "deep", "f3.txt")} {
		if _, err := os.Stat(filepath.Join(dir, "b", rel)); err != nil {
			t.Fatalf("missing copied file %s: %v", rel, err)
		}
	}
	// Source tree must remain intact.
	if _, err := os.Stat(filepath.Join(dir, "a", "sub", "deep", "f3.txt")); err != nil {
		t.Fatalf("source tree must remain, got: %v", err)
	}
}

func TestCopyDirEmptyWithoutRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	out := runCopy(t, dir, "a", "b", false)
	if !strings.Contains(out, "Successfully copied") {
		t.Fatalf("expected success for empty dir, got: %s", out)
	}
	fi, err := os.Stat(filepath.Join(dir, "b"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("expected destination directory: %v", err)
	}
}

func TestCopyDirIntoItselfRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Copying a directory into one of its own subdirectories must be
	// rejected up front (it would otherwise recurse forever).
	out := runCopy(t, dir, "a", "a/sub2", true)
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected self-copy rejection, got: %s", out)
	}
}
