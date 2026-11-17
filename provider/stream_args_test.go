package provider

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestChatStreamExposesToolArgsProgress(t *testing.T) {
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write_file","arguments":""}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"D:\\\\x.txt\",\"content\":\"第一行"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\n第二行\"}"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	var snaps [][]StreamToolCallProgress
	resp, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "write it"}}, nil,
		func(c StreamChunk) { snaps = append(snaps, c.ToolCalls) })
	if err != nil {
		t.Fatal(err)
	}

	if len(snaps) != 3 {
		t.Fatalf("chunks = %d, want 3", len(snaps))
	}
	if snaps[0][0].Args != "" {
		t.Errorf("snapshot0 args = %q, want empty (name arrives before any argument)", snaps[0][0].Args)
	}
	if got := snaps[1][0].Args; got != `{"path":"D:\\x.txt","content":"第一行` {
		t.Errorf("snapshot1 args = %q, want the first argument piece", got)
	}
	if got, want := snaps[2][0].Args, resp.ToolCalls[0].Function.Arguments; got != want {
		t.Errorf("snapshot2 args = %q, want the complete %q", got, want)
	}
	if snaps[0][0].Name != "write_file" || snaps[0][0].ID != "call_w" || snaps[0][0].Index != 0 {
		t.Errorf("snapshot0 = %+v, want name/id/index from the first delta", snaps[0][0])
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Arguments != `{"path":"D:\\x.txt","content":"第一行\n第二行"}` {
		t.Errorf("final tool calls = %+v", resp.ToolCalls)
	}
}

func TestChatStreamArgsProgressParallelCalls(t *testing.T) {
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"a\"}"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"b\"}"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	var last []StreamToolCallProgress
	_, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "two files"}}, nil,
		func(c StreamChunk) { last = c.ToolCalls })
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 {
		t.Fatalf("last snapshot = %+v, want two in-flight calls", last)
	}
	if last[0].Args != `{"path":"a"}` || last[1].Args != `{"path":"b"}` || last[1].Index != 1 {
		t.Errorf("last snapshot = %+v, want each call's own args", last)
	}
}
