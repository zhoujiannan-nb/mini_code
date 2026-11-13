package util

import (
	"log/slog"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	"github.com/user/mini_code/provider"
)

// tiktokenWarnOnce keeps the offline warning to one line per process. It used to
// be logged on every count — thousands of identical lines that buried the one
// fact that matters: every context decision is running on the fallback estimate.
var tiktokenWarnOnce sync.Once

func warnTiktokenUnavailable(err error) {
	tiktokenWarnOnce.Do(func() {
		slog.Warn("tiktoken BPE file unavailable; token counts use the built-in estimate (CJK-aware). "+
			"The provider's own usage numbers still calibrate it (see token_calib.go).",
			"error", err)
	})
}

// CountMessagesTokens counts tokens in messages using tiktoken.
// Image parts are counted with the Qwen2-VL visual-token formula (see
// image_tokens.go) instead of a fixed estimate, so context budgeting reflects
// the real token cost of each image. This is a pure read: it never mutates the
// messages, so it cannot affect the context sequence sent to the model.
func CountMessagesTokens(messages []provider.Message) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		warnTiktokenUnavailable(err)
		return fallbackCountMessagesTokens(messages)
	}

	total := 0
	for _, msg := range messages {
		total += 3 // message overhead
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
			total += 1 // extra token for name field
		}
	}

	total += 3 // reply prefix
	return total
}

// EstimateTokens estimates tokens for a single text string.
func EstimateTokens(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		warnTiktokenUnavailable(err)
		return EstimateTextFallback(text)
	}
	return len(tke.Encode(text, nil, nil))
}

// fallbackCountMessagesTokens provides fallback when tiktoken is unavailable.
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
	// cjkTokensPerRune is the fallback cost of one CJK character. cl100k (and
	// Qwen, whose tokenizer is close) spend roughly 1-1.5 tokens per Chinese
	// character; the old len/4 fallback spent 0.75 and therefore under-counted a
	// Chinese conversation by about half. Under-counting is the dangerous
	// direction: compaction fires late and the provider answers with a 400.
	cjkTokensPerRune = 1.5
	// asciiBytesPerToken is the well-known cl100k rule of thumb for English and
	// code.
	asciiBytesPerToken = 4.0
)

// EstimateTextFallback estimates a string's token count without a tokenizer,
// with the CJK case handled honestly: ASCII bytes at 4 per token, CJK runes at
// 1.5 each, everything else at one token per rune conservatively.
//
// It exists for the offline case: tiktoken downloads its BPE file on first use,
// and on a machine without internet it fails forever, silently degrading every
// context decision. See CountMessagesTokens for the warning.
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
	return int(tokens + 0.999) // round up: never under-count
}

// isCJK reports whether r is in the ranges that tokenize as Chinese/Japanese/
// Korean text (the case the old byte-based fallback got most wrong).
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // extension A
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // full-width forms
		return true
	case r >= 0x3040 && r <= 0x30FF: // hiragana / katakana
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // hangul
		return true
	}
	return false
}
