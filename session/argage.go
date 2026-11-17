package session

import (
	"encoding/json"
	"fmt"

	"github.com/user/mini_code/provider"
)


const argAgeThreshold = 2000

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
		return "", false
	}
	fp, _ := v["file_path"].(string)
	if fp == "" {
		fp, _ = v["path"].(string)
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
		return "", false
	}
	return out, true
}

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
						return false
					}
					m.ToolCalls[j].Function.Arguments = replacement
					return true
				}
			}
		case "tool":
		default:
			return false
		}
	}
	return false
}
