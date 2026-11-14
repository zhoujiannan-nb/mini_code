package session

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// ---- ageOutCallArgs ----

func TestAgeOutCallArgsOnlyBigWriteEdit(t *testing.T) {
	big := strings.Repeat("x", 3000)
	if _, ok := ageOutCallArgs("read_file", `{"path":"a.txt","old_text":"`+big+`"}`); ok {
		t.Fatal("read_file must never age")
	}
	if _, ok := ageOutCallArgs("write_file", `{"path":"a.txt","content":"short"}`); ok {
		t.Fatal("below-threshold args must stay")
	}
	for _, bad := range []string{"not json", `{"content":"` + big + `"}`, `{]`} {
		if _, ok := ageOutCallArgs("write_file", bad); ok {
			t.Fatalf("must not age unparsable/anchor-less args: %q", bad)
		}
	}
	out, ok := ageOutCallArgs("write_file", `{"path":"dir/a.txt","content":"`+big+`"}`)
	if !ok {
		t.Fatal("big write must age")
	}
	var v map[string]any
	if json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("replacement is not valid JSON: %q", out)
	}
	if v["file_path"] != "dir/a.txt" || !strings.Contains(v["content"].(string), "aged out") {
		t.Fatalf("summary lost the anchor: %s", out)
	}
	// Idempotent: the summary sits far below the threshold.
	if _, ok := ageOutCallArgs("write_file", out); ok {
		t.Fatal("aged args must not re-age")
	}
	outE, ok := ageOutCallArgs("edit_file", `{"path":"a.txt","old_text":"`+strings.Repeat("o", 2500)+`","new_text":"`+strings.Repeat("n", 2500)+`"}`)
	if !ok || !strings.Contains(outE, "replaced 2500 chars") {
		t.Fatalf("edit summary wrong: %q", outE)
	}
	// The dsh names and argument keys age the same way.
	if _, ok := ageOutCallArgs("write", `{"file_path":"a.txt","content":"`+big+`"}`); !ok {
		t.Fatal("big dsh-name write must age")
	}
	outE2, ok := ageOutCallArgs("edit", `{"file_path":"a.txt","old_string":"`+strings.Repeat("o", 2500)+`","new_string":"`+strings.Repeat("n", 2500)+`"}`)
	if !ok || !strings.Contains(outE2, "replaced 2500 chars") {
		t.Fatalf("dsh-name edit summary wrong: %q", outE2)
	}
}

// ---- ageCallArgs ----

func TestAgeCallArgsWalksTrailingBlock(t *testing.T) {
	big := strings.Repeat("y", 3000)
	msgs := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "c1", Function: provider.FuncCall{Name: "write_file", Arguments: `{"path":"a","content":"` + big + `"}`}},
		}},
	}
	msgs[1].ToolCalls[0].Function.Arguments = `{"path":"a","content":"` + big + `"}`
	if !ageCallArgs(msgs, "c1", `{"path":"a","content":"[aged out]"}`) {
		t.Fatal("call not aged")
	}
	if !strings.Contains(msgs[1].ToolCalls[0].Function.Arguments, "aged out") {
		t.Fatal("history not mutated")
	}
	if ageCallArgs(msgs, "c1", `{"path":"a","content":"[aged out]"}`) {
		t.Fatal("second pass must be a no-op")
	}
	if ageCallArgs(msgs, "nope", "x") {
		t.Fatal("unknown call id reported aged")
	}
	// A call sitting before an older user boundary is out of the trailing
	// block: aging only ever touches the batch that just completed.
	msgs2 := []provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "c1", Function: provider.FuncCall{Name: "write_file", Arguments: `{"path":"a","content":"` + big + `"}`}},
		}},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: "hi"},
	}
	if ageCallArgs(msgs2, "c1", "x") {
		t.Fatal("must not reach past the user boundary")
	}
}

// ---- loop-level aging ----

type bigWriteTool struct{ fail bool }

func (bigWriteTool) Name() string        { return "write_file" }
func (bigWriteTool) Description() string { return "fake write" }
func (bigWriteTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t bigWriteTool) IsHidden() bool { return false }
func (t bigWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	if t.fail {
		return tools.NewTextResult("Error: disk on fire"), nil
	}
	return tools.NewTextResult("Successfully wrote 3000 bytes to a.txt"), nil
}

// TestLoopAgesSuccessfulBigWrite asserts the next model request carries the
// summary instead of the payload, and the ledger keeps the full arguments plus
// a compaction/argage shadow event.
func TestLoopAgesSuccessfulBigWrite(t *testing.T) {
	var requests int
	var secondBody string
	handler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		b := make([]byte, r.ContentLength)
		for off := 0; off < len(b); {
			n, _ := r.Body.Read(b[off:])
			if n <= 0 {
				break
			}
			off += n
		}
		if requests == 2 {
			secondBody = string(b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\",\"content\":\"`+strings.Repeat("Z", 3000)+`\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(bigWriteTool{})
	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(bigWriteTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false
	loop.argAgingOn = true
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Run: err=%v", err)
	}
	if !strings.Contains(secondBody, "aged out") || strings.Contains(secondBody, strings.Repeat("Z", 3000)) {
		t.Fatalf("second request must carry the summary, not the payload")
	}
	var aged int
	for _, e := range rec.events {
		if e.typ == EventCompactionArgAge {
			aged++
			if e.payload["call_id"] != "call_w1" || e.payload["chars_before"].(int) < 3000 {
				t.Fatalf("argage event payload: %v", e.payload)
			}
		}
		if e.typ == EventToolCall && e.payload["call_id"] == "call_w1" {
			if len([]rune(e.payload["args"].(string))) < 3000 {
				t.Fatal("ledger tool/call row lost the full arguments")
			}
		}
	}
	if aged != 1 {
		t.Fatalf("argage events = %d, want 1", aged)
	}
}

// TestLoopKeepsFailedWriteArgs pins the safety rail: an errored call keeps its
// full arguments so the retry has something to work with.
func TestLoopKeepsFailedWriteArgs(t *testing.T) {
	var requests int
	var secondBody string
	handler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		b := make([]byte, r.ContentLength)
		for off := 0; off < len(b); {
			n, _ := r.Body.Read(b[off:])
			if n <= 0 {
				break
			}
			off += n
		}
		if requests == 2 {
			secondBody = string(b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w2","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\",\"content\":\"`+strings.Repeat("Q", 3000)+`\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(bigWriteTool{fail: true})
	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(bigWriteTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false
	loop.argAgingOn = true
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil {
		t.Fatalf("Run: err=%v", err)
	}
	if !strings.Contains(secondBody, strings.Repeat("Q", 3000)) {
		t.Fatal("a failed write must keep its arguments")
	}
	for _, e := range rec.events {
		if e.typ == EventCompactionArgAge {
			t.Fatalf("no argage event expected on failure: %v", e.payload)
		}
	}
}
