package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/user/mini_code/util"
)

type _FsTool struct {
	workspace    string
	allowedDir   string
	extraAllowed []string
}

func (t *_FsTool) resolvePath(path string) (string, error) {
	p := path
	if !filepath.IsAbs(p) && t.workspace != "" {
		p = filepath.Join(t.workspace, p)
	}
	p = filepath.Clean(p)
	if t.allowedDir != "" {
		allDirs := append([]string{t.allowedDir}, t.extraAllowed...)
		allowed := false
		for _, d := range allDirs {
			rel, err := filepath.Rel(filepath.Clean(d), p)
			if err == nil && !strings.HasPrefix(rel, "..") {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("path %s is outside allowed directory %s", path, t.allowedDir)
		}
	}
	return p, nil
}

// ReadFileTool
type ReadFileTool struct{ _FsTool }

func NewReadFileTool(workspace string) *ReadFileTool {
	return &ReadFileTool{_FsTool{workspace: workspace}}
}

func (t *ReadFileTool) Name() string { return "read" }

// Description/Parameters: dsh's verbatim surface (tools/dsh_surface.go). The
// implementation still accepts the historical "path" key and the unlisted
// "tail" parameter, so older sessions keep working.
func (t *ReadFileTool) Description() string                { return dshDesc("read") }
func (t *ReadFileTool) IsHidden() bool                     { return false }
func (t *ReadFileTool) Parameters() map[string]interface{} { return dshParams("read") }

func (t *ReadFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	log := _FsTool{workspace: t.workspace}
	path := strParam(params, "file_path", "path")
	if strings.TrimSpace(path) == "" {
		// dsh's stable ToolArgsError text for this argument.
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	// dsh validates explicitly-passed offset/limit before touching the file
	// (parseReadArgs): a wrong argument is an argument error, not a silent
	// default. Numbers arrive as float64 from the JSON decoder.
	offset, limit := 1, readMaxLimit
	if v, present := params["offset"]; present {
		n, ok := positiveInt(v)
		if !ok {
			return NewTextResult("Error: invalid arguments: offset must be a positive integer"), nil
		}
		offset = n
	}
	if v, present := params["limit"]; present {
		n, ok := positiveInt(v)
		if !ok {
			return NewTextResult("Error: invalid arguments: limit must be a positive integer"), nil
		}
		if n > readMaxLimit {
			return NewTextResult(fmt.Sprintf("Error: invalid arguments: limit must be less than or equal to %d", readMaxLimit)), nil
		}
		limit = n
	}
	tail := intParam(params, "tail", 0) // mc extension; never schema-advertised
	fp, err := log.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(fp)
	if err != nil {
		// Point the model at the closest real names in the same directory so
		// a mistyped path (case, extension, version suffix) is fixed in one
		// step instead of costing an extra list_dir turn.
		msg := fmt.Sprintf("Error: cannot read %q: not found", path)
		if hint := fileNotFoundHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if info.IsDir() {
		msg := fmt.Sprintf("Error: cannot read %q: not a regular file", path)
		if hint := dirNotFileHint(fp); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}

	// Large files: stream the requested window instead of loading the whole
	// file into memory (see readLargeWindow). The output format matches the
	// small-file path exactly.
	if info.Size() > largeFileThreshold {
		return t.readLargeWindow(ctx, fp, path, offset, limit, tail), nil
	}

	raw, err := os.ReadFile(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err)), nil
	}

	// Binary guard: a NUL byte in the first 8KB means this is a binary file
	// (exe, image, db, zip, ...). Dumping it as text would produce U+FFFD
	// noise that wastes context and confuses the model.
	if bytes.IndexByte(raw[:min(8192, len(raw))], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: cannot read %q: binary file", path)), nil
	}

	// DecodeText: text files on Windows are often GBK (ANSI) encoded; without
	// this the raw bytes become U+FFFD noise once the tool result is
	// JSON-marshaled for the LLM or persisted. The detected format is not
	// needed here (read_file never writes), but the same helper is used by
	// edit_file/write_file so both halves of a read-modify-write cycle see
	// exactly the same text — the asymmetry between the two was the bug that
	// made a GBK file impossible to edit with a Chinese old_text.
	content, _ := util.DecodeText(raw)
	// Normalize CRLF to LF for display. CRLF is the dominant line ending on
	// Windows; without this, every line the model sees carries a trailing
	// "\r" (rendered as a "\r" escape in JSON), which wastes tokens,
	// distorts the model's view of the file, and makes the read output
	// inconsistent with edit_file's matching semantics (edit_file already
	// normalizes CRLF to LF before matching). Display only: the file on
	// disk is untouched, and edit_file restores the file's original
	// line-ending style when writing.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	// A trailing newline terminates the last line; it does not create an
	// extra empty line. Without this, "a\nb\n" would report 3 lines and
	// offset=3 would return a phantom empty line 3.
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 1 && lines[0] == "" && len(raw) == 0 {
		lines = nil // a truly empty file has zero lines (dsh renders the EOF footer)
	}
	total := len(lines)

	// tail mode: the last N lines win over offset/limit.
	if tail > 0 {
		offset = total - tail + 1
		if offset < 1 {
			offset = 1
		}
		limit = tail
	}
	if offset > total && !(total == 0 && offset == 1) {
		return NewTextResult(fmt.Sprintf("Error: offset %d is out of range for %q (%d lines)", offset, path, total)), nil
	}

	start := offset - 1
	end := start + limit
	if end > total {
		end = total
	}
	body, shownEnd, _, capped := renderReadWindow(lines[start:end], offset)
	var footer string
	switch {
	case capped:
		footer = readCappedFooter(offset, shownEnd, shownEnd+1)
	case shownEnd < total:
		footer = fmt.Sprintf("(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, shownEnd, total, shownEnd+1)
	default:
		footer = fmt.Sprintf("(End of file - total %d lines)", total)
	}
	return NewTextResult(formatReadOutput(fp, body, footer)), nil
}

// positiveInt reports whether a decoded-JSON value is an integer >= 1.
func positiveInt(v interface{}) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	if f != float64(int(f)) || int(f) < 1 {
		return 0, false
	}
	return int(f), true
}

// largeFileThreshold: files bigger than this are served by readLargeWindow,
// a streaming scan that keeps only the requested window in memory instead of
// the whole file. Loading a 100MB log (or worse, a 1GB file) byte-for-byte
// into a []string slice is the difference between a fast tool call and a
// multi-second stall / out-of-memory crash of the whole agent process.
// 10 MiB is dsh's readStreamMinSize default: below it dsh reads the whole
// file in one go, which is faster than the line scan, so mc does the same.
const largeFileThreshold = 10 << 20

// readLargeWindow serves read_file for files above largeFileThreshold with a
// streaming line scan:
//   - memory is O(window) (or O(tail)), never O(file);
//   - the exact total line count is always known (the scan keeps counting
//     after the window is full, dsh's buildWindow contract), so every footer
//     is one of the three dsh formats;
//   - tail=N keeps a ring buffer of the last N lines while counting the rest,
//     so "show me the end of the log" is one cheap call.
//
// Line semantics match the small-file path exactly: a line is a "\n"-
// terminated segment or a final unterminated segment; a trailing newline does
// not create a phantom empty line; CRLF is normalized to LF. When the scan
// stops early the total line count is unknown and the footer says so.
func (t *ReadFileTool) readLargeWindow(ctx context.Context, fp, path string, offset, limit, tail int) *ToolResult {
	f, err := os.Open(fp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error reading file: %s", err))
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	// Binary guard (same as the small-file path): NUL byte in the first 8KB.
	head := make([]byte, 8192)
	n, _ := io.ReadFull(br, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return NewTextResult(fmt.Sprintf("Error: cannot read %q: binary file", path))
	}
	// A UTF-8 BOM is part of the file's identity but not of its text: leaving
	// it in place glues an invisible U+FEFF to the first line, which the model
	// then cannot reproduce in old_text. The small-file path drops it via
	// util.DecodeText; this path scans line by line, so it drops it here.
	if bytes.HasPrefix(head[:n], []byte{0xEF, 0xBB, 0xBF}) {
		n -= 3
		copy(head[:n], head[3:])
	}
	// Re-play the 8KB head and continue with the buffered reader: the line
	// stream starts at byte 0, and br still holds everything after the head.
	src := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(head[:n]), br), 256*1024)

	lineNo := 0
	var window, ring []string
	for {
		// A cancelled call must stop scanning: tail over a multi-GB log is the
		// one place where "stop" could otherwise take a very long time. The check
		// is amortized (every 1024 lines) so it costs nothing on the hot path.
		if lineNo&1023 == 0 && ctx.Err() != nil {
			return NewTextResult("Interrupted while scanning the file.")
		}
		raw, rerr := src.ReadString('\n')
		if len(raw) > 0 {
			// DecodeToUTF8 per line: large Windows text files are often GBK;
			// line-by-line decoding is safe (a GBK lead byte never follows a
			// 0x0A newline byte).
			line := util.DecodeToUTF8([]byte(raw))
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			lineNo++
			if tail > 0 {
				ring = append(ring, line)
				if len(ring) > tail {
					ring = ring[1:]
				}
			} else if lineNo >= offset && len(window) < limit {
				window = append(window, line)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return NewTextResult(fmt.Sprintf("Error reading file: %s", rerr))
		}
	}
	total := lineNo

	if tail > 0 {
		start := total - len(ring) + 1
		if start < 1 {
			start = 1
		}
		body, _, _, _ := renderReadWindow(ring, start)
		// The tail reaches EOF by construction, so the end-of-file footer is
		// the truthful one; the line numbering shows where the window starts.
		return NewTextResult(formatReadOutput(fp, body, fmt.Sprintf("(End of file - total %d lines)", total)))
	}

	if len(window) == 0 {
		// The scan reached EOF without collecting the window: offset is past
		// the end of the file.
		return NewTextResult(fmt.Sprintf("Error: offset %d is out of range for %q (%d lines)", offset, path, total))
	}
	body, shownEnd, _, capped := renderReadWindow(window, offset)
	var footer string
	switch {
	case capped:
		footer = readCappedFooter(offset, shownEnd, shownEnd+1)
	case shownEnd < total:
		footer = fmt.Sprintf("(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, shownEnd, total, shownEnd+1)
	default:
		footer = fmt.Sprintf("(End of file - total %d lines)", total)
	}
	return NewTextResult(formatReadOutput(fp, body, footer))
}

// WriteFileTool
type WriteFileTool struct{ _FsTool }

func NewWriteFileTool(workspace string) *WriteFileTool {
	return &WriteFileTool{_FsTool{workspace: workspace}}
}

// Name/Description/Parameters: dsh's verbatim "write" surface. The
// implementation still accepts the historical "path" key and the unlisted
// "append" flag (a large-file escape hatch older sessions rely on).
func (t *WriteFileTool) Name() string                       { return "write" }
func (t *WriteFileTool) Description() string                { return dshDesc("write") }
func (t *WriteFileTool) IsHidden() bool                     { return false }
func (t *WriteFileTool) Parameters() map[string]interface{} { return dshParams("write") }

func (t *WriteFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path := strParam(params, "file_path", "path")
	content, _ := params["content"].(string)
	appendMode, _ := params["append"].(bool)
	if strings.TrimSpace(path) == "" {
		// dsh's stable ToolArgsError text for this argument.
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	// Encoding: a write must not silently transcode the file it touches. The
	// existing file's encoding (UTF-8 or GBK, with or without a BOM) is
	// preserved — including in append mode, because appending UTF-8 bytes to a
	// GBK file corrupts its tail for every reader that follows (editor, build
	// tool, the next read_file). A new file is UTF-8.
	srcFormat, _ := existingTextFormat(fp)
	if appendMode {
		// Append mode: add content to the end of the file (create it when
		// missing). This is the reliable way to build a large file in
		// several smaller write calls — a single oversized write can be
		// cut off by the output token limit, and re-writing the whole file
		// for every chunk would double the output cost.
		data := encodeFor(srcFormat, content)
		f, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", err)), nil
		}
		n, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", werr)), nil
		}
		if cerr != nil {
			return NewTextResult(fmt.Sprintf("Error appending to file: %s", cerr)), nil
		}
		if info, serr := os.Stat(fp); serr == nil {
			return NewTextResult(fmt.Sprintf("Successfully appended %d bytes to %s (file is now %d bytes)", n, fp, info.Size())), nil
		}
		return NewTextResult(fmt.Sprintf("Successfully appended %d bytes to %s", n, fp)), nil
	}
	// dsh's write contract: the five-line envelope naming the resolved path
	// and whether the file was created or updated; nothing else. The previous
	// content the overwrite replaces is not dumped into the result (dsh's UI
	// shows the diff to the human, not the model), and this also removes a
	// full re-read of the old file from every overwrite's latency.
	_, existed := existingTextFormat(fp)
	data := encodeFor(srcFormat, content)
	if err := os.WriteFile(fp, data, 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error writing file: %s", err)), nil
	}
	verb := "Created file"
	if existed {
		verb = "Updated file"
	}
	return NewTextResult("<path>" + fp + "</path>\n<type>file</type>\n<content>\n" + verb + "\n</content>"), nil
}

// existingTextFormat reports the encoding of the file at fp, so a write can put
// the text back in the form the file already had. ok is false when the path does
// not exist (a new file is UTF-8).
//
// Files up to 8 MiB are sampled whole: a head-only sample can classify a file
// whose first bytes are pure ASCII as UTF-8 while its Chinese body is GBK, and
// that mistake is exactly the mixed-encoding corruption this function exists to
// prevent. Above 8 MiB the head is the best available answer.
func existingTextFormat(fp string) (util.TextFormat, bool) {
	info, err := os.Stat(fp)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return util.TextFormat{Encoding: util.EncUTF8}, false
	}
	f, err := os.Open(fp)
	if err != nil {
		return util.TextFormat{Encoding: util.EncUTF8}, false
	}
	defer f.Close()
	limit := int64(8 << 20)
	if info.Size() < limit {
		limit = info.Size()
	}
	buf := make([]byte, limit)
	n, _ := io.ReadFull(f, buf)
	return util.DetectTextFormat(buf[:n]), true
}

