package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeLargeFile writes a file with nLines lines (each "line-%06d" plus a
// padding run to push the total size above largeFileThreshold) and returns
// the workspace dir and file name.
func makeLargeFile(t *testing.T, nLines int, crlf bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, "big.log")
	f, err := os.Create(fp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	eol := "\n"
	if crlf {
		eol = "\r\n"
	}
	pad := strings.Repeat("x", 60) // ~70 bytes/line -> 20000 lines ≈ 1.4MB
	for i := 1; i <= nLines; i++ {
		fmt.Fprintf(f, "line-%06d %s%s", i, pad, eol)
	}
	fi, _ := f.Stat()
	if fi.Size() <= largeFileThreshold {
		t.Fatalf("test file too small to exercise the large path: %d bytes", fi.Size())
	}
	return dir, "big.log"
}

func TestReadFileTailSmallFile(t *testing.T) {
	dir, name := writeTempFile(t, "t1.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n")
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(3)})
	if !strings.Contains(out, "8| l8") || !strings.Contains(out, "10| l10") {
		t.Fatalf("expected lines 8-10, got: %s", out)
	}
	if strings.Contains(out, "7| l7") {
		t.Fatalf("line 7 must not be in a tail=3 window: %s", out)
	}
	if !strings.Contains(out, "last 3 lines") {
		t.Fatalf("expected 'last 3 lines' footer, got: %s", out)
	}
}

func TestReadFileTailBiggerThanFile(t *testing.T) {
	dir, name := writeTempFile(t, "t2.txt", "a\nb\nc\n")
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(100)})
	if !strings.Contains(out, "1| a") || !strings.Contains(out, "3| c") {
		t.Fatalf("expected the whole file, got: %s", out)
	}
	if !strings.Contains(out, "3 lines total") {
		t.Fatalf("expected end-of-file footer, got: %s", out)
	}
}

func TestReadFileLargeHeadStopsEarly(t *testing.T) {
	dir, name := makeLargeFile(t, 20000, false)
	out := runRead(t, dir, name, nil) // default offset=1 limit=2000
	if !strings.Contains(out, "1| line-000001") {
		t.Fatalf("expected line 1, got: %s", out[:200])
	}
	if !strings.Contains(out, "2000| line-002000") {
		t.Fatalf("expected line 2000 in window, got: %s", out[:400])
	}
	if strings.Contains(out, "2001| line-002001") {
		t.Fatalf("line 2001 must not be in a limit=2000 window")
	}
	if !strings.Contains(out, "total line count unknown") {
		t.Fatalf("expected early-stop footer, got: %s", out[len(out)-300:])
	}
	if !strings.Contains(out, "offset=2001") {
		t.Fatalf("expected continuation hint offset=2001, got: %s", out[len(out)-300:])
	}
}

func TestReadFileLargeOffsetWindow(t *testing.T) {
	dir, name := makeLargeFile(t, 20000, false)
	out := runRead(t, dir, name, map[string]interface{}{"offset": float64(5000), "limit": float64(5)})
	if !strings.Contains(out, "5000| line-005000") || !strings.Contains(out, "5004| line-005004") {
		t.Fatalf("expected lines 5000-5004, got: %s", out)
	}
	if strings.Contains(out, "5005| line-005005") {
		t.Fatalf("line 5005 must not be in a 5-line window")
	}
}

func TestReadFileLargeTail(t *testing.T) {
	dir, name := makeLargeFile(t, 20000, false)
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(5)})
	if !strings.Contains(out, "19996| line-019996") || !strings.Contains(out, "20000| line-020000") {
		t.Fatalf("expected last 5 lines (19996-20000), got: %s", out)
	}
	if !strings.Contains(out, "of 20000") {
		t.Fatalf("expected known total in tail footer, got: %s", out[len(out)-200:])
	}
	if !strings.Contains(out, "last 5 lines") {
		t.Fatalf("expected 'last 5 lines' footer, got: %s", out[len(out)-200:])
	}
}

func TestReadFileLargeOffsetBeyondEnd(t *testing.T) {
	dir, name := makeLargeFile(t, 20000, false)
	out := runRead(t, dir, name, map[string]interface{}{"offset": float64(999999)})
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected offset error, got: %s", out)
	}
	if !strings.Contains(out, "20000 lines") {
		t.Fatalf("expected error to mention the true total (20000), got: %s", out)
	}
}

func TestReadFileLargeCRLF(t *testing.T) {
	dir, name := makeLargeFile(t, 20000, true)
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(3)})
	if strings.Contains(out, "\r") {
		t.Fatalf("CRLF must be normalized in the large path: %q", out)
	}
	if !strings.Contains(out, "20000| line-020000") {
		t.Fatalf("expected last line with correct number, got: %s", out)
	}
}

func TestReadFileLargeNoTrailingNewline(t *testing.T) {
	// File just above the threshold whose last line has no trailing newline:
	// the line count must not gain a phantom empty line.
	dir := t.TempDir()
	fp := filepath.Join(dir, "nl.log")
	var b strings.Builder
	for i := 1; i <= 15000; i++ {
		b.WriteString(fmt.Sprintf("line-%06d %s\n", i, strings.Repeat("y", 70)))
	}
	b.WriteString("final-line") // no trailing newline
	if err := os.WriteFile(fp, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "nl.log", map[string]interface{}{"tail": float64(2)})
	if !strings.Contains(out, "15001| final-line") {
		t.Fatalf("expected the unterminated final line as line 15001, got: %s", out)
	}
	if !strings.Contains(out, "of 15001") {
		t.Fatalf("expected 15001 total lines in footer, got: %s", out[len(out)-200:])
	}
}

func TestReadFileLargeBinaryGuard(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bin.dat")
	buf := make([]byte, 2<<20) // 2MB, above threshold
	buf[100] = 0               // NUL in the first 8KB
	if err := os.WriteFile(fp, buf, 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "bin.dat", nil)
	if !strings.Contains(out, "binary file") {
		t.Fatalf("expected binary-file error, got: %s", out)
	}
}
