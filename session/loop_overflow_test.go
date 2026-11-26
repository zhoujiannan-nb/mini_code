package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/provider"
)

func TestLoopShrinksOnContextOverflow(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"maximum context length is 4096 tokens; prompt exceeds the context","code":"context_length_exceeded"}}`))
			return
		}
		sseText(w, "recovered")
	}))
	defer srv.Close()
	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	bigOut := strings.Repeat("oversized tool output ", 2000)
	history := []provider.Message{
		{Role: "user", Content: "work"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "e1", Type: "function", Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"bench"}`}},
		}},
		{Role: "tool", Content: bigOut, ToolCallID: "e1", Name: "exec"},
		{Role: "assistant", Content: "a1"},
		{Role: "assistant", Content: "a2"},
		{Role: "assistant", Content: "a3"},
	}
	res, err := loop.Run(context.Background(), "SYSTEM", "continue", history)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("the loop should recover from a context overflow, got %+v", res)
	}
	if atomic.LoadInt64(&requests) < 2 {
		t.Fatalf("expected a retry after shrinking, got %d requests", requests)
	}
	pruned := false
	for _, m := range res.Messages {
		if m.Role == "tool" && (strings.Contains(m.GetText(), "pruned") || strings.Contains(m.GetText(), "truncated")) {
			pruned = true
		}
	}
	if !pruned {
		t.Fatal("the oversized tool result should have been pruned in place")
	}
}
