package util

import "github.com/user/uclaw/provider"

func CountMessagesTokens(messages []provider.Message) int {
	total := 0
	for _, msg := range messages {
		total += 4
		if len(msg.ContentParts) > 0 {
			for _, p := range msg.ContentParts {
				switch p.Type {
				case "text":
					total += len(p.Text) / 4
				case "image_url":
					total += 1100 // rough estimate for an image token
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
	total += 2
	return total
}

func EstimateTokens(text string) int {
	return len(text) / 4
}
