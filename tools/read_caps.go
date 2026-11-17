package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	readMaxLineChars = 2000
	readMaxBytes = 51200
	readMaxLimit = 2000
)

func truncateReadLine(line string) string {
	if utf8.RuneCountInString(line) <= readMaxLineChars {
		return line
	}
	return string([]rune(line)[:readMaxLineChars]) + fmt.Sprintf("... (line truncated to %d chars)", readMaxLineChars)
}

func readCappedFooter(start, end, next int) string {
	return fmt.Sprintf("(Output capped. Showing lines %d-%d. Use offset=%d to continue.)", start, end, next)
}

func formatReadOutput(displayPath, body, footer string) string {
	if body == "" {
		body = footer
	} else {
		body = body + "\n\n" + footer
	}
	return "<path>" + displayPath + "</path>\n<type>file</type>\n<content>\n" + body + "\n</content>"
}

func renderReadWindow(lines []string, startLine int) (body string, shownEnd int, kept int, byteCapped bool) {
	var b strings.Builder
	total := 0
	shownEnd = startLine - 1
	for i, line := range lines {
		n := startLine + i
		text := truncateReadLine(line)
		size := len(text) + 1
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
