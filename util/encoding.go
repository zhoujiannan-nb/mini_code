package util

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func DecodeToUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err == nil || len(out) > 0 {
		return string(out)
	}
	return string(b)
}

type Encoding int

const (
	EncUTF8 Encoding = iota
	EncGBK
)

func (e Encoding) String() string {
	if e == EncGBK {
		return "GBK"
	}
	return "UTF-8"
}

type TextFormat struct {
	Encoding Encoding
	BOM      bool
}

func (f TextFormat) String() string {
	if f.BOM {
		return f.Encoding.String() + "+BOM"
	}
	return f.Encoding.String()
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func DetectTextFormat(b []byte) TextFormat {
	if bytes.HasPrefix(b, utf8BOM) {
		return TextFormat{Encoding: EncUTF8, BOM: true}
	}
	if utf8.Valid(b) {
		return TextFormat{Encoding: EncUTF8}
	}
	if _, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return TextFormat{Encoding: EncGBK}
	}
	return TextFormat{Encoding: EncUTF8}
}

func DecodeText(b []byte) (string, TextFormat) {
	f := DetectTextFormat(b)
	if f.BOM {
		b = b[len(utf8BOM):]
	}
	if f.Encoding == EncGBK {
		out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
		if err == nil || len(out) > 0 {
			return string(out), f
		}
	}
	return string(b), f
}

func (f TextFormat) Encode(s string) (out []byte, ok bool) {
	var body []byte
	switch f.Encoding {
	case EncGBK:
		b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
		if err != nil {
			return nil, false
		}
		body = b
	default:
		if !utf8.ValidString(s) {
			return nil, false
		}
		body = []byte(s)
	}
	if f.BOM {
		out = make([]byte, 0, len(utf8BOM)+len(body))
		out = append(out, utf8BOM...)
		out = append(out, body...)
		return out, true
	}
	return body, true
}
