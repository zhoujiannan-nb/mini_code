package util

import (
	"log/slog"

	"github.com/pkoukk/tiktoken-go"
	"github.com/user/mini_code/provider"
)

// CountMessagesTokens counts tokens in messages using tiktoken.
// Images use a rough estimate (1100 tokens) since calculation is model-specific.
func CountMessagesTokens(messages []provider.Message) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		slog.Warn("failed to get tiktoken encoding, falling back to char estimation", "error", err)
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
					total += 1100
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
		return len(text) / 4
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
					total += len(p.Text) / 4
				case "image_url":
					total += 1100
				}
			}
		} else {
			total += len(msg.Content) / 4
		}
		if msg.ReasoningContent != "" {
			total += len(msg.ReasoningContent) / 4
		}
		if msg.ToolCalls != nil {
			for _, tc := range msg.ToolCalls {
				total += len(tc.Function.Name) / 4
				total += len(tc.Function.Arguments) / 4
			}
		}
		if msg.Name != "" {
			total += len(msg.Name) / 4
		}
	}
	total += 3
	return total
}
