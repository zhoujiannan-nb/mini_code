package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeLargeFile writes a file with nLines lines (each "line-%06d" plus a
// padding run to push the total size above largeFileThreshold, which is
// dsh's readStreamMinSize: 10 MiB) and returns the workspace dir and file
// name. 150000 lines × ~71 bytes ≈ 10.4 MB.
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
	pad := strings.Repeat("x", 60)
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
	if !strings.Contains(out, "8: l8") || !strings.Contains(out, "10: l10") {
		t.Fatalf("expected lines 8-10, got: %s", out)
	}
	if strings.Contains(out, "7: l7") {
		t.Fatalf("line 7 must not be in a tail=3 window: %s", out)
	}
	// The tail window ends at EOF, so the dsh end-of-file footer is the
	// truthful one; the numbering shows where the window starts.
	if !strings.Contains(out, "(End of file - total 10 lines)") {
		t.Fatalf("expected end-of-file footer, got: %s", out)
	}
}

func TestReadFileTailBiggerThanFile(t *testing.T) {
	dir, name := writeTempFile(t, "t2.txt", "a\nb\nc\n")
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(100)})
	if !strings.Contains(out, "1: a") || !strings.Contains(out, "3: c") {
		t.Fatalf("expected the whole file, got: %s", out)
	}
	if !strings.Contains(out, "(End of file - total 3 lines)") {
		t.Fatalf("expected end-of-file footer, got: %s", out)
	}
}

// A default head read on a large file hits the 51200-byte cap long before
// 2000 lines: dsh ends the window with the capped footer and an exact total.
func TestReadFileLargeHeadHitsByteCap(t *testing.T) {
	dir, name := makeLargeFile(t, 150000, false)
	out := runRead(t, dir, name, nil) // default offset=1 limit=2000
	if !strings.Contains(out, "1: line-000001") {
		t.Fatalf("expected line 1, got: %s", out[:200])
	}
	if !strings.Contains(out, "(Output capped. Showing lines 1-") || !strings.Contains(out, "Use offset=") {
		t.Fatalf("expected byte-cap footer, got: %s", out[len(out)-300:])
	}
	// The cap keeps the body under readMaxBytes.
	if len(out) > readMaxBytes+4096 { // +footer+envelope headroom
		t.Fatalf("capped read still produced %d bytes", len(out))
	}
}

// A line-count-bounded window (limit small enough to fit the byte cap) must
// show exactly those lines with a continuation footer.
func TestReadFileLargeOffsetWindow(t *testing.T) {
	dir, name := makeLargeFile(t, 150000, false)
	out := runRead(t, dir, name, map[string]interface{}{"offset": float64(5000), "limit": float64(5)})
	if !strings.Contains(out, "5000: line-005000") || !strings.Contains(out, "5004: line-005004") {
		t.Fatalf("expected lines 5000-5004, got: %s", out)
	}
	if strings.Contains(out, "5005: line-005005") {
		t.Fatalf("line 5005 must not be in a 5-line window")
	}
	if !strings.Contains(out, "(Showing lines 5000-5004 of 150000. Use offset=5005 to continue.)") {
		t.Fatalf("expected continuation footer with exact total, got: %s", out[len(out)-300:])
	}
}

func TestReadFileLargeTail(t *testing.T) {
	dir, name := makeLargeFile(t, 150000, false)
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(5)})
	if !strings.Contains(out, "149996: line-149996") || !strings.Contains(out, "150000: line-150000") {
		t.Fatalf("expected last 5 lines, got: %s", out)
	}
	if !strings.Contains(out, "(End of file - total 150000 lines)") {
		t.Fatalf("expected end-of-file footer with exact total, got: %s", out[len(out)-200:])
	}
}

func TestReadFileLargeOffsetBeyondEnd(t *testing.T) {
	dir, name := makeLargeFile(t, 150000, false)
	out := runRead(t, dir, name, map[string]interface{}{"offset": float64(999999)})
	if !strings.HasPrefix(out, "Error:") {
		t.Fatalf("expected offset error, got: %s", out)
	}
	if !strings.Contains(out, `is out of range for "`) || !strings.Contains(out, "(150000 lines)") {
		t.Fatalf("expected dsh out-of-range text with the true total, got: %s", out)
	}
}

