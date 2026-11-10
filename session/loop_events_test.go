package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// echoTool is a trivial tool for the ledger tests: it returns a fixed string.
type echoTool struct{}

func (echoTool) Name() string        { return "echotool" }
func (echoTool) Description() string { return "echo test tool" }
func (echoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (echoTool) IsHidden() bool { return false }
func (echoTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult("echo-result"), nil
}

// eventRecorder collects ledger events in order.
type eventRecorder struct {
	mu     sync.Mutex
	events []struct {
		typ     string
		turn    int
		payload map[string]any
	}
}

func (r *eventRecorder) sink(typ string, turn int, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, struct {
		typ     string
		turn    int
		payload map[string]any
	}{typ, turn, payload})
}

func (r *eventRecorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.typ
	}
	return out
}

func newLedgerClient(t *testing.T, handler http.HandlerFunc) (*provider.ModelClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       srv.URL,
		APIKey:        "test",
		ModelName:     "test-model",
		MaxTokens:     100,
		ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	return client, func() { client.Close(); srv.Close() }
}

// TestLoopEventLedger runs one tool round followed by a final answer and
// asserts the ledger recorded the full fact sequence in order:
// turn/start → assistant/message(with tool_calls) → tool/call → tool/result →
// turn/start → assistant/message → task/end(completed).
func TestLoopEventLedger(t *testing.T) {
	var requests int
	srvHandler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"checking"}}]}`)
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e1","type":"function","function":{"name":"echotool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"all done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, srvHandler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(echoTool{})

	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(echoTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)

	res, err := loop.Run(context.Background(), "sys", "do the thing", nil)
	if err != nil || !res.Success {
		t.Fatalf("Run: err=%v success=%v", err, res != nil && res.Success)
	}

	got := strings.Join(rec.types(), ",")
	want := strings.Join([]string{
		EventTurnStart, EventAssistantMessage, EventToolCall, EventToolResult,
		EventTurnStart, EventAssistantMessage, EventTaskEnd,
	}, ",")
	if got != want {
		t.Fatalf("event sequence =\n  %s\nwant\n  %s", got, want)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	asst := rec.events[1].payload
	calls, _ := asst["tool_calls"].([]map[string]any)
	if len(calls) != 1 || calls[0]["name"] != "echotool" || calls[0]["id"] != "call_e1" {
		t.Fatalf("assistant/message tool_calls = %+v, want the echotool call", asst["tool_calls"])
	}
	tr := rec.events[3].payload
	if tr["call_id"] != "call_e1" || tr["name"] != "echotool" || tr["ok"] != true || tr["result"] != "echo-result" {
		t.Fatalf("tool/result payload = %+v", tr)
	}
	if _, ok := tr["duration_ms"]; !ok {
		t.Fatalf("tool/result payload missing duration_ms: %+v", tr)
	}
	end := rec.events[6].payload
	if end["status"] != "completed" || end["turns"] != 2 {
		t.Fatalf("task/end payload = %+v, want completed/2", end)
	}
}

// TestLoopEventLedgerInterrupted cancels the task before its first turn: the
// ledger must still close the run with task/end(interrupted) — an open run
// in the ledger is exactly what the UI resync looks for.
func TestLoopEventLedgerInterrupted(t *testing.T) {
	client, closeFn := newLedgerClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("LLM must not be called after cancel")
	})
	defer closeFn()

	loop := NewAgentLoop(client, 5, nil, nil, nil, nil, nil, nil)
	loop.compactionOn = false
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := loop.Run(ctx, "sys", "hello", nil)
	if err != nil || res == nil || !res.Interrupted {
		t.Fatalf("Run: err=%v res=%+v, want interrupted", err, res)
	}

	got := strings.Join(rec.types(), ",")
	want := EventTurnStart + "," + EventTaskEnd
	if got != want {
		t.Fatalf("event sequence = %s, want %s", got, want)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if p := rec.events[1].payload; p["status"] != "interrupted" {
		t.Fatalf("task/end payload = %+v, want interrupted", p)
	}
}
