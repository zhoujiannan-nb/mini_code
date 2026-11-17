package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/tools"
)

func TestTruncatedTextContinuesAndConcatenates(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, sseTextChunk("The quick brown fox ", "length"))
		} else {
			writeSSEChunk(w, sseTextChunk("jumps over the lazy dog.", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	res, err := loop.Run(context.Background(), "system prompt", "tell me a sentence", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	want := "The quick brown fox jumps over the lazy dog."
	if res.Content != want {
		t.Fatalf("expected concatenated content %q, got %q", want, res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests (truncated + continuation), got %d", got)
	}
	found := false
	for _, m := range res.Messages {
		if m.Role == "user" && strings.Contains(m.GetText(), "cut off by the output token limit") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a user message instructing the model to continue the truncated answer")
	}
}

func TestTruncatedTextCapStopsLooping(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, sseTextChunk("x", "length"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.maxTurns = 10

	res, err := loop.Run(context.Background(), "system prompt", "write a very long answer", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected best-effort success, got error: %q", res.Error)
	}
	if res.Content != strings.Repeat("x", 1+maxTruncContinues) {
		t.Fatalf("expected %q, got %q", strings.Repeat("x", 1+maxTruncContinues), res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 1+maxTruncContinues {
		t.Fatalf("expected exactly %d LLM requests, got %d", 1+maxTruncContinues, got)
	}
}

func TestTruncatedToolCallSuggestsSplit(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, sseToolCallChunk("write_file", `{\"path\":\"big.txt\",\"content\":\"AAAA`, "length"))
		} else {
			writeSSEChunk(w, sseTextChunk("done", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "write_file", result: "written"})
	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg

	res, err := loop.Run(context.Background(), "system prompt", "write a huge file", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests, got %d", got)
	}
	found := false
	for _, m := range res.Messages {
		txt := m.GetText()
		if strings.Contains(txt, "output token limit") && strings.Contains(txt, "smaller") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the error message to mention the output token limit and splitting the payload")
	}
	for _, m := range res.Messages {
		if m.Role == "tool" {
			t.Fatalf("truncated tool call must not be executed, but a tool result exists: %q", m.GetText())
		}
	}
}

func TestTruncationStateResetOnToolCall(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			writeSSEChunk(w, sseTextChunk("part1 ", "length"))
		case 2:
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
		default:
			writeSSEChunk(w, sseTextChunk("final", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})
	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg

	res, err := loop.Run(context.Background(), "system prompt", "do the thing", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "final" {
		t.Fatalf("expected final content %q (truncated chunk must be dropped), got %q", "final", res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 3 {
		t.Fatalf("expected exactly 3 LLM requests, got %d", got)
	}
}
