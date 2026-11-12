package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// The continue guard: a text-only stop in a tool-using task is judged
// before the task closes; an "unfinished" verdict steers one more step
// (see loop_continue.go for the design).

// guardScript drives a scripted model server: mainSSE answers the streaming
// agent calls, judgeAnswers answers the non-streaming judge calls in order
// (repeats the last entry when exhausted).
type guardScript struct {
	mainSSE      []string // one SSE payload per main (stream=true) request
	judgeAnswers []string // judge (stream=false) verdicts, in order
	// judgeRaw answers a judge call with a whole completion body, for the
	// shapes a plain verdict word cannot express (a thinking model that
	// leaves content empty and answers in reasoning_content). Wins over
	// judgeAnswers when set.
	judgeRaw []string
}

type guardRig struct {
	srv        *httptest.Server
	loop       *AgentLoop
	probe      *probeTool
	mainReqs   int64
	judgeReqs  int64
	judgeBodies []string // captured judge user-message payloads
}

func newGuardRig(t *testing.T, script guardScript) *guardRig {
	t.Helper()
	rig := &guardRig{}
	var mainN, judgeN int64
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Stream        bool `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		if !body.Stream {
			// vLLM/SGLang reject stream_options on a non-streaming call, and
			// the judge IS such a call: reproduce the 400 so a regression
			// shows up as a failing test instead of a silently disabled guard.
			if body.StreamOptions != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Stream options can only be defined when ` + "`stream=True`" + `. (parameter=stream_options)","type":"BadRequestError"}}`))
				return
			}
			// Judge call: record the user payload, answer with the next verdict.
			n := atomic.AddInt64(&judgeN, 1)
			atomic.StoreInt64(&rig.judgeReqs, n)
			idx := int(n - 1)
			if len(script.judgeRaw) > 0 {
				if idx >= len(script.judgeRaw) {
					idx = len(script.judgeRaw) - 1
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(script.judgeRaw[idx]))
				return
			}
			if idx >= len(script.judgeAnswers) {
				idx = len(script.judgeAnswers) - 1
			}
			verdict := "false"
			if idx >= 0 {
				verdict = script.judgeAnswers[idx]
			}
			if len(body.Messages) == 2 {
				rig.judgeBodies = append(rig.judgeBodies, body.Messages[1].Content)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + verdict + `"},"finish_reason":"stop"}]}`))
			return
		}
		n := atomic.AddInt64(&mainN, 1)
		atomic.StoreInt64(&rig.mainReqs, n)
		idx := int(n - 1)
		if idx >= len(script.mainSSE) {
			idx = len(script.mainSSE) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, script.mainSSE[idx])
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(rig.srv.Close)

	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       rig.srv.URL,
		APIKey:        "test",
		ModelName:     "test-model",
		MaxTokens:     100,
		ContextWindow: 4096,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	rig.probe = &probeTool{}
	reg := tools.NewToolRegistry()
	reg.Register(rig.probe)
	rig.loop = NewAgentLoop(client, 10, reg, []provider.ToolSchema{tools.ToSchema(rig.probe)}, nil, nil, nil, nil)
	rig.loop.compactionOn = false
	return rig
}

const (
	cgSSETool   = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"probetool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	cgSSENarr  = `{"choices":[{"delta":{"content":"Let me grep the night scripts to confirm what is A/B-ed."},"finish_reason":"stop"}]}`
	cgSSENarr2 = `{"choices":[{"delta":{"content":"Okay, I will write the script now and run it."},"finish_reason":"stop"}]}`
	cgSSEFinal      = `{"choices":[{"delta":{"content":"all done: sc_ is the speculative-config switch."},"finish_reason":"stop"}]}`
	cgSSEHandoff    = `{"choices":[{"delta":{"content":"四个待定项列在上面的表格里了，给我答案我就开跑。"},"finish_reason":"stop"}]}`
)

