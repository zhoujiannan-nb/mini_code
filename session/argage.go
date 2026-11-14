package session

import (
	"encoding/json"
	"fmt"

	"github.com/user/mini_code/provider"
)

// dsh-style argument aging. The third round of the A/B test measured the real
// token hog of creative tasks: NOT tool results (those are pruned) but the
// write_file/edit_file arguments sitting in the assistant history — t1's calls
// carried 128K characters of payload that every later turn re-sends, which is
// what produced the 1.4M-input-token runs. A write that has SUCCEEDED has no
// future need for its payload: the file on disk is the copy of record, and
// read_file is the way back to it. So once such a call is answered, its
// arguments in the assistant message are replaced by a one-line summary that
// keeps what the model still needs to reason about (which file, how big) and
// says it was aged out.
//
// Safety rails:
//   - only write_file/edit_file, only after a non-error, non-interrupted result;
//   - only above argAgeThreshold runes, so small edits keep full fidelity;
//   - the replacement is valid JSON carrying the original file_path, so a
//     replayed history never trips a provider-side arguments parse;
//   - idempotent: the summary is far below threshold, a second pass is a no-op;
//   - the ledger keeps full fidelity (the tool/call row is append-only); only
//     the model history and the persisted projection shrink. A compaction/argage
//     shadow event prices every cut.

// argAgeThreshold is the write-size floor worth aging: below it the re-send
// cost is smaller than the churn of rewriting history.
const argAgeThreshold = 2000

// ageOutCallArgs renders the summary replacement for a big write/edit
// argument blob. Returns ("", false) when the call must keep its arguments.
func ageOutCallArgs(name, args string) (string, bool) {
	switch name {
	case "write_file", "write", "edit_file", "edit":
	default:
		return "", false
	}
	if len([]rune(args)) <= argAgeThreshold {
		return "", false
	}
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil {
		return "", false // not JSON we understand: leave it alone
	}
	fp, _ := v["file_path"].(string) // dsh key first
	if fp == "" {
		fp, _ = v["path"].(string) // historical mc key
	}
	if fp == "" {
		return "", false
	}
	runeLen := func(s string) int { return len([]rune(s)) }
	strArg := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := v[k].(string); ok {
				return s
			}
		}
		return ""
	}
	var summary map[string]any
	switch name {
	case "write_file", "write":
		summary = map[string]any{
			"file_path": fp,
			"content":   fmt.Sprintf("[aged out: wrote %d chars; the file on disk is the copy of record — read before editing again]", runeLen(strArg("content"))),
		}
	case "edit_file", "edit":
		summary = map[string]any{
			"file_path":  fp,
			"old_string": fmt.Sprintf("[aged out: replaced %d chars]", runeLen(strArg("old_string", "old_text"))),
			"new_string": fmt.Sprintf("[aged out: with %d chars]", runeLen(strArg("new_string", "new_text"))),
		}
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return "", false
	}
	out := string(b)
	if len([]rune(out)) >= len([]rune(args)) {
		return "", false // a "shrink" that does not shrink is a bug, not a save
	}
	return out, true
}

// ageCallArgs walks back through the trailing assistant block and swaps the
// arguments of the tool call with the given id. Mutates in place: the caller's
// slice and everything that later persists it see the summary. Reports whether
// a replacement happened (false = not found, wrong block, or already aged).
func ageCallArgs(messages []provider.Message, callID, replacement string) bool {
	if callID == "" {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m := &messages[i]
		switch m.Role {
		case "assistant":
			for j := range m.ToolCalls {
				if m.ToolCalls[j].ID == callID {
					if len([]rune(m.ToolCalls[j].Function.Arguments)) <= argAgeThreshold {
						return false // already aged or never eligible: no-op
					}
					m.ToolCalls[j].Function.Arguments = replacement
					return true
				}
			}
		case "tool":
			// part of the trailing batch's result run: keep scanning back
		default:
			return false // walked past the trailing assistant block
		}
	}
	return false
}
