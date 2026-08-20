package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestEmptyResponseNudgesAndRecovers verifies that a model response with no
// content and no tool calls does not end the task with an empty reply: the
// loop nudges the model once and the task still completes with the content
// of the next (non-empty) response.
func TestEmptyResponseNudgesAndRecovers(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First attempt: a well-formed but EMPTY response (stop, no
			// content, no tool calls).
			writeSSEChunk(w, `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		// Second attempt: the model recovers and answers.
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
		t.Fatalf("expected success after empty-response nudge, got error: %q", res.Error)
	}
	if res.Content != "hello world" {
		t.Fatalf("expected content %q, got %q", "hello world", res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests (1 empty + 1 recovered), got %d", got)
	}
}

// TestEmptyResponseFailsAfterMaxRetries verifies that a persistently empty
// model is nudged a bounded number of times (maxEmptyRetries) and then the
// task fails with a clear error instead of looping until the turn budget.
func TestEmptyResponseFailsAfterMaxRetries(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		// Always empty.
		writeSSEChunk(w, `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	res, err := loop.Run(context.Background(), "system prompt", "say hello", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure after repeated empty responses, got success")
	}
	if res.Error == "" {
		t.Fatal("expected a non-empty error explaining the failure")
	}
	// 1 initial response + maxEmptyRetries nudged retries.
	if got := atomic.LoadInt64(&requests); got != 1+maxEmptyRetries {
		t.Fatalf("expected exactly %d LLM requests, got %d", 1+maxEmptyRetries, got)
	}
}

// TestWhitespaceOnlyResponseIsTreatedAsEmpty verifies that a response made
// of whitespace only is guarded exactly like an empty one (the task does
// not end with a blank reply).
func TestWhitespaceOnlyResponseIsTreatedAsEmpty(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"  \n\t "},"finish_reason":"stop"}]}`)
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`)
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
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "final answer" {
		t.Fatalf("expected content %q, got %q", "final answer", res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests, got %d", got)
	}
}