// TestContinueGuardSteersUnfinishedStop: the 7500 incident as a regression
// test — tool use, then a narration-only stop. The judge says "unfinished",
// the loop steers once, the model resumes with a real tool call and later a
// true final answer. The second stop is NOT judged at all: the steer allowance
// for this user turn is spent (maxContinueSteers), and re-arming on the model's
// own obedient tool call is what used to make the guard impossible to end.
func TestContinueGuardSteersUnfinishedStop(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSENarr, cgSSETool, cgSSEFinal},
		judgeAnswers: []string{"true", "false"},
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "all done: sc_ is the speculative-config switch." {
		t.Fatalf("unexpected final content: %q", res.Content)
	}
	if got := atomic.LoadInt64(&rig.mainReqs); got != 4 {
		t.Fatalf("expected 4 main requests (tool, narration, steered tool, final), got %d", got)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != maxContinueSteers {
		t.Fatalf("expected %d judge call(s) — one per user turn, got %d", maxContinueSteers, got)
	}
	if plain, _ := rig.probe.counts(); plain != 2 {
		t.Fatalf("expected the tool to run twice (once per tool-call turn), got %d", plain)
	}
	// The judge input must be self-contained: task line, step summary and
	// the trailing text are all present in the single user message.
	if len(rig.judgeBodies) == 0 {
		t.Fatal("no judge request captured")
	}
	for _, want := range []string{"## Task", "check the sc_ switch", "Recent assistant steps", "probetool", "grep the night scripts", "one word"} {
		if !strings.Contains(rig.judgeBodies[0], want) {
			t.Fatalf("judge payload missing %q:\n%s", want, rig.judgeBodies[0])
		}
	}
}

// TestContinueGuardOneSteerPerUserTurn: a model that answers a steer with more
// narration instead of a tool call closes the turn without paying for a second
// judge call — and so does one that OBEYED and then narrated again (the old
// stall check re-armed on exactly that).
func TestContinueGuardOneSteerPerUserTurn(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSENarr, cgSSENarr2},
		judgeAnswers: []string{"true"},
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "Okay, I will write the script now and run it." {
		t.Fatalf("expected the post-steer narration as final content, got %q", res.Content)
	}
	if got := atomic.LoadInt64(&rig.mainReqs); got != 3 {
		t.Fatalf("expected 3 main requests, got %d", got)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 1 {
		t.Fatalf("a spent allowance must not buy a second judge call, got %d calls", got)
	}
}

// TestContinueGuardHandoffNotJudged: "给我这四个答案我就开跑" promises an
// action no tool performed, so the judge alone would always rescue it. A reply
// that hands the decision back to the user is closed by the deterministic gate,
// with no judge call and no injected message.
func TestContinueGuardHandoffNotJudged(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSEHandoff},
		judgeAnswers: []string{"true"},
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "四个待定项列在上面的表格里了，给我答案我就开跑。" {
		t.Fatalf("the hand-off answer must be the final content, got %q", res.Content)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 0 {
		t.Fatalf("a reply that waits for the user must not be judged, got %d judge calls", got)
	}
	if got := atomic.LoadInt64(&rig.mainReqs); got != 2 {
		t.Fatalf("expected the task to close on the hand-off, got %d main requests", got)
	}
}

// TestContinueGuardHumanMessageRearms: a real user message is the only event
// that grants a fresh steer (dsh clears its guard counter on the same signal).
func TestContinueGuardHumanMessageRearms(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSENarr, cgSSETool, cgSSENarr, cgSSETool, cgSSEFinal},
		judgeAnswers: []string{"true"},
	})
	// The human speaks once, at the start of turn 3 — after the first steer.
	drained := 0
	rig.loop.SetInjections(func() []string {
		drained++
		if drained == 3 {
			return []string{"顺手把判分器的口径也核一遍"}
		}
		return nil
	})
	if _, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 2 {
		t.Fatalf("a new user message must re-arm the guard (one steer per user turn), got %d judge calls", got)
	}
}

// TestContinueGuardNoticeDoesNotRearm: runtime notices ride the same injection
// channel as human messages (a sub-agent settlement, a group relay). They are
// not new work and must not buy the model another steer.
func TestContinueGuardNoticeDoesNotRearm(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSENarr, cgSSETool, cgSSENarr, cgSSEFinal},
		judgeAnswers: []string{"true"},
	})
	drained := 0
	rig.loop.SetInjections(func() []string {
		drained++
		if drained == 3 {
			return []string{"<subagent id=\"abc12345\" state=\"completed\">Result: done</subagent>"}
		}
		return nil
	})
	if _, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 1 {
		t.Fatalf("an injected notice must not re-arm the guard, got %d judge calls", got)
	}
}