func TestReadFileLargeCRLF(t *testing.T) {
	dir, name := makeLargeFile(t, 150000, true)
	out := runRead(t, dir, name, map[string]interface{}{"tail": float64(3)})
	if strings.Contains(out, "\r") {
		t.Fatalf("CRLF must be normalized in the large path: %q", out)
	}
	if !strings.Contains(out, "150000: line-150000") {
		t.Fatalf("expected last line with correct number, got: %s", out)
	}
}

func TestReadFileLargeNoTrailingNewline(t *testing.T) {
	// File just above the threshold whose last line has no trailing newline:
	// the line count must not gain a phantom empty line.
	dir := t.TempDir()
	fp := filepath.Join(dir, "nl.log")
	var b strings.Builder
	for i := 1; i <= 150000; i++ {
		b.WriteString(fmt.Sprintf("line-%06d %s\n", i, strings.Repeat("y", 70)))
	}
	b.WriteString("final-line") // no trailing newline
	if err := os.WriteFile(fp, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "nl.log", map[string]interface{}{"tail": float64(2)})
	if !strings.Contains(out, "150001: final-line") {
		t.Fatalf("expected the unterminated final line as line 150001, got: %s", out)
	}
	if !strings.Contains(out, "(End of file - total 150001 lines)") {
		t.Fatalf("expected 150001 total lines in footer, got: %s", out[len(out)-200:])
	}
}

func TestReadFileLargeBinaryGuard(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bin.dat")
	buf := make([]byte, 11<<20) // 11MB, above the 10MiB streaming threshold
	buf[100] = 0                // NUL in the first 8KB
	if err := os.WriteFile(fp, buf, 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "bin.dat", nil)
	if !strings.Contains(out, "binary file") {
		t.Fatalf("expected binary-file error, got: %s", out)
	}
}

// The caps dsh declares and mc now enforces, pinned directly:
// long lines truncate with the dsh suffix; an explicit limit above the
// maximum is an argument error; the success envelope is the dsh one.
func TestReadCapsAndEnvelope(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("z", readMaxLineChars+500)
	fp := filepath.Join(dir, "caps.txt")
	if err := os.WriteFile(fp, []byte("first\n"+long+"\nlast\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "caps.txt", nil)
	if !strings.HasPrefix(out, "<path>") || !strings.Contains(out, "<type>file</type>\n<content>\n") || !strings.HasSuffix(out, "\n</content>") {
		t.Fatalf("expected the dsh read envelope, got: %.120s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("... (line truncated to %d chars)", readMaxLineChars)) {
		t.Fatalf("expected line truncation suffix, got: %.200s", out)
	}
	out = runRead(t, dir, "caps.txt", map[string]interface{}{"limit": float64(3000)})
	if !strings.Contains(out, "invalid arguments: limit must be less than or equal to 2000") {
		t.Fatalf("expected limit argument error, got: %s", out)
	}
	out = runRead(t, dir, "caps.txt", map[string]interface{}{"offset": float64(0)})
	if !strings.Contains(out, "invalid arguments: offset must be a positive integer") {
		t.Fatalf("expected offset argument error, got: %s", out)
	}
	out = runRead(t, dir, "caps.txt", map[string]interface{}{"file_path": "", "path": ""})
	if !strings.Contains(out, "invalid arguments: file_path must be a non-empty string") {
		t.Fatalf("expected file_path argument error, got: %s", out)
	}
}

// A truly empty file is not special-cased: it renders the zero-line footer
// inside the envelope (dsh's contract).
func TestReadEmptyFileFooter(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, nil, 0644); err != nil {
		t.Fatal(err)
	}
	out := runRead(t, dir, "empty.txt", nil)
	if !strings.Contains(out, "(End of file - total 0 lines)") {
		t.Fatalf("expected zero-line footer, got: %s", out)
	}
}
