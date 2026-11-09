package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func countTokens(m provider.Message) int {
	return util.CountMessagesTokens([]provider.Message{m})
}

// newClientWithBudget builds a vllm test client with an explicit window so a
// test can cross the compaction threshold with a short message.
func newClientWithBudget(t *testing.T, baseURL string, contextWindow, maxTokens int) *provider.ModelClient {
	t.Helper()
	client, err := provider.NewModelClient(config.ModelConfig{
		Provider:      "vllm",
		BaseURL:       baseURL,
		APIKey:        "test",
		ModelName:     "test-model",
		MaxTokens:     maxTokens,
		ContextWindow: contextWindow,
	})
	if err != nil {
		t.Fatalf("NewModelClient: %v", err)
	}
	return client
}

func testToolDefs() []provider.ToolSchema {
	return []provider.ToolSchema{
		{Type: "function", Function: provider.ToolSchemaFunc{Name: "exec", Description: "run a command", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: provider.ToolSchemaFunc{Name: "read_file", Description: "read a file", Parameters: map[string]interface{}{"type": "object"}}},
	}
}

func sseText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": text}, "finish_reason": "stop"}},
	})
	writeSSEChunk(w, string(body))
	w.Write([]byte("data: [DONE]\n\n"))
}

// sseEmpty answers with a well-formed but empty reply: the loop's
// empty-response guard then continues to the next turn. Used to get a second
// request without needing a tool registry.
func sseEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSEChunk(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	w.Write([]byte("data: [DONE]\n\n"))
}

func sseToolCall(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"exec","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	w.Write([]byte("data: [DONE]\n\n"))
}

func historyMsgs() []provider.Message {
	return []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "next question"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function", Function: provider.FuncCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
		{Role: "tool", Content: "file body", ToolCallID: "c1", Name: "read_file"},
		{Role: "assistant", Content: "latest answer"},
	}
}

// ---------------------------------------------------------------------------
// Pure rebuild tests (no HTTP)
// ---------------------------------------------------------------------------

// TestRebuildContextShape pins the compaction shape:
// [system][verbatim tail][summary].
func TestRebuildContextShape(t *testing.T) {
	in := append(historyMsgs(), BuildCompactRequest(""))
	out, ok := RebuildContext(in, "## Goal\ndemo", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("RebuildContext refused to rebuild")
	}
	if out[0].Role != "system" || out[0].Content != "SYSTEM" {
		t.Fatalf("system message not kept first/identical: %+v", out[0])
	}
	// The summary closes the context: it is the only new content, so everything
	// before it stays a byte-stable, cacheable prefix.
	sum := out[len(out)-1]
	if !strings.Contains(sum.GetText(), "<previous-summary>") || !strings.Contains(sum.GetText(), "## Goal") {
		t.Fatalf("summary is not the anchored block closing the context: %q", sum.GetText())
	}
	if sum.Role != "user" {
		t.Fatalf("summary must be a user message, got %q", sum.Role)
	}
	for _, m := range out {
		if IsCompactRequest(m) {
			t.Fatal("compaction instruction survived the rebuild")
		}
	}
	tail := out[1 : len(out)-1]
	if len(tail) < 2 || len(tail) > CompactKeepMessages+1 {
		t.Fatalf("unexpected retained tail size %d: %+v", len(tail), tail)
	}
	if tail[0].Role == "tool" {
		t.Fatalf("retained block opens with a tool result: %+v", tail[0])
	}
	// Every retained tool result keeps the assistant tool_calls that produced it.
	for i, m := range tail {
		if m.Role != "tool" {
			continue
		}
		found := false
		for j := 0; j < i; j++ {
			for _, tc := range tail[j].ToolCalls {
				if tc.ID == m.ToolCallID {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("retained tool result %s lost its parent tool_calls", m.ToolCallID)
		}
	}
}

// TestRebuildContextDropsNoticesAndInstructions: stale 75/80/85% notices and
// compaction instructions never make it into the new context (the marker-based
// dedup re-arms the notices from the new, much lower usage).
func TestRebuildContextDropsNoticesAndInstructions(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		usageNotice(usageNoticeStage1Pct, 900, 1000, 950),
		BuildCompactRequest(""),
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("rebuild refused")
	}
	for _, m := range out {
		if IsUsageNotice(m) || IsCompactRequest(m) {
			t.Fatalf("stale notice/instruction survived: %q", m.GetText())
		}
	}
}

// TestRebuildContextHonorsBudget: the verbatim block never exceeds the budget;
// a single oversized newest message is truncated instead of blowing it.
func TestRebuildContextHonorsBudget(t *testing.T) {
	const budget = 300
	huge := strings.Repeat("lots of tool output ", 2000) // several thousand tokens
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: huge},
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, budget)
	if !ok {
		t.Fatal("rebuild refused")
	}
	if len(out) != 3 {
		t.Fatalf("expected [system][one truncated message][summary], got %d: %+v", len(out), out)
	}
	if got := countTokens(out[1]); got > budget {
		t.Fatalf("retained message costs %d tokens, over the %d budget", got, budget)
	}
	if !strings.Contains(out[1].GetText(), "truncated to fit") {
		t.Fatalf("oversized message was not truncated: len=%d", len(out[1].GetText()))
	}
	if !strings.Contains(out[2].GetText(), "<previous-summary>") {
		t.Fatalf("summary does not close the context: %q", out[2].GetText())
	}
}