// encodeFor encodes s into the file's format. When the text cannot be
// represented there (an emoji in a GBK file, say), UTF-8 is written instead
// of mangled bytes — a silent choice by content, never a corrupted one.
func encodeFor(f util.TextFormat, s string) []byte {
	data, ok := f.Encode(s)
	if ok {
		return data
	}
	return []byte(s)
}

// countTextLines counts lines the same way read_file does: a trailing newline
// terminates the last line and does not create a phantom empty line, and a
// final line without a trailing newline still counts.
func countTextLines(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	s := string(raw)
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++ // final line without a trailing newline
	}
	return n
}

// EditFileTool
type EditFileTool struct{ _FsTool }

func NewEditFileTool(workspace string) *EditFileTool {
	return &EditFileTool{_FsTool{workspace: workspace}}
}

// Name/Description/Parameters: dsh's verbatim "edit" surface. The
// implementation still accepts the historical path/old_text/new_text keys.
func (t *EditFileTool) Name() string                       { return "edit" }
func (t *EditFileTool) Description() string                { return dshDesc("edit") }
func (t *EditFileTool) IsHidden() bool                     { return false }
func (t *EditFileTool) Parameters() map[string]interface{} { return dshParams("edit") }

func (t *EditFileTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path := strParam(params, "file_path", "path")
	oldText := strParam(params, "old_string", "old_text")
	newText, ok := params["new_string"].(string)
	if !ok {
		newText, _ = params["new_text"].(string)
	}
	replaceAll, _ := params["replace_all"].(bool)

	// dsh's three stable edit validation messages (ToolArgsError), in its
	// order: a wrong argument is named exactly, not lumped together.
	if strings.TrimSpace(path) == "" {
		return NewTextResult("Error: invalid arguments: file_path must be a non-empty string"), nil
	}
	if oldText == "" {
		return NewTextResult("Error: invalid arguments: old_string must be a non-empty string"), nil
	}
	if oldText == newText {
		return NewTextResult("Error: invalid arguments: old_string and new_string must differ"), nil
	}

	fp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		// Same recovery hint as read_file: a mistyped path names the closest
		// real files instead of forcing an extra list_dir turn.
		msg := fmt.Sprintf("Error: cannot read %q: not found", path)
		if os.IsNotExist(err) {
			if hint := fileNotFoundHint(fp); hint != "" {
				msg += "\n" + hint
			}
		}
		return NewTextResult(msg), nil
	}

	// Decode with the same helper read_file uses. Before this, read_file
	// handed the model GBK-decoded text while edit_file matched against the
	// RAW bytes: a Chinese old_text copied from a successful read could never
	// match, and a successful ASCII-only edit silently rewrote the whole file
	// as UTF-8. Both halves now go through util.DecodeText / TextFormat.Encode,
	// so the text the model edits is the text it read and the file keeps its
	// original encoding (UTF-8 or GBK, BOM preserved).
	content, srcFormat := util.DecodeText(raw)
	// Detect the file's line-ending style so an edit does not silently
	// convert a CRLF file to LF: byte-exact checks, git diffs and Windows
	// tooling all notice the change. Files without CRLF stay LF.
	crlf := strings.Contains(content, "\r\n")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

	// One literal matching pass — dsh's only mechanism. The former
	// whitespace-tolerant second pass is gone: it could replace line ranges
	// the model never targeted, and its "(whitespace-tolerant match)"
	// success text promised a behavior the advertised tool does not have.
	count := strings.Count(content, oldNorm)
	if count == 0 {
		msg := fmt.Sprintf("Error: old_string was not found in %q", path)
		if hint := closestLineHint(strings.Split(content, "\n"), oldNorm); hint != "" {
			msg += "\n" + hint
		}
		return NewTextResult(msg), nil
	}
	if count > 1 && !replaceAll {
		return NewTextResult(fmt.Sprintf("Error: old_string matched %d times in %q; provide a more specific old_string or set replace_all to true", count, path)), nil
	}
	newContent := strings.ReplaceAll(content, oldNorm, newNorm)
	if crlf {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
	}
	data := encodeFor(srcFormat, newContent)
	if err := os.WriteFile(fp, data, 0644); err != nil {
		return NewTextResult(fmt.Sprintf("Error editing file: %s", err)), nil
	}
	if replaceAll {
		return NewTextResult(fmt.Sprintf("The file %s has been updated. All occurrences were successfully replaced.", fp)), nil
	}
	return NewTextResult(fmt.Sprintf("The file %s has been updated successfully.", fp)), nil
}

