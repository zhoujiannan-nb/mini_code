package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
)

// newTestLoop builds an AgentLoop whose model client points at the given
// test server. Compaction is disabled so no extra LLM calls are made.
func newTestLoop(t *testing.T, baseURL string) (*AgentLoop, *provider.ModelClient) {
	t.Helper()
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       baseURL,
		APIKey:        "test",
		ModelName:     "test-model",
		MaxTokens:     100,
		ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	loop := NewAgentLoop(client, 5, nil, nil, nil, nil, nil, nil)
	loop.compactionOn = false
	return loop, client
}

// writeSSEChunk writes one SSE data line.
func writeSSEChunk(w http.ResponseWriter, payload string) {
	w.Write([]byte("data: " + payload + "\n\n"))
}

// TestChatWithRetryRecoversFromMidStreamDrop verifies that a stream which
// drops mid-response (no finish_reason, no [DONE]) is retried at the loop
// level and the task still completes with the full content.
func TestChatWithRetryRecoversFromMidStreamDrop(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First attempt: partial stream, then the connection ends
			// without finish_reason/[DONE] (simulated mid-flight drop).
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"par"}}]}`)
			return
		}
		// Second attempt: a well-formed complete stream.
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"hello world"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	res, err := loop.Run(context.Background(), "system prompt", "say hello", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success after retry, got error: %q", res.Error)
	}
	if res.Content != "hello world" {
		t.Fatalf("expected content %q, got %q", "hello world", res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests (1 dropped + 1 recovered), got %d", got)
	}
}

// TestChatWithRetryGivesUpAfterMaxAttempts verifies that a persistently
// dropping stream is retried a bounded number of times (3 total) and then
// the task fails with a clear error instead of looping forever.
func TestChatWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"partial"}}]}`)
		// Always drop: no finish_reason, no [DONE].
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	res, err := loop.Run(context.Background(), "system prompt", "say hello", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure, got success")
	}
	if res.Error == "" {
		t.Fatal("expected a non-empty error explaining the failure")
	}
	if got := atomic.LoadInt64(&requests); got != 3 {
		t.Fatalf("expected exactly 3 LLM requests (initial + 2 retries), got %d", got)
	}
}
