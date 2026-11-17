package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

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

	nonTokenErrors := []string{
		"Rate limit exceeded. Please try again later.",
		"too many requests, slow down",
		"payload exceeds the maximum size of 10MB",
		"context deadline exceeded",
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

func TestNonStreamOmitsStreamOptions(t *testing.T) {
	var (
		sawNonStream    bool
		sawStreamWithOpts bool
	)
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !req.Stream {
			if req.StreamOptions != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"Stream options can only be defined when `+ "`stream=True`"+ `. (parameter=stream_options)","type":"BadRequestError"}}`)
				return
			}
			sawNonStream = true
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			return
		}
		sawStreamWithOpts = req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	if _, err := mc.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("non-streaming Chat: %v", err)
	}
	if !sawNonStream {
		t.Error("server never saw the non-streaming request")
	}
	if _, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if !sawStreamWithOpts {
		t.Error("streaming request lost stream_options.include_usage")
	}
}
