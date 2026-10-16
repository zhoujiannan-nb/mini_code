package session

import (
	"context"
	"encoding/json"
	"fmt"
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
// limit itself is the loop's autoCompactLimit — 95% of the context window clamped
// to the input budget — so how much output was reserved cannot move it below the
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
	maxInput := loop.client.GetMaxInput() // window 4096 - output 100 - 2048
	window := loop.client.GetContextWindow()

	// 95% of this window (3891) is larger than the input budget, so the clamp
	// wins: the session may never be asked for more than the provider takes.
	limit := loop.autoCompactLimit()
	if want := int(float64(window) * autoCompactWindowFraction); want > maxInput && limit != maxInput {
		t.Fatalf("limit should clamp to the input budget %d, got %d", maxInput, limit)
	}
	if limit <= 0 {
		t.Fatal("unknown budget produced a positive limit")
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

// TestContextWindowFraction: thresholds are fractions of the model's context
// window, clamped to the input budget so a percentage can never become a request
// the provider would reject.
func TestContextWindowFraction(t *testing.T) {
	// 1M window with a 32K output: input budget 1013760, and 95% of the window
	// (996147) fits inside it.
	if got := ContextWindowFraction(1_048_576, 1_013_760, 0.95); got != 996147 {
		t.Fatalf("95%% of a 1M window: got %d, want 996147", got)
	}
	// 260K window with a 31K output: 95% of the window (247000) is above the
	// input budget (226880) — clamp to the budget instead of asking for a 400.
	if got := ContextWindowFraction(260_000, 226_880, 0.95); got != 226_880 {
		t.Fatalf("an over-budget fraction should clamp to the input budget, got %d", got)
	}
	// Unknown window → the input budget; unknown budget → the window fraction.
	if got := ContextWindowFraction(0, 5000, 0.95); got != 5000 {
		t.Fatalf("unknown window should use the budget, got %d", got)
	}
	if got := ContextWindowFraction(10_000, 0, 0.85); got != 8500 {
		t.Fatalf("unknown budget should use the window fraction, got %d", got)
	}
}

// TestUsageReminderStagesFollowTheWindow: notices at 85% and 90% of the context
// window, each announced once, quoting the window and the absolute compaction
// point instead of a percentage of an unexplained budget.
func TestUsageReminderStagesFollowTheWindow(t *testing.T) {
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider: "vllm", BaseURL: "http://127.0.0.1:1", APIKey: "t",
		ModelName: "test-model", MaxTokens: 1000, ContextWindow: 100_000,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	defer client.Close()
	loop := NewAgentLoop(client, 5, nil, nil, nil, nil, nil, nil)

	base := []provider.Message{{Role: "system", Content: "SYSTEM"}}
	// Window 100000 → stages at 85000 / 90000, compaction at 95000 (input
	// budget 96952 never gets in the way).
	if got := loop.maybeInjectUsageReminder(base, 84_999); len(got) != len(base) {
		t.Fatalf("below 85%% of the window a notice was injected: %d messages", len(got))
	}
	one := loop.maybeInjectUsageReminder(base, 85_000)
	if len(one) != len(base)+1 || !strings.Contains(one[len(base)].GetText(), usageNoticeMarker(85)) {
		t.Fatalf("the 85%% stage was not announced: %+v", one)
	}
	text := one[len(base)].GetText()
	if !strings.Contains(text, "100000 tokens") || !strings.Contains(text, "compacts automatically at 95000") {
		t.Fatalf("notice does not quote the window and the compaction point: %q", text)
	}
	// Still stage 1 → the marker dedup keeps the context untouched (prefix cache).
	if again := loop.maybeInjectUsageReminder(one, 88_000); len(again) != len(one) {
		t.Fatalf("stage announced twice: %d vs %d messages", len(again), len(one))
	}
	two := loop.maybeInjectUsageReminder(one, 90_000)
	if len(two) != len(one)+1 || !strings.Contains(two[len(two)-1].GetText(), usageNoticeMarker(90)) {
		t.Fatalf("the 90%% stage was not announced: %d messages", len(two))
	}
	if after := loop.maybeInjectUsageReminder(two, 99_000); len(after) != len(two) {
		t.Fatal("both stages announced and it still injects")
	}
}

