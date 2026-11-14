// read_caps.go — dsh-parity output caps for read (measured from
// dsh-tool-fs: readMaxLineLength=2000 chars/line, readMaxBytes=51200 per
// call; README 119707322 config table, read-render.ts renderers).
package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// readMaxLineChars mirrors dsh's readMaxLineLength default (2000).
	readMaxLineChars = 2000
	// readMaxBytes mirrors dsh's readMaxBytes default (51200): the byte cap
	// over the selected lines of one read call. When the window exceeds it
	// the read ends early with the "(Output capped. …)" footer.
	readMaxBytes = 51200
	// readMaxLimit mirrors dsh's readLimit default: limit above this is a
	// rejected argument, not a silently clipped one.
	readMaxLimit = 2000
)

// truncateReadLine keeps the first readMaxLineChars characters of a line and
// appends dsh's exact truncation suffix (a measured "..." marker, not an
// ellipsis character).
func truncateReadLine(line string) string {
	if utf8.RuneCountInString(line) <= readMaxLineChars {
		return line
	}
	return string([]rune(line)[:readMaxLineChars]) + fmt.Sprintf("... (line truncated to %d chars)", readMaxLineChars)
}

// readCappedFooter is dsh's exact early-stop footer:
//
//	(Output capped. Showing lines <s>-<e>. Use offset=<n> to continue.)
func readCappedFooter(start, end, next int) string {
	return fmt.Sprintf("(Output capped. Showing lines %d-%d. Use offset=%d to continue.)", start, end, next)
}

// formatReadOutput is dsh's exact success envelope (dsh-tool-fs README L210 /
// formatReadOutput): the resolved path, the type, and the numbered body plus
// one footer inside <content>. An empty window renders the footer alone.
func formatReadOutput(displayPath, body, footer string) string {
	if body == "" {
		body = footer
	} else {
		body = body + "\n\n" + footer
	}
	return "<path>" + displayPath + "</path>\n<type>file</type>\n<content>\n" + body + "\n</content>"
}

// renderReadWindow formats one selected window of lines the way dsh's
// read-render does — `<n>: <text>` per line — and enforces the per-call byte
// cap: lines accumulate bytes and the first line that crosses readMaxBytes
// ends the window with the capped footer. The first line is always kept
// (dsh pushes a line, then lets the overflow flag end the window), so a
// single oversized line is still shown, truncated per line.
// It returns the formatted body, the last line number actually shown, how
// many lines were kept, and whether the byte cap cut the window short.
func renderReadWindow(lines []string, startLine int) (body string, shownEnd int, kept int, byteCapped bool) {
	var b strings.Builder
	total := 0
	shownEnd = startLine - 1
	for i, line := range lines {
		n := startLine + i
		text := truncateReadLine(line)
		size := len(text) + 1 // line bytes plus the newline that separates it
		if i > 0 && total+size > readMaxBytes {
			return strings.TrimRight(b.String(), "\n"), shownEnd, kept, true
		}
		total += size
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d: %s", n, text)
		shownEnd = n
		kept++
	}
	return b.String(), shownEnd, kept, false
}
