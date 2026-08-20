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

// TestTruncatedTextContinuesAndConcatenates: a text response cut off by the
// output token limit (finish_reason="length") must not end the task with a
// partial answer. The loop asks the model to continue, and the final content
// is the concatenation of the truncated chunk and the continuation.
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
	// The continuation instruction must have been sent to the model.
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

// TestTruncatedTextCapStopsLooping: a model that keeps getting truncated
// must not loop forever. After maxTruncContinues continuations the loop
// returns the accumulated (best-effort) content.
func TestTruncatedTextCapStopsLooping(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, sseTextChunk("x", "length")) // always truncated
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
	// initial truncated chunk + maxTruncContinues continuations = 4 chunks
	if res.Content != strings.Repeat("x", 1+maxTruncContinues) {
		t.Fatalf("expected %q, got %q", strings.Repeat("x", 1+maxTruncContinues), res.Content)
	}
	if got := atomic.LoadInt64(&requests); got != 1+maxTruncContinues {
		t.Fatalf("expected exactly %d LLM requests, got %d", 1+maxTruncContinues, got)
	}
}

// TestTruncatedToolCallSuggestsSplit: a tool call whose arguments were cut
// off mid-JSON by the output token limit must produce an error that names
// the real cause (token limit) and points at the fix (split the payload),
// instead of the generic "invalid JSON" message.
func TestTruncatedToolCallSuggestsSplit(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// Arguments cut off mid-string: not valid JSON.
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
	// The model must see the truncation-specific guidance.
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
	// The truncated call must not have been executed.
	for _, m := range res.Messages {
		if m.Role == "tool" {
			t.Fatalf("truncated tool call must not be executed, but a tool result exists: %q", m.GetText())
		}
	}
}

// TestTruncationStateResetOnToolCall: when the model abandons a truncated
// text answer and moves on to a tool call, the earlier truncated chunk must
// not leak into the final answer.
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