// wsNormalizeLine trims a line and collapses runs of spaces/tabs into a
// single space, so "  a\tb  " and "a b" compare equal.
func wsNormalizeLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}

// --- closest-line hint for failed edits ---
//
// When old_text matches nothing (exact or whitespace-tolerant), the model's
// next move is usually "re-read the whole file and try again" — a full extra
// turn. If the file actually contains a near-identical line (a typo, a
// different quote, a renamed identifier), pointing at it lets the model fix
// old_text immediately. The hint is advisory text on the error path only;
// the success path is byte-for-byte unchanged.

const (
	closestHintLineCap  = 20000 // only scan the first N file lines
	closestHintTextCap  = 400   // per-line comparison cap (runes)
	closestHintMinScore = 0.5   // below this the "closest" line is noise
)

// closestLineHint finds the file line most similar to the most distinctive
// line of oldText and renders a short hint naming its line number. It
// returns "" when nothing is close enough to be worth mentioning.
func closestLineHint(fileLines []string, oldText string) string {
	// Pick the longest non-blank old line: it is the most distinctive and
	// the most likely to have a near-twin in the file.
	var target string
	for _, l := range strings.Split(oldText, "\n") {
		n := wsNormalizeLine(l)
		if len([]rune(n)) > len([]rune(target)) {
			target = n
		}
	}
	if len([]rune(target)) < 4 {
		return "" // too short to be meaningful
	}

	targetRunes := []rune(target)
	if len(targetRunes) > closestHintTextCap {
		targetRunes = targetRunes[:closestHintTextCap]
	}
	targetBigrams := bigramCount(targetRunes)
	if len(targetBigrams) == 0 {
		return ""
	}

	bestScore := 0.0
	var bestLineNo int
	var bestLine string
	lim := len(fileLines)
	if lim > closestHintLineCap {
		lim = closestHintLineCap
	}
	for i := 0; i < lim; i++ {
		n := wsNormalizeLine(fileLines[i])
		if n == "" {
			continue
		}
		r := []rune(n)
		if len(r) > closestHintTextCap {
			r = r[:closestHintTextCap]
		}
		if len(r) < 4 {
			continue
		}
		score := bigramDice(targetBigrams, bigramCount(r))
		if score > bestScore {
			bestScore = score
			bestLineNo = i + 1
			bestLine = n
		}
	}
	if bestScore < closestHintMinScore {
		return ""
	}
	if len([]rune(bestLine)) > 120 {
		bestLine = string([]rune(bestLine)[:120]) + "…"
	}
	return fmt.Sprintf("Closest line in the file (line %d): %q — compare it with your old_text (check indentation, quotes, punctuation, spelling) and retry with the exact text.", bestLineNo, bestLine)
}

