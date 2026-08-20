package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/tools"
)

const (
	altNudgeFirstPhrase  = "Stop the alternation"
	altNudgeSecondPhrase = "STILL alternating"
)

// alternatingSSEServer serves a scripted sequence of tool-call batches:
// odd turns issue callA, even turns callB, until `altTurns` tool turns are
// done; then (optionally) one extra batch callC; then a final text answer.
func alternatingSSEServer(t *testing.T, altTurns int, withBreak bool) *httptest.Server {
	t.Helper()
	var requests int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&requests, 1)
		turn := int(n)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case turn <= altTurns:
			if turn%2 == 1 {
				writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"ls\"}`, "tool_calls"))
			} else {
				writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"dir\"}`, "tool_calls"))
			}
		case withBreak && turn == altTurns+1:
			writeSSEChunk(w, sseToolCallChunk("exec", `{\"command\":\"type notes.txt\"}`, "tool_calls"))
		default:
			writeSSEChunk(w, sseTextChunk("done", "stop"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
}

// TestNudgeOnAlternatingToolCalls: the model ping-pongs between two distinct
// calls for six turns (A,B,A,B,A,B); the loop must inject exactly one
// alternating-stall nudge (and no exact-repeat nudge), and the task still
// completes when the model recovers with a text answer.
func TestNudgeOnAlternatingToolCalls(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	srv := alternatingSSEServer(t, 6, false)
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg
	loop.maxTurns = 20

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, altNudgeFirstPhrase); got != 1 {
		t.Fatalf("expected exactly 1 alternating nudge, got %d", got)
	}
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 0 {
		t.Fatalf("expected no exact-repeat nudge, got %d", got)
	}
}

// TestNoAltNudgeOnBrokenAlternation: A,B,A,B,A followed by a genuinely
// different call is progress, not a stall — the strict period-2 window is
// broken and no nudge may be injected.
func TestNoAltNudgeOnBrokenAlternation(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	srv := alternatingSSEServer(t, 5, true) // A B A B A, then C, then done
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg
	loop.maxTurns = 20

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, altNudgeFirstPhrase); got != 0 {
		t.Fatalf("expected no alternating nudge for broken alternation, got %d", got)
	}
	if got := countNudges(res.Messages, nudgeFirstPhrase); got != 0 {
		t.Fatalf("expected no exact-repeat nudge, got %d", got)
	}
}

// TestAltNudgeEscalatesOnce: the model ignores the first nudge and keeps
// alternating; a second, firmer nudge must appear — and no third one.
func TestAltNudgeEscalatesOnce(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	srv := alternatingSSEServer(t, 12, false) // A B A B A B [nudge] A B A B A B [nudge] done
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg
	loop.maxTurns = 20

	res, err := loop.Run(context.Background(), "system prompt", "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := countNudges(res.Messages, altNudgeFirstPhrase); got != 1 {
		t.Fatalf("expected exactly 1 first-level alternating nudge, got %d", got)
	}
	if got := countNudges(res.Messages, altNudgeSecondPhrase); got != 1 {
		t.Fatalf("expected exactly 1 second-level alternating nudge, got %d", got)
	}
}

// TestAlternatingPairPure: unit checks of the period-2 detector on raw
// signature windows.
func TestAlternatingPairPure(t *testing.T) {
	cases := []struct {
		name    string
		hist    []string
		wantOK  bool
		wantKey string
	}{
		{"strict alternation", []string{"A", "B", "A", "B", "A", "B"}, true, "A\x01B"},
		{"order-independent key", []string{"B", "A", "B", "A", "B", "A"}, true, "A\x01B"},
		{"too short", []string{"A", "B", "A", "B", "A"}, false, ""},
		{"same call repeated", []string{"A", "A", "A", "A", "A", "A"}, false, ""},
		{"three distinct calls", []string{"A", "B", "C", "A", "B", "C"}, false, ""},
		{"broken at the end", []string{"A", "B", "A", "B", "A", "C"}, false, ""},
		{"broken in the middle", []string{"A", "B", "B", "A", "B", "A"}, false, ""},
	}
	for _, c := range cases {
		gotKey, gotOK := alternatingPair(c.hist)
		if gotOK != c.wantOK || (gotOK && gotKey != c.wantKey) {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, gotKey, gotOK, c.wantKey, c.wantOK)
		}
	}
}

// TestCallDescPure: callDesc renders name and truncated arguments.
func TestCallDescPure(t *testing.T) {
	if got := callDesc("exec\x00"); got != "exec" {
		t.Errorf("empty args: got %q, want %q", got, "exec")
	}
	if got := callDesc("exec\x00{\"command\":\"ls\"}"); got != `exec({"command":"ls"})` {
		t.Errorf("short args: got %q", got)
	}
	long := "exec\x00" + makeLongString(200)
	if got := callDesc(long); !strings.HasSuffix(got, "…)") || len(got) > len("exec(")+80+len("…)") {
		t.Errorf("long args not truncated: %q (len %d)", got, len(got))
	}
}

func makeLongString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
