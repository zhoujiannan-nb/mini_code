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

const (
	nudgeFirstPhrase  = "Stop repeating it"
	nudgeSecondPhrase = "STILL repeating the same"
)

// sseToolCallChunk renders one SSE chunk carrying a complete tool call.
func sseToolCallChunk(name, args, finish string) string {
	return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"` + name + `","arguments":"` + args + `"}}]},"finish_reason":"` + finish + `"}]}`
}

// sseTextChunk renders one SSE chunk carrying final text content.
func sseTextChunk(content, finish string) string {
	return `{"choices":[{"delta":{"content":"` + content + `"},"finish_reason":"` + finish + `"}]}`
}

// countNudges counts how many messages in messages carry a stall nudge.
func countNudges(messages []provider.Message, phrase string) int {
	n := 0
	for _, m := range messages {
		if strings.Contains(m.GetText(), phrase) {
			n++
		}
	}
	return n
}

// TestNudgeOnRepeatedToolCall: the model repeats the exact same tool call on
// three consecutive turns; the loop must inject exactly one corrective
// nudge after the third repetition, and the task still completes when the
// model recovers.
func TestNudgeOnRepeatedToolCall(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 3 {
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
		} else {
			writeSSEChunk(w, sseTextChunk("done", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 1 {
		t.Fatalf("expected exactly 1 first-level nudge, got %d", got)
	}
	if countNudges(res.Messages, nudgeSecondPhrase) != 0 {
		t.Fatal("expected no second-level nudge (streak never reached 5)")
	}
}

// TestNoNudgeOnDifferentCalls: alternating different tool calls is progress,
// not a stall — no nudge may be injected.
func TestNoNudgeOnDifferentCalls(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
		case 2:
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"dir\"}`, "tool_calls"))
		case 3:
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
		default:
			writeSSEChunk(w, sseTextChunk("done", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 0 {
		t.Fatalf("expected no nudge for non-repeating calls, got %d", got)
	}
}

// TestNudgeEscalatesOnce: the model ignores the first nudge and keeps
// repeating the same call; a second, firmer nudge must appear — and no
// third one.
func TestNudgeEscalatesOnce(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 6 {
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
		} else {
			writeSSEChunk(w, sseTextChunk("done", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg
	loop.maxTurns = 10 // 6 repeated calls + 1 final answer

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 1 {
		t.Fatalf("expected exactly 1 first-level nudge, got %d", got)
	}
	if got := countNudges(res.Messages, nudgeSecondPhrase); got != 1 {
		t.Fatalf("expected exactly 1 second-level nudge, got %d", got)
	}
}

// TestToolCallSignatureCanonical: semantically identical arguments with
// different key order or whitespace must produce the same signature;
// different arguments must not.
func TestToolCallSignatureCanonical(t *testing.T) {
	a := toolCallSignature(provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"ls","timeout":5}`}})
	b := toolCallSignature(provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"timeout": 5, "command": "ls"}`}})
	c := toolCallSignature(provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"dir","timeout":5}`}})
	d := toolCallSignature(provider.ToolCall{Function: provider.FuncCall{Name: "read_file", Arguments: `{"command":"ls","timeout":5}`}})
	if a != b {
		t.Fatalf("canonicalization failed: %q != %q", a, b)
	}
	if a == c {
		t.Fatal("different arguments must not share a signature")
	}
	if a == d {
		t.Fatal("different tool names must not share a signature")
	}
}

// fakeTool is a minimal tool stub for loop tests.
type fakeTool struct {
	name   string
	result string
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Description() string {
	return "test tool"
}
func (f *fakeTool) IsHidden() bool { return false }
func (f *fakeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (f *fakeTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult(f.result), nil
}
