package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// slowTool is a fake tool that signals the test when execution starts and
// then blocks until released. It lets the test observe exactly what the loop
// had persisted while the tool was running.
type slowTool struct {
	started chan struct{}
	release chan struct{}
	calls   int64
}

func (s *slowTool) Name() string          { return "slowtool" }
func (s *slowTool) Description() string   { return "slow test tool" }
func (s *slowTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (s *slowTool) IsHidden() bool { return false }

func (s *slowTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	atomic.AddInt64(&s.calls, 1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return tools.NewTextResult("slow done"), nil
}

// TestPersistBeforeToolExecution verifies that the assistant reply carrying a
// tool call is persisted (onTurn hook) BEFORE tool execution starts. Before
// the fix the hook only fired after handleToolCalls returned, so a web
// refresh during a long tool call lost the reply from the history view.
func TestPersistBeforeToolExecution(t *testing.T) {
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First turn: the model replies with text and a slow tool call.
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"I will check the log"}}]}`)
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_slow","type":"function","function":{"name":"slowtool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			// Second turn: final answer.
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"all done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	slow := &slowTool{started: make(chan struct{}, 1), release: make(chan struct{})}
	reg := tools.NewToolRegistry()
	reg.Register(slow)

	var mu sync.Mutex
	var snapshots [][]provider.Message
	turnHook := func(turn int, messages []provider.Message) {
		snap := make([]provider.Message, len(messages))
		copy(snap, messages)
		mu.Lock()
		snapshots = append(snapshots, snap)
		mu.Unlock()
	}

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
	defer client.Close()

	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(slow)}, nil, nil, nil, turnHook)
	loop.compactionOn = false

	type runResult struct {
		res *LoopResult
		err error
	}
	resCh := make(chan runResult, 1)
	go func() {
		res, err := loop.Run(context.Background(), "system prompt", "check the log", nil)
		resCh <- runResult{res, err}
	}()

	// Wait until the slow tool has started executing.
	select {
	case <-slow.started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for tool execution to start")
	}

	// While the tool is still running, the latest persistence snapshot must
	// already contain the assistant reply with its tool call.
	mu.Lock()
	if len(snapshots) == 0 {
		mu.Unlock()
		t.Fatal("no persistence hook fired before tool execution")
	}
	last := snapshots[len(snapshots)-1]
	found := false
	for _, m := range last {
		if m.Role == "assistant" && m.Content == "I will check the log" && len(m.ToolCalls) == 1 {
			found = true
		}
	}
	mu.Unlock()
	if !found {
		t.Fatalf("assistant reply with tool call was NOT persisted before tool execution started; last snapshot has %d messages", len(last))
	}

	// Release the tool and let the loop finish.
	close(slow.release)
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("Run returned error: %v", r.err)
		}
		if !r.res.Success {
			t.Fatalf("expected success, got error: %q", r.res.Error)
		}
		if r.res.Content != "all done" {
			t.Fatalf("expected final content %q, got %q", "all done", r.res.Content)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the loop to finish")
	}
	if got := atomic.LoadInt64(&slow.calls); got != 1 {
		t.Fatalf("expected the slow tool to run exactly once, got %d", got)
	}
}
