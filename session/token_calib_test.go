package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

func absInt(a, b int) int {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

func TestCalibratorPassthroughBeforeSamples(t *testing.T) {
	c := newTokenCalibrator()
	if got := c.apply(1234); got != 1234 {
		t.Fatalf("apply before any sample = %d, want 1234 (passthrough)", got)
	}
}

// TestCalibratorConvergesOnLine: observations drawn from reported = 1.35x + 400
// across a wide context range must be fitted so apply() tracks the true line.
func TestCalibratorConvergesOnLine(t *testing.T) {
	c := newTokenCalibrator()
	for _, x := range []int{1000, 1500, 3000, 6000, 12000, 24000} {
		c.update(x, int(1.35*float64(x)+400))
	}
	got := c.apply(100000)
	want := int(1.35*100000 + 400)
	if absInt(got, want) > want/100 { // 1%
		t.Fatalf("apply(100000) = %d, want ≈ %d (1%% tolerance)", got, want)
	}
}

// TestCalibratorTwoPointsExact: two samples determine the two-parameter line
// exactly; apply must extrapolate along it.
func TestCalibratorTwoPointsExact(t *testing.T) {
	c := newTokenCalibrator()
	c.update(1000, 2300) // on the line y = 2x + 300
	c.update(2000, 4300)
	if got, want := c.apply(3000), 6300; absInt(got, want) > 6 {
		t.Fatalf("apply(3000) = %d, want %d (2x+300)", got, want)
	}
}

// TestCalibratorSingleSampleRatio: with one observation the fit anchors a
// pure ratio at that point.
func TestCalibratorSingleSampleRatio(t *testing.T) {
	c := newTokenCalibrator()
	c.update(1000, 2500)
	if got, want := c.apply(1000), 2500; absInt(got, want) > 3 {
		t.Fatalf("apply(1000) = %d, want ≈ %d", got, want)
	}
	// Extrapolation follows the ratio 2.5.
	if got, want := c.apply(2000), 5000; absInt(got, want) > 25 {
		t.Fatalf("apply(2000) = %d, want ≈ %d (ratio 2.5)", got, want)
	}
}

// TestCalibratorCollinearFallback: samples at the same context size carry no
// leverage; the fit must fall back to the mean ratio (finite, no blow-up).
func TestCalibratorCollinearFallback(t *testing.T) {
	c := newTokenCalibrator()
	for i := 0; i < 10; i++ {
		c.update(1000, 2400)
	}
	if got, want := c.apply(1000), 2400; absInt(got, want) > 5 {
		t.Fatalf("apply(1000) = %d, want ≈ %d (mean ratio)", got, want)
	}
	if got := c.apply(2000); got < 4000 || got > 8000 {
		t.Fatalf("apply(2000) = %d, expected a finite ratio extrapolation", got)
	}
}

func TestCalibratorIgnoresZeroReport(t *testing.T) {
	c := newTokenCalibrator()
	c.update(1000, 0)
	c.update(0, 1000)
	if got := c.apply(1000); got != 1000 {
		t.Fatalf("apply = %d, want 1000 (no usable samples, passthrough)", got)
	}
}

// TestCalibratorSafetyValve: an absurd usage report must not produce absurd
// corrections for downstream threshold decisions.
func TestCalibratorSafetyValve(t *testing.T) {
	c := newTokenCalibrator()
	c.update(1000, 1000000)
	if got := c.apply(1000); got > 4000 {
		t.Fatalf("apply(1000) = %d, safety valve should cap at 4x the estimate", got)
	}
}

// TestCalibratorWindowEviction: after calibWindow+ samples the oldest
// observations must stop influencing the fit.
func TestCalibratorWindowEviction(t *testing.T) {
	c := newTokenCalibrator()
	c.update(1000, 100000) // line 1: 100x — must be evicted
	for i := 1; i <= 40; i++ {
		c.update(1000+i, 1000+i) // line 2: identity
	}
	if got := c.apply(5000); absInt(got, 5000) > 50 {
		t.Fatalf("apply(5000) = %d, want ≈ 5000 (old 100x sample must be evicted)", got)
	}
}

func TestCalibratorRegistryKeying(t *testing.T) {
	a := calibratorFor("model-x", nil)
	if a != calibratorFor("model-x", nil) {
		t.Fatal("registry: same key must return the same calibrator")
	}
	if calibratorFor("model-y", nil) == a {
		t.Fatal("registry: different models must get different calibrators")
	}
	d1 := []provider.ToolSchema{{Type: "function", Function: provider.ToolSchemaFunc{Name: "exec"}}}
	d2 := []provider.ToolSchema{{Type: "function", Function: provider.ToolSchemaFunc{Name: "read_file"}}}
	if calibratorFor("model-x", d1) == a {
		t.Fatal("registry: a toolset must change the key")
	}
	if calibratorFor("model-x", d1) != calibratorFor("model-x", d1) {
		t.Fatal("registry: identical toolsets must share a calibrator")
	}
	_ = d2
}

func TestCalibratorConcurrentAccess(t *testing.T) {
	c := newTokenCalibrator()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				x := 1000 + i*1000
				c.update(x, x+500)
				c.apply(x)
			}
		}(g)
	}
	wg.Wait()
}