// TestRebuildContextChargesTheToolCallParent: a retained tool result drags in
// the assistant message that declared the call. That parent is charged against
// the tail budget (truncating its narration) instead of being appended for free.
func TestRebuildContextChargesTheToolCallParent(t *testing.T) {
	const budget = 600
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: strings.Repeat("long narration ", 3000),
			ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function",
				Function: provider.FuncCall{Name: "exec", Arguments: `{"command":"dir"}`}}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("tool output ", 2000)},
	}
	out, ok := RebuildContext(in, "summary", CompactKeepMessages, budget)
	if !ok {
		t.Fatal("rebuild refused")
	}
	if len(out) != 4 || out[1].Role != "assistant" || out[2].Role != "tool" {
		t.Fatalf("expected [system][assistant][tool][summary], got %d messages: %+v", len(out), out)
	}
	if !strings.Contains(out[3].GetText(), "<previous-summary>") {
		t.Fatalf("summary does not close the context: %q", out[3].GetText())
	}
	// The pairing survives: the tool result still has its declaring call.
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != out[2].ToolCallID {
		t.Fatalf("tool call pairing broken: %+v", out[1].ToolCalls)
	}
	if out[1].ToolCalls[0].Function.Arguments != `{"command":"dir"}` {
		t.Fatal("tool_call arguments were altered — the request would be malformed")
	}
	cost := countTokens(out[1]) + countTokens(out[2])
	if cost > budget+minParentTokens+64 {
		t.Fatalf("retained tail costs %d tokens, over the %d budget plus the %d parent allowance",
			cost, budget, minParentTokens)
	}
	if !strings.Contains(out[1].GetText(), "truncated to fit") {
		t.Fatal("oversized parent narration was not truncated to its allowance")
	}
}

// TestCompactKeepBudget: the verbatim tail is capped by the window and by what
// the rebuilt request already carries — not only by the absolute 20k ceiling.
// Otherwise a small window finishes a compaction still over the trigger and
// spends every following turn on another (uncached) compaction turn.
func TestCompactKeepBudget(t *testing.T) {
	if got := CompactKeepBudget(0, 0); got != CompactKeepMaxTokens {
		t.Fatalf("unknown window should use the ceiling, got %d", got)
	}
	// Big window: the target is half the window (50k) but the ceiling wins.
	if got := CompactKeepBudget(100_000, 0); got != CompactKeepMaxTokens {
		t.Fatalf("100k window should keep up to the ceiling, got %d", got)
	}
	// Small window (the 7501 sandbox: 8928 input tokens): target is 4464, minus
	// the 3000 tokens of system prompt, tool schemas and summary.
	if got := CompactKeepBudget(8928, 3000); got != 1464 {
		t.Fatalf("8928-token window with 3000 overhead should keep 1464, got %d", got)
	}
	// A window whose fixed parts already eat the target still keeps a tail.
	if got := CompactKeepBudget(8928, 100_000); got != minKeepTokens {
		t.Fatalf("starved window should still keep %d tokens, got %d", minKeepTokens, got)
	}
	// Whatever the window, the rebuild target stays well under the compaction
	// point (90% of the input budget), so the turn right
	// after a compaction is a normal turn.
	for _, mi := range []int{2048, 8928, 32_000, 200_000} {
		if got := RebuildTargetTokens(mi); float64(got)/float64(mi) >= compactInputFraction {
			t.Fatalf("rebuild target %d of %d is not below the compaction threshold", got, mi)
		}
	}
}