// bigramCount counts adjacent-rune pairs of s.
func bigramCount(s []rune) map[string]int {
	m := make(map[string]int, len(s))
	for i := 0; i+1 < len(s); i++ {
		m[string(s[i:i+2])]++
	}
	return m
}

// bigramDice is the Dice coefficient over rune-bigram multisets:
// 2*|A∩B| / (|A|+|B|). 1.0 = identical, 0.0 = disjoint.
func bigramDice(a, b map[string]int) float64 {
	inter := 0
	for bg, ca := range a {
		if cb, ok := b[bg]; ok {
			if ca < cb {
				inter += ca
			} else {
				inter += cb
			}
		}
	}
	total := len(a) + len(b)
	if total == 0 {
		return 0
	}
	return 2.0 * float64(inter) / float64(total)
}

// ListDirTool
type ListDirTool struct{ _FsTool }

var ignoreDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true,
	"venv": true, "dist": true, "build": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, ".coverage": true, "htmlcov": true,
}

func NewListDirTool(workspace string) *ListDirTool {
	return &ListDirTool{_FsTool{workspace: workspace}}
}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "List directory contents. File entries show their size in parentheses (e.g. \"notes.txt (2.3 KB)\"). Set recursive=true to list nested contents."
}
func (t *ListDirTool) IsHidden() bool { return false }
func (t *ListDirTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":      map[string]interface{}{"type": "string", "description": "Directory path to list"},
			"recursive": map[string]interface{}{"type": "boolean", "description": "List recursively (default false)"},
			"max_depth": map[string]interface{}{"type": "integer", "description": "Max recursion depth (default 3)", "minimum": 1, "maximum": 10},
		},
		"required": []string{"path"},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("unknown path")
	}
	dp, err := t.resolvePath(path)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
	}
	info, err := os.Stat(dp)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: directory not found: %s", path)), nil
	}
	if !info.IsDir() {
		return NewTextResult(fmt.Sprintf("Error: not a directory: %s", path)), nil
	}

	recursive, _ := params["recursive"].(bool)
	depthLimit := intParam(params, "max_depth", 3)

	var items []string
	total := 0
	cap := 400

	if recursive {
		baseDepth := len(strings.Split(dp, string(os.PathSeparator)))
		filepath.Walk(dp, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// Stop an interrupted recursive listing instead of walking the rest of
			// the tree before returning.
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if p == dp {
				return nil
			}
			currentDepth := len(strings.Split(p, string(os.PathSeparator))) - baseDepth
			if currentDepth > depthLimit {
				return filepath.SkipDir
			}
			// Noise entries are matched by the components RELATIVE to the
			// walked root only — matching the absolute path's components
			// would empty the listing whenever the workspace itself lives
			// under a directory named e.g. "build" or "dist" (glob.go and
			// search.go match entry names the same way). A noise directory
			// prunes its whole subtree; a noise file (".coverage") is
			// skipped alone.
			rel, rerr := filepath.Rel(dp, p)
			if rerr != nil {
				return nil
			}
			for _, part := range strings.Split(rel, string(os.PathSeparator)) {
				if ignoreDirs[part] {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			total++
			if total <= cap {
				if info.IsDir() {
					items = append(items, rel+"/")
				} else {
					items = append(items, rel+" ("+humanSize(info.Size())+")")
				}
			}
			return nil
		})
	} else {
		entries, err := os.ReadDir(dp)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error listing directory: %s", err)), nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if ignoreDirs[e.Name()] {
				continue
			}
			total++
			if total <= cap {
				if e.IsDir() {
					items = append(items, e.Name()+"/")
				} else {
					name := e.Name()
					// e.Info() can fail (broken symlink, permission error);
					// then the entry is shown without a size rather than
					// dropping it from the listing.
					if fi, ierr := e.Info(); ierr == nil {
						name += " (" + humanSize(fi.Size()) + ")"
					}
					items = append(items, name)
				}
			}
		}
	}

	if len(items) == 0 {
		return NewTextResult(fmt.Sprintf("Directory %s is empty", path)), nil
	}
	result := strings.Join(items, "\n")
	if total > cap {
		result += fmt.Sprintf("\n\n(truncated, showing first %d of %d entries)", cap, total)
	}
	return NewTextResult(result), nil
}

// humanSize renders a byte count in human-readable form using 1024-based
// units: "512 B", "3.4 KB", "1.2 MB", "2.0 GB". Sub-unit precision is
// dropped at KB and above (one decimal) to keep listing lines short.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
