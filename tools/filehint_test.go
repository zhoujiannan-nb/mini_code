package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileHintReadNotFound(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "data_v2.csv", "data.csv", "readme.md", "zzz_unrelated.txt")
	out := runRead(t, dir, "data_v1.csv", nil)
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "data_v2.csv") {
		t.Fatalf("expected the closest name data_v2.csv in the hint, got: %s", out)
	}
	if !strings.Contains(out, "data.csv") {
		t.Fatalf("expected data.csv in the hint, got: %s", out)
	}
}

func TestFileHintReadExtensionTypo(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "Report.docx", "notes.txt")
	out := runRead(t, dir, "Report.doc", nil)
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "Report.docx") {
		t.Fatalf("expected Report.docx in the hint, got: %s", out)
	}
}

func TestFileHintReadNoSimilar(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "alpha.txt", "beta.txt")
	out := runRead(t, dir, "completely_different_name_zzz", nil)
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if strings.Contains(out, "Hint: similar names") {
		t.Fatalf("did not expect a similar-names hint, got: %s", out)
	}
}

func TestFileHintReadMissingParent(t *testing.T) {
	dir := t.TempDir()
	out := runRead(t, dir, "no_such_dir/x.txt", nil)
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if strings.Contains(out, "Hint: similar names") {
		t.Fatalf("did not expect a similar-names hint, got: %s", out)
	}
}

func TestFileHintReadDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "sub", nil)
	if !strings.Contains(out, "not a regular file") {
		t.Fatalf("expected not-a-file error, got: %s", out)
	}
	if !strings.Contains(out, "use glob") {
		t.Fatalf("expected a glob suggestion, got: %s", out)
	}
	if !strings.Contains(out, "2 entries") {
		t.Fatalf("expected the entry count, got: %s", out)
	}
}

func TestFileHintReadSuccessUnchanged(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "ok.txt")
	out := runRead(t, dir, "ok.txt", nil)
	if strings.Contains(out, "Hint:") {
		t.Fatalf("success path must not carry hints, got: %s", out)
	}
	if !strings.Contains(out, "1: x") {
		t.Fatalf("expected file content, got: %s", out)
	}
}

func TestFileHintEditNotFound(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "config.yaml", "config_old.yaml")
	out := runEdit(t, dir, "config.yml", "a", "b", false)
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "config.yaml") {
		t.Fatalf("expected config.yaml in the hint, got: %s", out)
	}
}

func TestSimilarFileNamesCap(t *testing.T) {
	dir := t.TempDir()
	names := []string{"table_1.csv", "table_2.csv", "table_3.csv", "table_4.csv", "table_5.csv", "table_6.csv", "other.log"}
	makeFiles(t, dir, names...)
	got := similarFileNames(dir, "table_7.csv")
	if len(got) > similarHintCap {
		t.Fatalf("expected at most %d candidates, got %d: %v", similarHintCap, len(got), got)
	}
	if len(got) == 0 {
		t.Fatal("expected candidates, got none")
	}
}
