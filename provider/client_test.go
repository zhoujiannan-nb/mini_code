package provider

import "testing"

// TestIsTokenErrorClassifies verifies that context-overflow errors are
// recognized (fail fast) while unrelated transient errors are not (so
// they still get retried).
func TestIsTokenErrorClassifies(t *testing.T) {
	mc := &ModelClient{}

	tokenErrors := []string{
		"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens (135000 in your messages; 5000 for the completion). Please reduce the length of the messages or completion.",
		"prompt is too long: 150000 tokens > 128000 maximum",
		"context window exceeded: requested 200000 tokens",
		"token limit reached for model qwen3.6-35b-fp8",
		"invalid value for max_tokens: exceeds model limit",
		"maximum input tokens is 128000, got 140000",
		"output tokens exceed the reserved budget",
	}
	for _, msg := range tokenErrors {
		if !mc.isTokenError(msg) {
			t.Errorf("isTokenError(%q) = false, want true", msg)
		}
	}

	// These must NOT be treated as token errors, otherwise transient or
	// unrelated failures would skip retries and kill the whole run.
	nonTokenErrors := []string{
		"Rate limit exceeded. Please try again later.",
		"too many requests, slow down",
		"payload exceeds the maximum size of 10MB",
		"context deadline exceeded", // client-side timeout
		"max concurrent requests exceeded",
		"request header size exceeds limit",
		"connection refused",
		"internal server error",
		"model not found",
		"",
	}
	for _, msg := range nonTokenErrors {
		if mc.isTokenError(msg) {
			t.Errorf("isTokenError(%q) = true, want false", msg)
		}
	}
}
