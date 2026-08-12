package util

import "encoding/json"

func ExtractJSON(text string) (interface{}, error) {
	if text == "" {
		return nil, nil
	}
	// strategy 1: ```JSON_START ... ```JSON_END
	if s, ok := extractBetween(text, "```JSON_START", "```JSON_END"); ok {
		if v, err := jsonUnmarshal(s); err == nil {
			return v, nil
		}
	}
	// strategy 2: ```json ... ```
	if s, ok := extractCodeBlock(text, "json"); ok {
		if v, err := jsonUnmarshal(s); err == nil {
			return v, nil
		}
	}
	// strategy 3: direct parse
	if v, err := jsonUnmarshal(text); err == nil {
		return v, nil
	}
	// strategy 4: find first balanced JSON structure
	if v := findJSONInText(text); v != nil {
		return v, nil
	}
	return nil, nil
}

func extractBetween(text, open, close string) (string, bool) {
	start := indexOf(text, open)
	if start < 0 {
		return "", false
	}
	start += len(open)
	end := indexOf(text[start:], close)
	if end < 0 {
		return text[start:], false
	}
	return text[start : start+end], true
}

func extractCodeBlock(text, lang string) (string, bool) {
	marker := "```" + lang
	start := indexOf(text, marker)
	if start < 0 {
		start = indexOf(text, "```"+toUpper(lang))
		if start < 0 {
			return "", false
		}
	}
	start += len(marker)
	for start < len(text) && text[start] == ' ' {
		start++
	}
	end := indexOf(text[start:], "```")
	if end < 0 {
		return text[start:], true
	}
	return text[start : start+end], true
}

func jsonUnmarshal(s string) (interface{}, error) {
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

func findJSONInText(text string) interface{} {
	for i := 0; i < len(text); i++ {
		if text[i] == '{' {
			if v := extractBalanced(text, i, '{', '}'); v != nil {
				return v
			}
		}
		if text[i] == '[' {
			if v := extractBalanced(text, i, '[', ']'); v != nil {
				return v
			}
		}
	}
	return nil
}

func extractBalanced(text string, start int, open, close byte) interface{} {
	depth := 0
	inString := false
	escapeNext := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if escapeNext {
			escapeNext = false
			continue
		}
		if ch == '\\' && inString {
			escapeNext = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if ch == open {
				depth++
			} else if ch == close {
				depth--
				if depth == 0 {
					candidate := text[start : i+1]
					if v, err := jsonUnmarshal(candidate); err == nil {
						return v
					}
					nextStart := indexOf(text[i+1:], string(open))
					if nextStart >= 0 {
						return extractBalanced(text, i+1+nextStart, open, close)
					}
					return nil
				}
			}
		}
	}
	return nil
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func toUpper(s string) string {
	if len(s) == 0 {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}