// TestRebuildBudgetUsesTheThresholdUnits is the regression: the loop judges
// compaction on the calibrated (provider-scale) count, so the tail budget and
// the rebuild must be measured in that same unit. Measured with the raw local
// estimate, a rebuild came back ~1.9x larger than budgeted — 86% of the window
// right after compacting — and immediately triggered another compaction turn.
func TestRebuildBudgetUsesTheThresholdUnits(t *testing.T) {
	const maxInput = 8928
	// A provider that tokenizes about twice as coarsely as the local estimate.
	est := func(ms []provider.Message) int { return util.CountMessagesTokens(ms) * 2 }

	messages := []provider.Message{
		{Role: "system", Content: strings.Repeat("system prompt ", 400)},
		{Role: "user", Content: "read the log and summarize it"},
		{Role: "tool", Content: strings.Repeat("huge tool output ", 4000)},
		{Role: "assistant", Content: "done"},
	}
	summary := strings.Repeat("checkpoint summary ", 300)

	overhead := RebuildOverhead(messages, summary, testToolDefs(), est)
	out, ok := RebuildContextEst(messages, summary, CompactKeepMessages,
		CompactKeepBudget(maxInput, overhead), est)
	if !ok {
		t.Fatal("rebuild refused")
	}
	if got := est(out); float64(got)/float64(maxInput) >= compactInputFraction {
		t.Fatalf("rebuilt context is %d tokens (%.0f%% of the window), still over the trigger",
			got, 100*float64(got)/float64(maxInput))
	}
	if len(out) < 3 {
		t.Fatalf("rebuilt context lost its verbatim tail: %+v", out)
	}
	// The tools and the summary must be part of the overhead, or the tail is
	// sized against a budget the request never had.
	if bare := RebuildOverhead(messages, summary, nil, nil); bare >= overhead {
		t.Fatalf("overhead did not grow with tool schemas / summary: %d vs %d", bare, overhead)
	}
}
// TestBuildCompactRequest covers the two prompt sources: the fixed passive
// prompt, and the agent-authored focus used for compact_context / manual.
func TestBuildCompactRequest(t *testing.T) {
	fixedMsg := BuildCompactRequest("")
	fixed := fixedMsg.GetText()
	if !strings.Contains(fixed, "CONTEXT COMPACTION") || !strings.Contains(fixed, "## Relevant Files") {
		t.Fatalf("fixed prompt missing its structure: %q", fixed)
	}
	if !strings.Contains(fixed, compactRequestMarker) {
		t.Fatal("compaction request carries no marker (the loop could not recognise it)")
	}
	if strings.Contains(fixed, "Additional focus") {
		t.Fatalf("passive prompt must be the fixed one: %q", fixed)
	}
	agentMsg := BuildCompactRequest("keep the failing test output")
	agentPrompt := agentMsg.GetText()
	if !strings.Contains(agentPrompt, "keep the failing test output") {
		t.Fatalf("agent instruction dropped: %q", agentPrompt)
	}
	if !IsCompactRequest(BuildCompactRequest("x")) {
		t.Fatal("IsCompactRequest failed to recognise a compaction instruction")
	}
	if IsCompactRequest(provider.Message{Role: "user", Content: "hello"}) {
		t.Fatal("a normal user message must not look like a compaction instruction")
	}
}

