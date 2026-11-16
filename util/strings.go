package util

import "strings"

// TruncateRunes shortens s to n runes, marking a cut with an ellipsis.
// (It used to exist in three copies — channels, session, tools — byte-identical
// except for their names; now one copy rules.)
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// FirstNonEmpty returns the first value whose TrimSpace is non-empty ("" when
// all are blank).
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
