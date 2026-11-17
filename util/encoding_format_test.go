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

func TestEncodeRejectsUnrepresentable(t *testing.T) {
	f := TextFormat{Encoding: EncGBK}
	if _, ok := f.Encode("表情 🙂"); ok {
		t.Fatal("Encode reported success for text GBK cannot represent")
	}
}
