package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

type probeTool struct {
	mu         sync.Mutex
	chunks     []string
	plainCalls int
	streamCall int
}

func (p *probeTool) Name() string        { return "probetool" }
func (p *probeTool) Description() string { return "fake streamable tool" }
func (p *probeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (p *probeTool) IsHidden() bool { return false }

func (p *probeTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	p.mu.Lock()
	p.plainCalls++
	p.mu.Unlock()
	return tools.NewTextResult("probe final"), nil
}

func (p *probeTool) ExecuteStreamed(ctx context.Context, params map[string]interface{}, sink func(string)) (*tools.ToolResult, error) {
	p.mu.Lock()
	p.streamCall++
	p.mu.Unlock()
	sink("chunk-one")
	sink("chunk-two")
	return tools.NewTextResult("probe final"), nil
}

func (p *probeTool) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.plainCalls, p.streamCall
}

func probeServer(t *testing.T) *httptest.Server {
	t.Helper()
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"running it"}}]}`)
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_probe","type":"function","function":{"name":"probetool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"all done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type probeRig struct {
	loop     *AgentLoop
	probe    *probeTool
	mu       sync.Mutex
	ended    []string
	observed []string
}

func (r *probeRig) feed(name, chunk string) {
	r.mu.Lock()
	r.observed = append(r.observed, name+"|"+chunk)
	r.mu.Unlock()
}

func (r *probeRig) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.observed...), append([]string(nil), r.ended...)
}

func runProbe(t *testing.T, wireFeed bool) *probeRig {
	t.Helper()
	srv := probeServer(t)
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
	t.Cleanup(func() { client.Close() })

	rig := &probeRig{probe: &probeTool{}}
	reg := tools.NewToolRegistry()
	reg.Register(rig.probe)

	rig.loop = NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(rig.probe)},
		nil,
		func(name string, params map[string]interface{}, result string) {
			rig.mu.Lock()
			rig.ended = append(rig.ended, name+"|"+result)
			rig.mu.Unlock()
		},
		nil, nil)
	rig.loop.compactionOn = false
	if wireFeed {
		rig.loop.SetOnToolOutput(rig.feed)
	}

	res, err := rig.loop.Run(context.Background(), "system prompt", "run the probe", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	return rig
}

func TestStreamedToolFeedsOutputToLoop(t *testing.T) {
	rig := runProbe(t, true)

	plain, streamed := rig.probe.counts()
	if streamed != 1 || plain != 0 {
		t.Fatalf("expected the streamed entry point exactly once, got plain=%d streamed=%d", plain, streamed)
	}

	feed, ended := rig.snapshot()
	want := []string{"probetool|chunk-one", "probetool|chunk-two"}
	if strings.Join(feed, ",") != strings.Join(want, ",") {
		t.Fatalf("live feed mismatch:\n got %v\nwant %v", feed, want)
	}

	if len(ended) != 1 || ended[0] != "probetool|probe final" {
		t.Fatalf("expected one tool_end with the plain result, got %v", ended)
	}
}

func TestStreamedToolWithoutListenerUsesRegistry(t *testing.T) {
	rig := runProbe(t, false)

	plain, streamed := rig.probe.counts()
	if plain != 1 || streamed != 0 {
		t.Fatalf("expected the registry path when no listener is wired, got plain=%d streamed=%d", plain, streamed)
	}

	feed, ended := rig.snapshot()
	if len(feed) != 0 {
		t.Fatalf("expected no live output without a listener, got %v", feed)
	}
	if len(ended) != 1 || ended[0] != "probetool|probe final" {
		t.Fatalf("expected one tool_end with the plain result, got %v", ended)
	}
}
