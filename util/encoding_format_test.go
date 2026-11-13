package util

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func gbk(t *testing.T, s string) []byte {
	t.Helper()
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode %q to GBK: %v", s, err)
	}
	return b
}

// TestTextFormatDetectsGBK pins the detection rule: bytes that are not valid
// UTF-8 but decode as GBK are GBK, and everything else stays UTF-8 (so
// undecodable bytes pass through instead of becoming U+FFFD).
func TestTextFormatDetectsGBK(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want TextFormat
	}{
		{"ascii", []byte("hello"), TextFormat{Encoding: EncUTF8}},
		{"utf8 chinese", []byte("你好"), TextFormat{Encoding: EncUTF8}},
		{"gbk chinese", gbk(t, "你好，世界"), TextFormat{Encoding: EncGBK}},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte("hi")...), TextFormat{Encoding: EncUTF8, BOM: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectTextFormat(c.in); got != c.want {
				t.Fatalf("DetectTextFormat = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestDecodeTextRoundTripsGBK is the property that matters for editing a GBK
// file: the text handed to the model, encoded back into the detected format,
// must be the original bytes. If it is not, every edit rewrites the file.
func TestDecodeTextRoundTripsGBK(t *testing.T) {
	src := "第一行 comment\n第二行 中文\n"
	raw := gbk(t, src)

	text, format := DecodeText(raw)
	if format.Encoding != EncGBK {
		t.Fatalf("format = %v, want GBK", format)
	}
	if text != src {
		t.Fatalf("decoded = %q, want %q", text, src)
	}
	back, ok := format.Encode(text)
	if !ok {
		t.Fatal("Encode reported failure for text it just decoded")
	}
	if !bytes.Equal(back, raw) {
		t.Fatalf("round-trip bytes differ:\ngot:  %x\nwant: %x", back, raw)
	}
}

// TestBOMRoundTrip: the BOM is not part of the text, but it is part of the
// file. A read-modify-write must keep it.
func TestBOMRoundTrip(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("package main\n")...)
	text, format := DecodeText(raw)
	if !format.BOM {
		t.Fatal("BOM not detected")
	}
	if text != "package main\n" {
		t.Fatalf("decoded = %q, want the text without the BOM", text)
	}
	back, ok := format.Encode(text)
	if !ok || !bytes.Equal(back, raw) {
		t.Fatalf("round-trip lost the BOM: got %x, want %x", back, raw)
	}
}

// TestEncodeRejectsUnrepresentable: an emoji cannot live in GBK. Encode must
// say so rather than writing '?' bytes, so the caller can deliberately choose
// UTF-8 and tell the model.
func TestEncodeRejectsUnrepresentable(t *testing.T) {
	f := TextFormat{Encoding: EncGBK}
	if _, ok := f.Encode("表情 🙂"); ok {
		t.Fatal("Encode reported success for text GBK cannot represent")
	}
}
