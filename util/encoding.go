package util

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeToUTF8 normalizes raw program output into a UTF-8 string.
//
// On Windows, cmd.exe and most console programs write in the system ANSI
// code page (GBK/CP936 on Chinese systems). Capturing those bytes and
// persisting them via encoding/json silently turns every invalid UTF-8
// sequence into U+FFFD, destroying the text for both the UI and the LLM.
//
// Strategy: valid UTF-8 passes through untouched (covers programs that
// already emit UTF-8, e.g. git, python with UTF-8 output); otherwise the
// input is decoded as GBK. If the GBK decode fails partway, the decodable
// prefix is kept (x/text stops at the first invalid sequence).
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

// Encoding is the byte encoding of a text file on disk, as far as this
// harness can tell without a charset declaration.
type Encoding int

const (
	// EncUTF8 is UTF-8 (with or without a BOM), the default for anything the
	// harness creates itself and for every file valid as UTF-8.
	EncUTF8 Encoding = iota
	// EncGBK is the Windows Simplified-Chinese ANSI code page (CP936/GB18030
	// subset). It is the only non-UTF-8 encoding this harness round-trips:
	// it is what Notepad and most Chinese-language Windows tooling still
	// write by default.
	EncGBK
)

func (e Encoding) String() string {
	if e == EncGBK {
		return "GBK"
	}
	return "UTF-8"
}

// TextFormat is how a specific file on disk is encoded: the encoding plus
// whether it carries a UTF-8 BOM.
//
// The BOM matters because it is part of the file's identity: editors, build
// tools and diffs all see it, and a read-modify-write cycle that silently
// drops it turns a no-op edit into a whole-file change. It is stripped from
// the text handed to the model and restored when the file is written back.
type TextFormat struct {
	Encoding Encoding
	BOM      bool
}

// String renders the format for a tool result ("UTF-8", "GBK+BOM", ...).
func (f TextFormat) String() string {
	if f.BOM {
		return f.Encoding.String() + "+BOM"
	}
	return f.Encoding.String()
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// DetectTextFormat reports how the bytes are encoded. Valid UTF-8 (with or
// without BOM) is UTF-8. Anything else is treated as GBK when it decodes as
// GBK, and as UTF-8 otherwise — so undecodable bytes are passed through
// unchanged rather than replaced with U+FFFD.
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

// DecodeText decodes a file's bytes to UTF-8 and reports the format it found,
// so the caller can write the file back in the same encoding.
//
// The whole file is decoded as one unit (not per line) because a GBK lead byte
// followed by a byte in 0x0A range is impossible only for the ASCII newline
// itself; multi-byte sequences never straddle it, but decoding whole-file is
// still simpler and makes the encoder's behaviour symmetric with the decoder's.
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

// Encode text back into the file's format. The BOM is re-added when the file
// had one. It returns ok=false when the text cannot be represented in the
// target encoding (e.g. an emoji in a GBK file), so the caller can decide to
// write UTF-8 instead of silently writing '?' bytes.
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