// TestExtractSummary: content wins, reasoning_content is the fallback (thinking
// models), a wrapping code fence is stripped.
func TestExtractSummary(t *testing.T) {
	if got := ExtractSummary("## Goal\nreal", "ignored"); got != "## Goal\nreal" {
		t.Fatalf("content should win, got %q", got)
	}
	if got := ExtractSummary("   ", "## Goal\nfrom reasoning"); got != "## Goal\nfrom reasoning" {
		t.Fatalf("reasoning fallback failed, got %q", got)
	}
	if got := ExtractSummary("```\n## Goal\nfenced\n```", ""); got != "## Goal\nfenced" {
		t.Fatalf("code fence not stripped, got %q", got)
	}
	if got := ExtractSummary("", ""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Loop integration: the prefix-cache safety tests
// ---------------------------------------------------------------------------

// capturedRequest is one request body as the provider saw it. Messages and
// tools stay raw JSON so the assertions compare the exact bytes the server
// tokenizes — that is what decides KV prefix cache hits (i.e. money).
type capturedRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []json.RawMessage `json:"messages"`
	Tools    json.RawMessage   `json:"tools"`
}

type captureServer struct {
	mu       sync.Mutex
	requests []capturedRequest
	handler  func(w http.ResponseWriter, n int)
}

func (c *captureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req capturedRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	n := len(c.requests) + 1
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	c.handler(w, n)
}

func (c *captureServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *captureServer) at(i int) capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[i]
}

func msgAt(t *testing.T, req capturedRequest, i int) provider.Message {
	t.Helper()
	var m provider.Message
	if err := json.Unmarshal(req.Messages[i], &m); err != nil {
		t.Fatalf("decode message %d: %v", i, err)
	}
	return m
}

