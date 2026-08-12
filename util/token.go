package util

import (
	"log/slog"

	"github.com/pkoukk/tiktoken-go"
	"github.com/user/mini_code/provider"
)

// CountMessagesTokens counts tokens in messages using tiktoken for accurate calculation.
// For images, it uses a rough estimate since image token calculation is model-specific.
func CountMessagesTokens(messages []provider.Message) int {
	// Get tokenizer for cl100k_base encoding (used by GPT-4 and GPT-3.5-turbo)
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		slog.Warn("failed to get tiktoken encoding, falling back to char estimation", "error", err)
		return fallbackCountMessagesTokens(messages)
	}

	total := 0
	for _, msg := range messages {
		// Each message has overhead: <|start|>{role}\n{content}<|end|>\n
		// For gpt-3.5-turbo-0613 and gpt-4-0613, this is 3 tokens
		total += 3

		// Count tokens for role
		total += len(tke.Encode(msg.Role, nil, nil))

		// Count tokens for content
		if len(msg.ContentParts) > 0 {
			for _, p := range msg.ContentParts {
				switch p.Type {
				case "text":
					total += len(tke.Encode(p.Text, nil, nil))
				case "image_url":
					// Image token count is model-specific and complex.
					// Use rough estimate: 1100 tokens per image (typical for GPT-4V)
					total += 1100
				}
			}
		} else {
			total += len(tke.Encode(msg.Content, nil, nil))
		}

		// Count tokens for tool calls
		if msg.ToolCalls != nil {
			for _, tc := range msg.ToolCalls {
				total += len(tke.Encode(tc.Function.Name, nil, nil))
				total += len(tke.Encode(tc.Function.Arguments, nil, nil))
			}
		}

		// Count tokens for name (if present)
		if msg.Name != "" {
			total += len(tke.Encode(msg.Name, nil, nil))
			// Extra token for name field
			total += 1
		}
	}

	// Every reply is primed with <|start|>assistant<|message|>
	total += 3

	return total
}

// EstimateTokens estimates tokens for a single text string using tiktoken.
func EstimateTokens(text string) int {
	tke, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		// Fallback to character estimation
		return len(text) / 4
	}
	return len(tke.Encode(text, nil, nil))
}

// fallbackCountMessagesTokens provides a fallback estimation when tiktoken is unavailable.
func fallbackCountMessagesTokens(messages []provider.Message) int {
	total := 0
	for _, msg := range messages {
		total += 3 // message overhead
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
