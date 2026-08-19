package util

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeToUTF8Passthrough(t *testing.T) {
	in := []byte("hello 中文 UTF-8 passes through untouched, exit code: 0")
	if got := DecodeToUTF8(in); got != string(in) {
		t.Errorf("passthrough changed input:\n got %q\nwant %q", got, string(in))
	}
	if got := DecodeToUTF8(nil); got != "" {
		t.Errorf("nil input = %q, want empty", got)
	}
	if got := DecodeToUTF8([]byte("ascii only")); got != "ascii only" {
		t.Errorf("ascii input = %q", got)
	}
}

// TestDecodeToUTF8GBK round-trips a typical cmd.exe error message: encode
// the Chinese text as GBK (what cmd.exe on a Chinese system actually
// emits), then DecodeToUTF8 must recover the exact original.
func TestDecodeToUTF8GBK(t *testing.T) {
	cases := []string{
		"'tail' 不是内部或外部命令，也不是可运行的程序或批处理文件。",
		"系统找不到指定的文件。",
		"错误：文件名、目录名或卷标语法不正确。",
		"中文混合 mixed GBK output 与英文",
	}
	for _, original := range cases {
		b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(original))
		if err != nil {
			t.Fatalf("GBK encode(%q) failed: %v", original, err)
		}
		got := DecodeToUTF8(b)
		if got != original {
			t.Errorf("DecodeToUTF8(gbk(%q)) = %q", original, got)
		}
	}
}

// TestDecodeToUTF8InvalidFallback must never panic or lose the prefix when
// the input is neither valid UTF-8 nor fully valid GBK.
func TestDecodeToUTF8InvalidFallback(t *testing.T) {
	in := []byte{0xFF, 0xFE, 0x41, 0x42, 0x43, 0x80, 0x81}
	got := DecodeToUTF8(in)
	if got == "" {
		t.Error("expected non-empty output for invalid input")
	}
}