// sseChunkWithUsage wraps a choice chunk in a full SSE payload carrying the
// given usage block (provider-style accounting for the request).
func sseChunkWithUsage(choice, usage string) string {
	return "data: {\"choices\":[" + choice + "]," + usage + "}\n\n"
}

// bigSystemPrompt is a realistic-sized system prompt (~4k tokens) so the
// test exercises the normal operating range of the calibrator, where the
// safety valve (cap at 4x the raw estimate) does not interfere.
var bigSystemPrompt = strings.Repeat("The agent must follow the project conventions, coding style, and tool usage rules described in this section before taking any action. ", 200)

// TestRunCalibratesUsage: the loop must fold the provider-reported
// prompt_tokens into the calibrator and publish the CALIBRATED
// (provider-scale) context usage from the next turn onward, not the raw
// cl100k estimate. The fake server reports prompt_tokens = 2*local + 300,
// so after two requests the loop's published usage must track that line.
func TestRunCalibratesUsage(t *testing.T) {
	reg := tools.NewToolRegistry()
	reg.Register(&fakeTool{name: "exec", result: "ok"})

	var mu sync.Mutex
	var rawEst []int // raw local estimate of each request's messages
	var decodeFail atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			decodeFail.Store(1)
			return
		}
		est := util.CountMessagesTokens(body.Messages)
		mu.Lock()
		rawEst = append(rawEst, est)
		n := len(rawEst)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		usage := fmt.Sprintf(`"usage":{"prompt_tokens":%d,"completion_tokens":5,"total_tokens":%d}`, est*2+300, est*2+305)
		if n < 3 {
			choice := `{"delta":{"tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"exec","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}`
			w.Write([]byte(sseChunkWithUsage(choice, usage)))
		} else {
			choice := `{"delta":{"content":"done"},"finish_reason":"stop"}`
			w.Write([]byte(sseChunkWithUsage(choice, usage)))
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	loop.tools = reg
	loop.SetTokenCalibrator(newTokenCalibrator()) // hermetic: not the shared registry

	var published []int
	var pubMu sync.Mutex
	loop.SetOnContextUsage(func(tokens int) {
		pubMu.Lock()
		published = append(published, tokens)
		pubMu.Unlock()
	})

	res, err := loop.Run(context.Background(), bigSystemPrompt, "do it", nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if decodeFail.Load() != 0 {
		t.Fatal("fake server could not decode a request")
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if len(rawEst) != 3 || len(published) != 3 {
		t.Fatalf("expected 3 requests and 3 usage publications, got %d/%d", len(rawEst), len(published))
	}

	// Turn 1: no observations yet, so the published count is the raw estimate.
	if published[0] != rawEst[0] {
		t.Fatalf("turn 1 published = %d, want raw estimate %d (no calibration yet)", published[0], rawEst[0])
	}
	// Turn 3: two (estimate, reported) observations on the line 2x+300, so
	// the published count must track the provider scale.
	want3 := rawEst[2]*2 + 300
	if diff := absInt(published[2], want3); float64(diff) > 0.01*float64(want3) {
		t.Fatalf("turn 3 published = %d, want ≈ %d (calibrated to 2x+300); raw est was %d",
			published[2], want3, rawEst[2])
	}
	// And it must differ from the raw estimate (proof the calibration moved).
	if published[2] == rawEst[2] {
		t.Fatalf("turn 3 published %d equals the raw estimate — calibration had no effect", published[2])
	}
}

// TestShouldCompactCountHonorsCalibratedCount: the compaction *decision* is made
// on the caller-supplied (calibrated) count against an absolute token limit. The
// limit itself is the loop's autoCompactLimit — 90% of the usable input budget —
// so how much output was reserved cannot move it below the
// window the model actually has. Compaction is a turn of the agent loop (see
// compaction_turn_test.go); this pins only the threshold logic.
func TestShouldCompactCountHonorsCalibratedCount(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	// The compaction point is a fraction of the usable input budget, so it always
	// stays below what the provider takes.
	maxInput := loop.client.GetMaxInput() // window 4096 - output 100 - 2048
	if maxInput <= 0 {
		t.Fatalf("the test model should have an input budget, got %d", maxInput)
	}
	limit := loop.autoCompactLimit()
	if want := InputBudgetFraction(maxInput, compactInputFraction); limit != want {
		t.Fatalf("limit = %d, want %d (%.0f%% of the budget)", limit, want, 100*compactInputFraction)
	}
	if limit >= maxInput {
		t.Fatalf("the compaction point must stay below the budget: %d >= %d", limit, maxInput)
	}
	if loop.compactor.ShouldCompactCount(limit-1, limit) {
		t.Fatalf("ShouldCompactCount true below the limit (%d/%d)", limit-1, limit)
	}
	if !loop.compactor.ShouldCompactCount(limit, limit) {
		t.Fatalf("ShouldCompactCount false at the limit (%d/%d)", limit, limit)
	}
	if atomic.LoadInt64(&calls) != 0 {
		t.Fatalf("threshold checks must not call the model, got %d calls", calls)
	}
	// No budget known → never compact (a bogus 0 must not fire on a fresh session).
	if loop.compactor.ShouldCompactCount(1000, 0) {
		t.Fatal("ShouldCompactCount must be false when the limit is unknown")
	}
}

// TestInputBudgetFraction: every threshold is a fraction of the usable input
// budget — the only denominator a request can honestly be measured against. An
// unknown budget yields no threshold at all rather than a made-up one.
func TestInputBudgetFraction(t *testing.T) {
	if got := InputBudgetFraction(1_013_760, 0.98); got != 993_484 {
		t.Fatalf("98%% of 1013760: got %d, want 993484", got)
	}
	// A fraction above 1 still cannot ask for more than the budget itself.
	if got := InputBudgetFraction(1000, 1.5); got != 1000 {
		t.Fatalf("an over-unity fraction should clamp to the budget, got %d", got)
	}
	// Unknown budget → unknown threshold (callers skip the check).
	if got := InputBudgetFraction(0, 0.90); got != 0 {
		t.Fatalf("an unknown budget should give no threshold, got %d", got)
	}
	if got := InputBudgetFraction(-5, 0.90); got != 0 {
		t.Fatalf("a negative budget should give no threshold, got %d", got)
	}
}

// TestUsageReminderInjectionRemoved: the 80%/85% context-usage notices were removed
// — they told the model to call compact_context, a tool that is not registered,
// while the loop compacts on its own at 90% of the usable input budget. So even
// with the context far past the old first stage, a turn must append no notice:
// the model sees the conversation and nothing else. The automatic-compaction path
// (compactionOn) is off here, so a surviving injection would be the only thing
// that could add a message.
func TestUsageReminderInjectionRemoved(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	loop, client := newTestLoop(t, srv.URL)
	defer client.Close()
	// The old first stage of this model's usable input budget, cleared on purpose.
	// Built in blocks: re-tokenizing a message that grows one sentence at a time
	// would cost more than the turn it exists to test.
	stage1 := InputBudgetFraction(loop.inputBudget(), 0.80)
	block := strings.Repeat("filler sentence about the ongoing work. ", 50)
	history := []provider.Message{{Role: "user", Content: block}}
	cost := countTokens(history[0])
	if cost <= 0 {
		t.Fatalf("the filler block should cost tokens, got %d", cost)
	}
	history[0].Content = strings.Repeat(block, stage1/cost+2)
	if got := countTokens(history[0]); got < stage1 {
		t.Fatalf("the history must clear the old first stage: %d tokens of a %d budget", got, loop.inputBudget())
	}

	res, err := loop.Run(context.Background(), "SYSTEM", "continue", history)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Run: err=%v success=%v", err, res != nil && res.Success)
	}
	for _, m := range res.Messages {
		if IsUsageNotice(m) {
			t.Fatalf("a context-usage notice was still injected: %q", firstN(m.GetText(), 120))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("the model was never called, so the request was never checked")
	}
	for _, b := range bodies {
		if strings.Contains(b, usageNoticeMarkerPrefix) {
			t.Fatalf("the request still carries a usage notice: %s", firstN(b, 200))
		}
	}
}

// A model with no configured context window used to have no budget at all: every
// threshold evaluated to "never", so automatic compaction was silently dead until
// the provider rejected a request. The loop now assumes a conservative budget
// instead, which keeps it alive (and warns once).
func TestUnknownWindowFallsBackToConservativeBudget(t *testing.T) {
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: "http://127.0.0.1:1", APIKey: "t",
		ModelName: "test-model",
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer client.Close()
	loop := NewAgentLoop(client, 5, nil, nil, nil, nil, nil, nil)
	if client.GetMaxInput() != 0 {
		t.Fatalf("an unknown window should still report an unknown provider budget, got %d", client.GetMaxInput())
	}
	if got := loop.inputBudget(); got != unknownInputBudget {
		t.Fatalf("loop budget = %d, want the fallback %d", got, unknownInputBudget)
	}
	// The fallback must be expressed as a real compaction threshold, not zero.
	if limit := loop.autoCompactLimit(); limit <= 0 {
		t.Fatalf("auto-compaction limit = %d with an unknown window, want > 0", limit)
	}
}
