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

// TestFileHintReadNotFound: a mistyped file name must name the closest real
// files in the same directory.
func TestFileHintReadNotFound(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "data_v2.csv", "data.csv", "readme.md", "zzz_unrelated.txt")
	out := runRead(t, dir, "data_v1.csv", nil)
	if !strings.Contains(out, "file not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "data_v2.csv") {
		t.Fatalf("expected the closest name data_v2.csv in the hint, got: %s", out)
	}
	if !strings.Contains(out, "data.csv") {
		t.Fatalf("expected data.csv in the hint, got: %s", out)
	}
}

// TestFileHintReadExtensionTypo: a wrong extension is a common typo; the
// real name must be suggested (platform-independent, unlike a case-only
// difference which Windows resolves transparently).
func TestFileHintReadExtensionTypo(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "Report.docx", "notes.txt")
	out := runRead(t, dir, "Report.doc", nil)
	if !strings.Contains(out, "file not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "Report.docx") {
		t.Fatalf("expected Report.docx in the hint, got: %s", out)
	}
}

// TestFileHintReadNoSimilar: when nothing in the directory is similar, the
// error must stay a plain not-found (no noisy hint).
func TestFileHintReadNoSimilar(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "alpha.txt", "beta.txt")
	out := runRead(t, dir, "completely_different_name_zzz", nil)
	if !strings.Contains(out, "file not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if strings.Contains(out, "Hint: similar names") {
		t.Fatalf("did not expect a similar-names hint, got: %s", out)
	}
}

// TestFileHintReadMissingParent: when the parent directory itself does not
// exist, no directory hint can be computed.
func TestFileHintReadMissingParent(t *testing.T) {
	dir := t.TempDir()
	out := runRead(t, dir, "no_such_dir/x.txt", nil)
	if !strings.Contains(out, "file not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if strings.Contains(out, "Hint: similar names") {
		t.Fatalf("did not expect a similar-names hint, got: %s", out)
	}
}

// TestFileHintReadDirectory: reading a directory must say it is a directory
// and point at list_dir.
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
	if !strings.Contains(out, "not a file") {
		t.Fatalf("expected not-a-file error, got: %s", out)
	}
	if !strings.Contains(out, "list_dir") {
		t.Fatalf("expected a list_dir suggestion, got: %s", out)
	}
	if !strings.Contains(out, "2 entries") {
		t.Fatalf("expected the entry count, got: %s", out)
	}
}

// TestFileHintReadSuccessUnchanged: the success path must not carry any hint.
func TestFileHintReadSuccessUnchanged(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "ok.txt")
	out := runRead(t, dir, "ok.txt", nil)
	if strings.Contains(out, "Hint:") {
		t.Fatalf("success path must not carry hints, got: %s", out)
	}
	if !strings.Contains(out, "1| x") {
		t.Fatalf("expected file content, got: %s", out)
	}
}

// TestFileHintEditNotFound: edit_file on a mistyped path must also name the
// closest real files.
func TestFileHintEditNotFound(t *testing.T) {
	dir := t.TempDir()
	makeFiles(t, dir, "config.yaml", "config_old.yaml")
	out := runEdit(t, dir, "config.yml", "a", "b", false)
	if !strings.Contains(out, "file not found") {
		t.Fatalf("expected not-found error, got: %s", out)
	}
	if !strings.Contains(out, "config.yaml") {
		t.Fatalf("expected config.yaml in the hint, got: %s", out)
	}
}

// TestSimilarFileNamesCap: at most similarHintCap candidates are returned.
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
