package session

import (
	"context"
	"encoding/json"
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

// jsonFragments cuts s into fixed-size pieces that concatenate back to s.
func jsonFragments(s string, size int) []string {
	var out []string
	for len(s) > 0 {
		n := size
		if n > len(s) {
			n = len(s)
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// argsSSEServer answers the first turn with a write_file call whose arguments
// arrive in many small deltas (that is what a long payload looks like on the
// wire), then a final reply on the second turn.
func argsSSEServer(t *testing.T, frags []string) *httptest.Server {
	t.Helper()
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"probetool","arguments":""}}]}}]}`)
			for _, f := range frags {
				payload := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + jsonString(f) + `}}]}}]}`
				writeSSEChunk(w, payload)
			}
			writeSSEChunk(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jsonString quotes a string for embedding in a JSON payload.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func newArgsLoop(t *testing.T, srvURL string, reg *tools.ToolRegistry) *AgentLoop {
	t.Helper()
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: srvURL, APIKey: "test",
		ModelName: "test-model", MaxTokens: 100, ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	loop := NewAgentLoop(client, 5, reg, nil, nil, nil, nil, nil)
	loop.compactionOn = false
	return loop
}

// TestArgsStreamedWhileModelIsTyping is the feature test: a long tool-call
// payload must reach the UI BEFORE tool_start, as a growing prefix of the final
// arguments. Everything the feed emits has to be a prefix of the complete value,
// in increasing length order, and the tail may be left to tool_start (which
// carries the whole arguments string) - that is the documented contract.
func TestArgsStreamedWhileModelIsTyping(t *testing.T) {
	full := `{"content":"` + strings.Repeat("abcdefghij", 300) + `"}` // ~3 KB
	frags := jsonFragments(full, 400)
	if len(frags) < 6 {
		t.Fatalf("test setup: expected many argument deltas, got %d", len(frags))
	}

	srv := argsSSEServer(t, frags)
	probe := &probeTool{}
	reg := tools.NewToolRegistry()
	reg.Register(probe)
	loop := newArgsLoop(t, srv.URL, reg)

	var mu sync.Mutex
	var got []string
	loop.SetOnToolArgs(func(name string, argsSoFar string) {
		mu.Lock()
		got = append(got, name+"\x00"+argsSoFar)
		mu.Unlock()
	})

	res, err := loop.Run(context.Background(), "sys", "write it", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("expected several live argument events (the whole point of the feature), got %d", len(got))
	}
	prev := 0
	for i, e := range got {
		name, args, _ := strings.Cut(e, "\x00")
		if name != "probetool" {
			t.Fatalf("event %d tagged %q, want the tool being generated", i, name)
		}
		if !strings.HasPrefix(full, args) {
			brief := args
			if len(brief) > 60 {
				brief = brief[:60]
			}
			t.Fatalf("event %d is not a prefix of the final arguments: %q", i, brief)
		}
		if len(args) <= prev {
			t.Fatalf("event %d did not grow (%d after %d)", i, len(args), prev)
		}
		prev = len(args)
	}
	// The model's own view is untouched: it still gets one complete tool call.
	plain, streamed := probe.counts()
	if plain+streamed != 1 {
		t.Fatalf("expected the tool to run exactly once, got plain=%d streamed=%d", plain, streamed)
	}
}

// TestArgsStreamInertWithoutListener: nothing is computed or emitted when nobody
// wired the feed, and the run is unaffected.
func TestArgsStreamInertWithoutListener(t *testing.T) {
	body := strings.Repeat("abcdefghij", 300)
	frags := jsonFragments(`{"content":"`+body+`"}`, 400)
	srv := argsSSEServer(t, frags)
	reg := tools.NewToolRegistry()
	probe := &probeTool{}
	reg.Register(probe)
	loop := newArgsLoop(t, srv.URL, reg) // no SetOnToolArgs

	res, err := loop.Run(context.Background(), "sys", "write it", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	plain, streamed := probe.counts()
	if plain+streamed != 1 {
		t.Fatalf("expected one tool execution, got plain=%d streamed=%d", plain, streamed)
	}
}

// TestArgsStreamerThrottle pins the feed's own rules: report at the start,
// coalesce small growth, always report a restart (a retry re-sends the arguments
// from empty, and the UI must stop showing the abandoned draft), and never
// re-report something the UI already has.
func TestArgsStreamerThrottle(t *testing.T) {
	var mu sync.Mutex
	var got []string
	a := newArgsStreamer(func(name, args string) {
		mu.Lock()
		got = append(got, name+"|"+args)
		mu.Unlock()
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }

	long := `{"content":"` + strings.Repeat("x", 2000) + `"`
	a.push([]provider.StreamToolCallProgress{{Index: 0, Name: "write_file", Args: long}})
	if count() != 1 {
		t.Fatalf("first snapshot should report at once, got %d", count())
	}

	// 10 bytes more, right away: too small and too soon, must be held back.
	a.push([]provider.StreamToolCallProgress{{Index: 0, Name: "write_file", Args: long + "0123456789"}})
	if count() != 1 {
		t.Fatalf("small growth inside the window should be coalesced, got %d", count())
	}

	// A retry restarted the arguments: shorter AND different, so report at once.
	a.push([]provider.StreamToolCallProgress{{Index: 0, Name: "write_file", Args: `{"content":"`}})
	if count() != 2 {
		t.Fatalf("a restart must be reported at once, got %d", count())
	}

	// The very same snapshot again: nothing to say.
	a.push([]provider.StreamToolCallProgress{{Index: 0, Name: "write_file", Args: `{"content":"`}})
	if count() != 2 {
		t.Fatalf("an unchanged snapshot must not be re-emitted, got %d", count())
	}

	// Growth past the byte threshold reports without waiting for the window.
	a.push([]provider.StreamToolCallProgress{{Index: 0, Name: "write_file", Args: `{"content":"` + strings.Repeat("y", argsFeedBytes) + `"}`}})
	if count() != 3 {
		t.Fatalf("growth past %d bytes should report at once, got %d", argsFeedBytes, count())
	}

	// Parallel calls keep separate feeds: index 1 starting must not be swallowed
	// by whatever index 0 has already reported.
	a.push([]provider.StreamToolCallProgress{
		{Index: 0, Name: "write_file", Args: `{"content":"` + strings.Repeat("y", argsFeedBytes) + `"}`},
		{Index: 1, Name: "exec", Args: `{"command":"dir"}`},
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("the second parallel call should get its own feed, got %d events", len(got))
	}
	if !strings.HasPrefix(got[3], "exec|") {
		t.Fatalf("event 3 = %q, want the exec call", got[3])
	}

	// No listener at all: the nil streamer must swallow everything.
	var nilStreamer *argsStreamer
	nilStreamer.push([]provider.StreamToolCallProgress{{Index: 0, Name: "x", Args: "abc"}})
}
