package session

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// editTool is a fake edit_file that always succeeds, so the churn guard
// counts every call.
type editTool struct{}

func (editTool) Name() string        { return "edit_file" }
func (editTool) Description() string { return "fake edit" }
func (editTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t editTool) IsHidden() bool { return false }
func (t editTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult("Successfully edited a.txt"), nil
}

// TestEditChurnNudgeAfterThreshold drives four distinct successful edits to
// the same path and asserts the fifth model request carries the churn nudge.
// The args differ per turn on purpose: identical calls would be caught by the
// repeat-stall guard instead, and this test must exercise only the tally.
func TestEditChurnNudgeAfterThreshold(t *testing.T) {
	var requests int
	var fifthBody string
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
		w.Header().Set("Content-Type", "text/event-stream")
		if requests <= editNudgeAt {
			call := fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e%d","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"a.txt\",\"old_text\":\"o%d\",\"new_text\":\"n%d\"}"}}]},"finish_reason":"tool_calls"}]}`, requests, requests, requests)
			writeSSEChunk(w, call)
		} else {
			if requests == 5 {
				fifthBody = string(b)
			}
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(editTool{})
	loop := NewAgentLoop(client, 8, reg, []provider.ToolSchema{tools.ToSchema(editTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Run: err=%v", err)
	}
	if requests < editNudgeAt+1 {
		t.Fatalf("requests = %d, expected the nudge round to run", requests)
	}
	if !strings.Contains(fifthBody, "edited 4 separate times") {
		t.Fatalf("fifth request must carry the churn nudge, got:\n%s", fifthBody)
	}
}

// TestFullWriteResetsEditTally drives three edits, then a full write_file to
// the same path, then three more edits — 6 edits total but no nudge, because
// the rewrite reset the tally.
func TestFullWriteResetsEditTally(t *testing.T) {
	var requests int
	var bodies []string
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
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case requests <= 3:
			writeSSEChunk(w, fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e%d","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"a.txt\",\"old_text\":\"o%d\",\"new_text\":\"n%d\"}"}}]},"finish_reason":"tool_calls"}]}`, requests, requests, requests))
		case requests == 4:
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a.txt\",\"content\":\"whole rewrite\"}"}}]},"finish_reason":"tool_calls"}]}`)
		case requests <= 7:
			writeSSEChunk(w, fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e%d","type":"function","function":{"name":"edit_file","arguments":"{\"path\":\"a.txt\",\"old_text\":\"x%d\",\"new_text\":\"y%d\"}"}}]},"finish_reason":"tool_calls"}]}`, requests, requests, requests))
		default:
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(editTool{})
	reg.Register(writeToolReset{})
	loop := NewAgentLoop(client, 10, reg, []provider.ToolSchema{tools.ToSchema(editTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil {
		t.Fatalf("Run: err=%v", err)
	}
	for i, b := range bodies {
		if strings.Contains(b, "separate times") {
			t.Fatalf("request %d must not carry a churn nudge after the rewrite reset", i+1)
		}
	}
}

// writeToolReset is a fake write_file that succeeds, used by the reset test.
type writeToolReset struct{}

func (writeToolReset) Name() string        { return "write_file" }
func (writeToolReset) Description() string { return "fake write" }
func (writeToolReset) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t writeToolReset) IsHidden() bool { return false }
func (t writeToolReset) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult("Successfully wrote 12 bytes to a.txt"), nil
}