// TestContinueGuardBudgetBounded: a model that insists on being rescued gets at
// most maxContinueSteers extra steps per user turn, however often it obeys.
func TestContinueGuardBudgetBounded(t *testing.T) {
	// Every stop: judge true (script repeats the last answer); every
	// steered turn: a tool call, then narration again.
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSENarr, cgSSETool, cgSSENarr, cgSSETool, cgSSENarr, cgSSETool, cgSSENarr, cgSSEFinal},
		judgeAnswers: []string{"true"},
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got > maxContinueSteers {
		t.Fatalf("judge calls exceeded the per-turn steer budget: %d", got)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
}
// TestContinueGuardSkipsPlainQA: a task that never used tools is a plain
// one-shot answer — the guard must not evaluate it at all (zero judge cost,
// zero extra latency).
func TestContinueGuardSkipsPlainQA(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSEFinal},
		judgeAnswers: []string{"true"},
	})
	res, err := rig.loop.Run(context.Background(), "system", "what does GEMM mean?", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 0 {
		t.Fatalf("a tool-less task must not be judged, got %d judge calls", got)
	}
}

// TestContinueGuardJudgeVerdictClosesTask: a "false" (or unparseable)
// verdict must not hold a finished task hostage 鈥?the task closes right
// away with the model's own answer.
func TestContinueGuardJudgeVerdictClosesTask(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE:      []string{cgSSETool, cgSSEFinal},
		judgeAnswers: []string{"perhaps"}, // unparseable -> finished
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Content != "all done: sc_ is the speculative-config switch." {
		t.Fatalf("unexpected final content: %q", res.Content)
	}
	if got := atomic.LoadInt64(&rig.mainReqs); got != 2 {
		t.Fatalf("expected the task to close without steering, got %d main requests", got)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 1 {
		t.Fatalf("expected exactly 1 judge call, got %d", got)
	}
}

func TestParseJudgeVerdict(t *testing.T) {
	cases := []struct {
		in      string
		verdict bool
		ok      bool
	}{
		{"true", true, true},
		{"TRUE", true, true},
		{" false", false, true},
		{"Answer: true.", true, true},
		{"perhaps so", false, false},
		{"", false, false},
		{"not true", true, true}, // first word wins, garbage prefix tolerated
	}
	for _, c := range cases {
		v, ok := parseJudgeVerdict(c.in)
		if v != c.verdict || ok != c.ok {
			t.Errorf("parseJudgeVerdict(%q) = (%v,%v), want (%v,%v)", c.in, v, ok, c.verdict, c.ok)
		}
	}
}

// TestContinueGuardJudgeVerdictFromReasoning: a thinking judge can answer with
// an EMPTY content and leave the one word in reasoning_content — the field logs
// showed this as `answer="" unfinished=false parsed=false`, i.e. every such
// verdict read as "finished" and the turn closed on a narration-only stop. An
// unparseable answer must fall back to the reasoning text and still steer.
func TestContinueGuardJudgeVerdictFromReasoning(t *testing.T) {
	rig := newGuardRig(t, guardScript{
		mainSSE: []string{cgSSETool, cgSSENarr, cgSSETool, cgSSEFinal},
		judgeRaw: []string{
			`{"choices":[{"message":{"content":"","reasoning_content":"The reply promises to grep the night scripts and no tool call did that, so the verdict is true."},"finish_reason":"stop"}]}`,
			`{"choices":[{"message":{"content":"false"},"finish_reason":"stop"}]}`,
		},
	})
	res, err := rig.loop.Run(context.Background(), "system", "check the sc_ switch", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if got := atomic.LoadInt64(&rig.judgeReqs); got != 1 {
		t.Fatalf("expected 1 judge call (the allowance is one per user turn), got %d", got)
	}
	if got := atomic.LoadInt64(&rig.mainReqs); got != 4 {
		t.Fatalf("a reasoning-only verdict must steer, got %d main requests (want 4)", got)
	}
	if plain, _ := rig.probe.counts(); plain != 2 {
		t.Fatalf("expected the tool to run twice, got %d", plain)
	}
}
