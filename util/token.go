package util

import (
	"log/slog"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	"github.com/user/mini_code/provider"
)

var tiktokenWarnOnce sync.Once

func warnTiktokenUnavailable(err error) {
	tiktokenWarnOnce.Do(func() {
		slog.Warn("tiktoken BPE file unavailable; token counts use the built-in estimate (CJK-aware). "+
			"The provider's own usage numbers still calibrate it (see token_calib.go).",
			"error", err)
	})
}

func WarmTokenizer() {
	if _, err := tiktoken.GetEncoding("cl100k_base"); err != nil {
		warnTiktokenUnavailable(err)
	}
}

func CountMessagesTokens(messages []provider.Message) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		warnTiktokenUnavailable(err)
		return fallbackCountMessagesTokens(messages)
	}

	total := 0
	for _, msg := range messages {
		total += 3
		total += len(tke.Encode(msg.Role, nil, nil))

		if len(msg.ContentParts) > 0 {
			for _, p := range msg.ContentParts {
				switch p.Type {
				case "text":
					total += len(tke.Encode(p.Text, nil, nil))
				case "image_url":
					if p.ImageURL != nil && p.ImageURL.URL != "" {
						total += imageTokensFromDataURL(p.ImageURL.URL)
					} else {
						total += fallbackImageTokens
					}
				}
			}
		} else {
			total += len(tke.Encode(msg.Content, nil, nil))
		}

		if msg.ReasoningContent != "" {
			total += len(tke.Encode(msg.ReasoningContent, nil, nil))
		}

		if msg.ToolCalls != nil {
			for _, tc := range msg.ToolCalls {
				total += len(tke.Encode(tc.Function.Name, nil, nil))
				total += len(tke.Encode(tc.Function.Arguments, nil, nil))
			}
		}

		if msg.Name != "" {
			total += len(tke.Encode(msg.Name, nil, nil))
			total += 1
		}
	}

	total += 3
	return total
}

func EstimateTokens(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		warnTiktokenUnavailable(err)
		return EstimateTextFallback(text)
	}
	return len(tke.Encode(text, nil, nil))
}

func fallbackCountMessagesTokens(messages []provider.Message) int {
	total := 0
	for _, msg := range messages {
		total += 3
		if len(msg.ContentParts) > 0 {
			for _, p := range msg.ContentParts {
				switch p.Type {
				case "text":
					total += EstimateTextFallback(p.Text)
				case "image_url":
					if p.ImageURL != nil && p.ImageURL.URL != "" {
						total += imageTokensFromDataURL(p.ImageURL.URL)
					} else {
						total += fallbackImageTokens
					}
				}
			}
		} else {
			total += EstimateTextFallback(msg.Content)
		}
		if msg.ReasoningContent != "" {
			total += EstimateTextFallback(msg.ReasoningContent)
		}
		if msg.ToolCalls != nil {
			for _, tc := range msg.ToolCalls {
				total += EstimateTextFallback(tc.Function.Name)
				total += EstimateTextFallback(tc.Function.Arguments)
			}
		}
		if msg.Name != "" {
			total += EstimateTextFallback(msg.Name)
		}
	}
	total += 3
	return total
}

const (
	cjkTokensPerRune = 1.5
	asciiBytesPerToken = 4.0
)

func EstimateTextFallback(s string) int {
	if s == "" {
		return 0
	}
	ascii := 0
	cjk := 0
	other := 0
	for _, r := range s {
		switch {
		case r < 128:
			ascii++
		case isCJK(r):
			cjk++
		default:
			other++
		}
	}
	tokens := float64(ascii)/asciiBytesPerToken + float64(cjk)*cjkTokensPerRune + float64(other)
	return int(tokens + 0.999)
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	case r >= 0x3000 && r <= 0x303F:
		return true
	case r >= 0xFF00 && r <= 0xFFEF:
		return true
	case r >= 0x3040 && r <= 0x30FF:
		return true
	case r >= 0xAC00 && r <= 0xD7AF:
		return true
	}
	return false
}
