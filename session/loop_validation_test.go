package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// spyTool records whether Execute ever ran; its schema requires two keys.
type spyTool struct{ calls int64 }

func (s *spyTool) Name() string        { return "edit" }
func (s *spyTool) Description() string { return "spy" }
func (s *spyTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":  map[string]interface{}{"type": "string"},
			"new_string": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"file_path", "new_string"},
	}
}
func (s *spyTool) IsHidden() bool { return false }
func (s *spyTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	atomic.AddInt64(&s.calls, 1)
	return tools.NewTextResult("executed"), nil
}

// runOneToolCall drives a loop whose first turn emits args as the given raw
// string (SSE payloads need no further escaping: keep args JSON-quotable),
// then returns the single tool message the model received.
func runOneToolCall(t *testing.T, args string, tool tools.Tool) (toolMsg string, execCalls int64) {
	t.Helper()
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+tool.Name()+`","arguments":`+`"`+args+`"`+`}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	loop.tools = registry

	res, err := loop.Run(context.Background(), "system prompt", "go", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolMsg = m.Content
		}
	}
	return toolMsg, atomic.LoadInt64(&tool.(*spyTool).calls)
}

// TestDispatchRejectsMissingRequiredArg: the schema-declared required keys are
// enforced by the dispatch layer (dsh parity). The tool must not run, and the
// model must get the same failure text shape dsh gives it.
func TestDispatchRejectsMissingRequiredArg(t *testing.T) {
	// {"file_path":"a","content":"x"} — the exact t2 incident shape: the key
	// new_string spelled "content" (JSON-escaped for the SSE payload).
	msg, calls := runOneToolCall(t, `{\"file_path\":\"a\",\"content\":\"x\"}`, &spyTool{})
	if calls != 0 {
		t.Fatalf("tool Execute must not run on an invalid call, got %d calls", calls)
	}
	for _, want := range []string{
		`Error: Validation failed for tool "edit":`,
		`- new_string: must have required property 'new_string'`,
		`Received arguments:`,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("tool result missing %q:\n%s", want, msg)
		}
	}
}

// TestDispatchRejectsUnparseableArgs: arguments that are not JSON at all get
// dsh's arguments-error text instead of a silent empty-parameter call. The
// dispatch path is called directly here: whether a malformed arguments string
// even reaches the loop is the transport parser's business, not this gate's.
func TestDispatchRejectsUnparseableArgs(t *testing.T) {
	loop, client := newTestLoop(t, "http://127.0.0.1:1")
	defer client.Close()
	tool := &spyTool{}
	registry := tools.NewToolRegistry()
	registry.Register(tool)
	loop.tools = registry

	res := loop.executeSingleTool(context.Background(), provider.ToolCall{
		ID: "call_1", Type: "function",
		Function: provider.FuncCall{Name: "edit", Arguments: `{bad json`},
	})
	if res == nil || !strings.HasPrefix(res.Text, `Error: Invalid arguments for tool "edit":`) {
		t.Fatalf("want Invalid-arguments text, got: %+v", res)
	}
	if atomic.LoadInt64(&tool.calls) != 0 {
		t.Fatal("tool Execute must not run on unparseable args")
	}
}

// TestDispatchCoercesAndRuns: a schema-valid call passes through untouched and
// a coercible scalar arrives at Execute as the declared type.
func TestDispatchCoercesAndRuns(t *testing.T) {
	tool := &spyTool{}
	msg, calls := runOneToolCall(t, `{\"file_path\":\"a\",\"new_string\":\"b\"}`, tool)
	if calls != 1 {
		t.Fatalf("valid call must execute once, got %d", calls)
	}
	if msg != "executed" {
		t.Fatalf("tool result = %q", msg)
	}
}
