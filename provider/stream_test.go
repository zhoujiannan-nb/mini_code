package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func newTestStreamServer(handler http.HandlerFunc) (*httptest.Server, *ModelClient) {
	srv := httptest.NewServer(handler)
	mc := &ModelClient{
		provider:   NewVLLMProvider(srv.URL, "test-key", "test-model", 8192, 32768, 0.7, 1.0, "", ""),
		httpClient: srv.Client(),
	}
	return srv, mc
}

func sseEvent(data string) string {
	return "data: " + data + "\n\n"
}

func TestChatStreamAggregatesContent(t *testing.T) {
	var sawStream bool
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := decodeJSON(r, &req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !req.Stream {
			t.Error("stream flag not set")
		}
		sawStream = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"reasoning_content":"让我想想"}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"content":"你好"}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"content":"，世界"}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"reasoning_tokens":3}}`))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	var (
		mu     sync.Mutex
		chunks []StreamChunk
	)
	resp, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, func(c StreamChunk) {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawStream {
		t.Error("server never received stream=true")
	}
	if resp.Content != "你好，世界" {
		t.Errorf("content = %q, want 你好，世界", resp.Content)
	}
	if resp.ReasoningContent != "让我想想" {
		t.Errorf("reasoning = %q, want 让我想想", resp.ReasoningContent)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", resp.FinishReason)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 10 || resp.Usage.ReasoningTokens != 3 {
		t.Errorf("usage = %+v, want prompt=10 reasoning=3", resp.Usage)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if chunks[0].Content != "" || chunks[0].ReasoningContent != "让我想想" {
		t.Errorf("chunk0 = %+v, want reasoning only", chunks[0])
	}
	if chunks[1].Content != "你好" || chunks[1].ReasoningContent != "让我想想" {
		t.Errorf("chunk1 = %+v", chunks[1])
	}
	if chunks[2].Content != "你好，世界" {
		t.Errorf("chunk2 = %+v", chunks[2])
	}
}

func TestChatStreamToolCalls(t *testing.T) {
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}`))
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	resp, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "read main.go"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "read_file" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("arguments = %q", tc.Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", resp.FinishReason)
	}
}

func TestChatStreamStreamError(t *testing.T) {
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseEvent(`{"choices":[{"delta":{"content":"partial"}}]}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic("abort stream")
	})
	defer srv.Close()

	_, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error for broken stream")
	}
}

func TestChatStreamEmptyStream(t *testing.T) {
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	})
	defer srv.Close()

	_, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpectedly") {
		t.Fatalf("err = %v, want stream ended unexpectedly", err)
	}
}

func TestChatStreamTruncatedToolCall(t *testing.T) {
	truncated := `{"path": "main.go"`
	first := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": ""}}}}}}}
	second := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": truncated}}}}}}}
	srv, mc := newTestStreamServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		b1, _ := json.Marshal(first)
		b2, _ := json.Marshal(second)
		fmt.Fprint(w, sseEvent(string(b1)))
		fmt.Fprint(w, sseEvent(string(b2)))
	})
	defer srv.Close()

	_, err := mc.ChatStream(context.Background(), []Message{{Role: "user", Content: "write main.go"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error for stream cut off mid tool call")
	}
	if !strings.Contains(err.Error(), "finish_reason") {
		t.Fatalf("err = %v, want mention of missing finish_reason", err)
	}
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
