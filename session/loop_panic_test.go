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

type panicTool struct{}

func (panicTool) Name() string        { return "boom" }
func (panicTool) Description() string { return "always panics" }
func (panicTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (panicTool) IsHidden() bool { return false }
func (panicTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	panic("simulated tool bug: index out of range")
}

func TestToolPanicContained(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"boom","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	registry := tools.NewToolRegistry()
	registry.Register(panicTool{})
	loop.tools = registry

	res, err := loop.Run(context.Background(), "system prompt", "use the boom tool", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success after contained panic, got error: %q", res.Error)
	}
	if res.Content != "done" {
		t.Fatalf("expected content %q, got %q", "done", res.Content)
	}
	found := false
	for _, m := range res.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "crashed (panic:") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a tool message containing 'crashed (panic:' in the conversation")
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("expected exactly 2 LLM requests (tool turn + final turn), got %d", got)
	}
}

func TestToolPanicDoesNotPoisonBatch(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"boom","arguments":"{}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"boom","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()

	registry := tools.NewToolRegistry()
	registry.Register(panicTool{})
	loop.tools = registry

	res, err := loop.Run(context.Background(), "system prompt", "batch two boom calls", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	toolMsgs := 0
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolMsgs++
			if !strings.Contains(m.Content, "crashed (panic:") {
				t.Fatalf("expected contained-panic error text, got %q", m.Content)
			}
		}
	}
	if toolMsgs != 2 {
		t.Fatalf("expected 2 tool result messages (one per call), got %d", toolMsgs)
	}
}
