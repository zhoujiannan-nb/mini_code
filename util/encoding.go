package util

import (
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