// TestActiveCompactionReusesTheCachedPrefix is the money test: the compaction
// request must be an exact extension of the previous normal request — same
// model, same tools, previous messages as a byte-identical prefix — so the
// provider serves everything but the appended instruction from the KV cache.
func TestActiveCompactionReusesTheCachedPrefix(t *testing.T) {
	state := tools.NewContextState()
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		switch n {
		case 1:
			// A normal turn. Request compaction while answering it, so the
			// NEXT request is the summarization turn. The empty reply just
			// keeps the loop going without needing a tool registry.
			state.RequestCompaction("keep the failing test output")
			sseEmpty(w)
		case 2:
			sseText(w, "## Goal\nsummarized work\n\n## Relevant Files\nmain.go")
		default:
			sseText(w, "done after compaction")
		}
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	loop, client := newTestLoop(t, ts.URL)
	defer client.Close()
	loop.compactionOn = true
	loop.SetContextState(state)
	loop.toolDefinitions = testToolDefs()

	if _, err := loop.Run(context.Background(), "SYSTEM PROMPT", "short task", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if srv.count() < 3 {
		t.Fatalf("expected at least 3 requests (normal, compaction, continuation), got %d", srv.count())
	}

	first, compact, after := srv.at(0), srv.at(1), srv.at(2)

	// 1. The compaction request still contains the previous request verbatim.
	if len(compact.Messages) <= len(first.Messages) {
		t.Fatalf("compaction request is not an extension of the previous one: %d vs %d messages", len(compact.Messages), len(first.Messages))
	}
	for i := range first.Messages {
		if string(first.Messages[i]) != string(compact.Messages[i]) {
			t.Fatalf("message %d changed between the normal and the compaction request (prefix cache would be invalidated):\n before: %s\n after:  %s",
				i, first.Messages[i], compact.Messages[i])
		}
	}
	// 2. Its last message is the compaction instruction (and it was not there before).
	last := msgAt(t, compact, len(compact.Messages)-1)
	if !IsCompactRequest(last) {
		t.Fatalf("the last message of the compaction request is not the instruction: %q", last.GetText())
	}
	if !strings.Contains(last.GetText(), "keep the failing test output") {
		t.Fatal("the agent-authored focus did not reach the compaction prompt")
	}
	for _, raw := range first.Messages {
		var m provider.Message
		_ = json.Unmarshal(raw, &m)
		if IsCompactRequest(m) {
			t.Fatal("instruction present on the normal turn")
		}
	}
	// 3. Tools / model / stream must be identical: they are part of the same
	//    prompt prefix, a difference costs a full re-prefill.
	if string(first.Tools) != string(compact.Tools) {
		t.Fatalf("tool schemas changed between requests (prefix cache invalidated):\n before: %s\n after:  %s", first.Tools, compact.Tools)
	}
	if len(first.Tools) == 0 {
		t.Fatal("test sent no tools; the tools assertion would be vacuous")
	}
	if first.Model != compact.Model || first.Stream != compact.Stream {
		t.Fatalf("model/stream changed on the compaction turn: %v/%v vs %v/%v", first.Model, first.Stream, compact.Model, compact.Stream)
	}
	// 4. The new context is [system][summary][tail], with no instruction left,
	//    and it is strictly smaller in tokens (that is what compaction is for).
	if a, b := sumTokens(t, after), sumTokens(t, compact); a >= b {
		t.Fatalf("context was not shrunk by compaction: %d tokens after vs %d before", a, b)
	}
	if sys := msgAt(t, after, 0); sys.Role != "system" || sys.Content != "SYSTEM PROMPT" {
		t.Fatalf("system prompt is not byte-identical after compaction: %q", sys.Content)
	}
	if last := len(after.Messages) - 1; true {
		// The summary closes the rebuilt context (it is the only new content).
		if got := msgAt(t, after, last); !strings.Contains(got.GetText(), "<previous-summary>") || !strings.Contains(got.GetText(), "summarized work") {
			t.Fatalf("summary block does not close the context: %q", got.GetText())
		}
	}
	for _, raw := range after.Messages {
		if m := mustMsg(t, raw); IsCompactRequest(m) {
			t.Fatal("the compaction instruction survived into the next turn")
		}
	}
	// 5. Tools survive the compaction turn (the task continues normally).
	if string(after.Tools) != string(first.Tools) {
		t.Fatal("tool schemas differ after compaction")
	}
}

func mustMsg(t *testing.T, raw json.RawMessage) provider.Message {
	t.Helper()
	var m provider.Message
	_ = json.Unmarshal(raw, &m)
	return m
}

// sumTokens is the size of a captured request. Compaction is judged in tokens,
// never in message counts (a summary replaces many small messages with one).
func sumTokens(t *testing.T, req capturedRequest) int {
	t.Helper()
	total := 0
	for _, raw := range req.Messages {
		total += countTokens(mustMsg(t, raw))
	}
	return total
}

// TestAutomaticCompactionTurnRunsAtTheThreshold drives the passive path with a
// tiny budget and checks the fixed prompt is what gets appended.
func TestAutomaticCompactionTurnRunsAtTheThreshold(t *testing.T) {
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		if n == 1 {
			sseText(w, "## Goal\nauto summary")
			return
		}
		sseText(w, "finished")
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := newClientWithBudget(t, ts.URL, 2400, 100)
	defer client.Close()
	loop := NewAgentLoop(client, 5, nil, testToolDefs(), nil, nil, nil, nil)
	loop.compactionOn = true

	long := strings.Repeat("detail ", 600)
	if _, err := loop.Run(context.Background(), "SYSTEM PROMPT", long, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if srv.count() < 2 {
		t.Fatalf("expected the compaction turn plus a continuation, got %d requests", srv.count())
	}
	first := srv.at(0)
	last := msgAt(t, first, len(first.Messages)-1)
	if !IsCompactRequest(last) {
		t.Fatalf("the automatic path did not append the compaction instruction: %q", last.GetText())
	}
	if strings.Contains(last.GetText(), "Additional focus") {
		t.Fatal("passive compaction must use the fixed prompt only")
	}
	if after := srv.at(1); true {
		got := msgAt(t, after, len(after.Messages)-1)
		if !strings.Contains(got.GetText(), "auto summary") {
			t.Fatal("the summary does not close the rebuilt context")
		}
	}
	if a, b := sumTokens(t, srv.at(1)), sumTokens(t, srv.at(0)); a >= b {
		t.Fatalf("context not shrunk after compaction: %d tokens after vs %d before", a, b)
	}
	// The compaction must not re-arm on the very next turn (the re-arm guard).
	for i := 1; i < srv.count(); i++ {
		for _, raw := range srv.at(i).Messages {
			if m := mustMsg(t, raw); IsCompactRequest(m) {
				t.Fatalf("compaction re-triggered on request %d without the context growing first", i+1)
			}
		}
	}
}

// TestCompactionTurnWithoutSummaryRestoresTheContext: when the model answers
// the instruction with a tool call and no text, the task must continue on the
// ORIGINAL context (instruction removed) and must not retry forever.
func TestCompactionTurnWithoutSummaryRestoresTheContext(t *testing.T) {
	state := tools.NewContextState()
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		switch n {
		case 1:
			state.RequestCompaction("now")
			sseEmpty(w)
		case 2:
			// Compaction turn: a tool call and no summary text at all.
			sseToolCall(w)
		default:
			sseText(w, "carried on")
		}
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	loop, client := newTestLoop(t, ts.URL)
	defer client.Close()
	loop.compactionOn = true
	loop.SetContextState(state)
	loop.toolDefinitions = testToolDefs()

	if _, err := loop.Run(context.Background(), "SYSTEM PROMPT", "task", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if srv.count() < 3 {
		t.Fatalf("expected the loop to carry on after the failed compaction, got %d requests", srv.count())
	}
	// No request after the failed compaction may still carry an instruction.
	for i := 2; i < srv.count(); i++ {
		for _, raw := range srv.at(i).Messages {
			if m := mustMsg(t, raw); IsCompactRequest(m) {
				t.Fatalf("request %d still carries a compaction instruction after a failed summary", i+1)
			}
		}
	}
}

// TestResumingAnInterruptedCompactionTurn: if the process died after the
// instruction was persisted, the next Run resumes the summarization turn
// instead of answering normally or appending a second instruction.
func TestResumingAnInterruptedCompactionTurn(t *testing.T) {
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		if n == 1 {
			sseText(w, "## Goal\nresumed summary")
			return
		}
		sseText(w, "carried on")
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := newClientWithBudget(t, ts.URL, 8192, 100)
	defer client.Close()
	loop := NewAgentLoop(client, 5, nil, nil, nil, nil, nil, nil)
	loop.compactionOn = false // only the resume may drive compaction here

	history := []provider.Message{
		{Role: "system", Content: "SYSTEM PROMPT"},
		{Role: "user", Content: "task"},
		BuildCompactRequest(""),
	}
	if _, err := loop.Run(context.Background(), "SYSTEM PROMPT", "", history); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first := srv.at(0)
	instructions := 0
	for _, raw := range first.Messages {
		if m := mustMsg(t, raw); IsCompactRequest(m) {
			instructions++
		}
	}
	if instructions != 1 {
		t.Fatalf("expected exactly one instruction on the resumed turn, got %d", instructions)
	}
	if after := srv.at(1); true {
		got := msgAt(t, after, len(after.Messages)-1)
		if !strings.Contains(got.GetText(), "resumed summary") {
			t.Fatal("resumed compaction did not close the context with the summary")
		}
	}
}

// TestSummarizeAndRebuildRequestShape pins the manual (no-loop) path: the
// request is the stored context plus one instruction message with the same
// tools, so it is still served from the prefix cache.
func TestSummarizeAndRebuildRequestShape(t *testing.T) {
	var got capturedRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		sseText(w, "## Goal\nmanual summary")
	}))
	defer ts.Close()

	client := newClientWithBudget(t, ts.URL, 8192, 100)
	defer client.Close()

	msgs := historyMsgs()
	out, err := NewContextCompactor(client).SummarizeAndRebuild(context.Background(), msgs, testToolDefs(), "")
	if err != nil {
		t.Fatalf("SummarizeAndRebuild: %v", err)
	}
	if len(got.Messages) != len(msgs)+1 {
		t.Fatalf("expected %d request messages (context + instruction), got %d", len(msgs)+1, len(got.Messages))
	}
	if last := msgAt(t, got, len(got.Messages)-1); !IsCompactRequest(last) {
		t.Fatalf("last request message is not the instruction: %q", last.GetText())
	}
	for i := range msgs {
		have := msgAt(t, got, i)
		if have.Role != msgs[i].Role || have.GetText() != msgs[i].GetText() {
			t.Fatalf("message %d altered: %+v vs %+v", i, have, msgs[i])
		}
	}
	if len(got.Tools) == 0 {
		t.Fatal("tool schemas were not sent on the compaction request")
	}
	if len(out) >= len(msgs) {
		t.Fatalf("context not shortened: %d vs %d", len(out), len(msgs))
	}
	if !strings.Contains(out[len(out)-1].GetText(), "manual summary") {
		t.Fatalf("summary does not close the rebuilt context: %q", out[len(out)-1].GetText())
	}
}
