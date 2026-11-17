package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEmptyResponseNudgesAndRecovers(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
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

func TestEmptyResponseFailsAfterMaxRetries(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
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
	if got := atomic.LoadInt64(&requests); got != 1+maxEmptyRetries {
		t.Fatalf("expected exactly %d LLM requests, got %d", 1+maxEmptyRetries, got)
	}
}

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
