package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// sseBatchChunk renders one SSE chunk carrying two complete tool calls
// (indices 0 and 1) plus the tool_calls finish reason.
func sseBatchChunk(name0, args0, name1, args1 string) string {
	return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"` + name0 + `","arguments":"` + args0 + `"}},{"index":1,"id":"call_b","type":"function","function":{"name":"` + name1 + `","arguments":"` + args1 + `"}}]},"finish_reason":"tool_calls"}]}`
}

// TestNudgeOnRepeatedBatch: the model re-issues the same two-call batch on
// three consecutive turns; the loop must inject exactly one corrective
// nudge after the third repetition, and the task still completes when the
// model recovers.
func TestNudgeOnRepeatedBatch(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 3 {
			writeSSEChunk(w, sseBatchChunk("exec", `{\"command\":\"ls\"}`, "exec", `{\"command\":\"dir\"}`))
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

// TestNudgeOnReorderedBatch: the same two calls re-issued with the order
// shuffled between turns is the same stall — the batch signature is
// order-independent, so the nudge must fire.
func TestNudgeOnReorderedBatch(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1, 3:
			writeSSEChunk(w, sseBatchChunk("exec", `{\"command\":\"ls\"}`, "exec", `{\"command\":\"dir\"}`))
		case 2:
			writeSSEChunk(w, sseBatchChunk("exec", `{\"command\":\"dir\"}`, "exec", `{\"command\":\"ls\"}`))
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
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 1 {
		t.Fatalf("expected exactly 1 first-level nudge for the reordered batch, got %d", got)
	}
}

// TestNoNudgeOnDifferentBatches: a batch that changes between turns is
// progress, not a stall — no nudge may be injected even when one member of
// the batch is repeated.
func TestNoNudgeOnDifferentBatches(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1, 3:
			writeSSEChunk(w, sseBatchChunk("exec", `{\"command\":\"ls\"}`, "exec", `{\"command\":\"dir\"}`))
		case 2:
			writeSSEChunk(w, sseBatchChunk("exec", `{\"command\":\"ls\"}`, "exec", `{\"command\":\"pwd\"}`))
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
		t.Fatalf("expected no nudge for changing batches, got %d", got)
	}
}

// TestBatchSignatureOrderIndependent: the batch signature must not depend on
// the order of the calls, and must differ when any call's arguments differ.
func TestBatchSignatureOrderIndependent(t *testing.T) {
	ls := provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"ls"}`}}
	dir := provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"dir"}`}}
	pwd := provider.ToolCall{Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"pwd"}`}}

	a := batchSignature([]provider.ToolCall{ls, dir})
	b := batchSignature([]provider.ToolCall{dir, ls})
	c := batchSignature([]provider.ToolCall{ls, pwd})
	if a != b {
		t.Fatalf("order must not matter: %q != %q", a, b)
	}
	if a == c {
		t.Fatal("different arguments must not share a batch signature")
	}
	// A one-call batch keeps the single-call signature (back-compat).
	if batchSignature([]provider.ToolCall{ls}) != toolCallSignature(ls) {
		t.Fatal("one-call batch signature must equal the call signature")
	}
}
