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


func countTokens(m provider.Message) int {
	return util.CountMessagesTokens([]provider.Message{m})
}

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


func TestRebuildContextShape(t *testing.T) {
	in := append(historyMsgs(), BuildCompactRequest(""))
	out, ok := RebuildContext(in, "## Goal\ndemo", CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		t.Fatal("RebuildContext refused to rebuild")
	}
	if out[0].Role != "system" || out[0].Content != "SYSTEM" {
		t.Fatalf("system message not kept first/identical: %+v", out[0])
	}
	sum := out[1]
	if !strings.Contains(sum.GetText(), "<previous-summary>") || !strings.Contains(sum.GetText(), "## Goal") {
		t.Fatalf("summary is not the anchored block heading the context: %q", sum.GetText())
	}
	if sum.Role != "user" {
		t.Fatalf("summary must be a user message, got %q", sum.Role)
	}
	for _, m := range out {
		if IsCompactRequest(m) {
			t.Fatal("compaction instruction survived the rebuild")
		}
	}
	tail := out[2:]
	if len(tail) < 2 || len(tail) > CompactKeepMessages+1 {
		t.Fatalf("unexpected retained tail size %d: %+v", len(tail), tail)
	}
	if tail[0].Role == "tool" {
		t.Fatalf("retained block opens with a tool result: %+v", tail[0])
	}
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

func TestRebuildContextDropsNoticesAndInstructions(t *testing.T) {
	in := []provider.Message{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "work"},
		{Role: "user", Content: "Context usage is now at 80% of the usable input budget (900 / 1000 tokens). " + usageNoticeMarker(80)},
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

func TestRebuildContextHonorsBudget(t *testing.T) {
	const budget = 300
	huge := strings.Repeat("lots of tool output ", 2000)
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
		t.Fatalf("expected [system][summary][one truncated message], got %d: %+v", len(out), out)
	}
	if !strings.Contains(out[1].GetText(), "<previous-summary>") {
		t.Fatalf("summary does not head the context: %q", out[1].GetText())
	}
	if got := countTokens(out[2]); got > budget {
		t.Fatalf("retained message costs %d tokens, over the %d budget", got, budget)
	}
	if !strings.Contains(out[2].GetText(), "truncated to fit") {
		t.Fatalf("oversized message was not truncated: len=%d", len(out[2].GetText()))
	}
}

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
	if len(out) != 4 || out[2].Role != "assistant" || out[3].Role != "tool" {
		t.Fatalf("expected [system][summary][assistant][tool], got %d messages: %+v", len(out), out)
	}
	if !strings.Contains(out[1].GetText(), "<previous-summary>") {
		t.Fatalf("summary does not head the context: %q", out[1].GetText())
	}
	if len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != out[3].ToolCallID {
		t.Fatalf("tool call pairing broken: %+v", out[2].ToolCalls)
	}
	if out[2].ToolCalls[0].Function.Arguments != `{"command":"dir"}` {
		t.Fatal("tool_call arguments were altered — the request would be malformed")
	}
	cost := countTokens(out[2]) + countTokens(out[3])
	if cost > budget+minParentTokens+64 {
		t.Fatalf("retained tail costs %d tokens, over the %d budget plus the %d parent allowance",
			cost, budget, minParentTokens)
	}
	if !strings.Contains(out[2].GetText(), "truncated to fit") {
		t.Fatal("oversized parent narration was not truncated to its allowance")
	}
}

func TestCompactKeepBudget(t *testing.T) {
	if got := CompactKeepBudget(0, 0); got != CompactKeepMaxTokens {
		t.Fatalf("unknown window should use the ceiling, got %d", got)
	}
	if got := CompactKeepBudget(100_000, 0); got != CompactKeepMaxTokens {
		t.Fatalf("100k window should keep up to the ceiling, got %d", got)
	}
	if got := CompactKeepBudget(8928, 3000); got != 1464 {
		t.Fatalf("8928-token window with 3000 overhead should keep 1464, got %d", got)
	}
	if got := CompactKeepBudget(8928, 100_000); got != minKeepTokens {
		t.Fatalf("starved window should still keep %d tokens, got %d", minKeepTokens, got)
	}
	for _, mi := range []int{2048, 8928, 32_000, 200_000} {
		if got := RebuildTargetTokens(mi); float64(got)/float64(mi) >= compactInputFraction {
			t.Fatalf("rebuild target %d of %d is not below the compaction threshold", got, mi)
		}
	}
}

func TestRebuildBudgetUsesTheThresholdUnits(t *testing.T) {
	const maxInput = 8928
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
	if bare := RebuildOverhead(messages, summary, nil, nil); bare >= overhead {
		t.Fatalf("overhead did not grow with tool schemas / summary: %d vs %d", bare, overhead)
	}
}

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

func TestActiveCompactionReusesTheCachedPrefix(t *testing.T) {
	state := tools.NewContextState()
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		switch n {
		case 1:
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

	if len(compact.Messages) <= len(first.Messages) {
		t.Fatalf("compaction request is not an extension of the previous one: %d vs %d messages", len(compact.Messages), len(first.Messages))
	}
	for i := range first.Messages {
		if string(first.Messages[i]) != string(compact.Messages[i]) {
			t.Fatalf("message %d changed between the normal and the compaction request (prefix cache would be invalidated):\n before: %s\n after:  %s",
				i, first.Messages[i], compact.Messages[i])
		}
	}
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
	if string(first.Tools) != string(compact.Tools) {
		t.Fatalf("tool schemas changed between requests (prefix cache invalidated):\n before: %s\n after:  %s", first.Tools, compact.Tools)
	}
	if len(first.Tools) == 0 {
		t.Fatal("test sent no tools; the tools assertion would be vacuous")
	}
	if first.Model != compact.Model || first.Stream != compact.Stream {
		t.Fatalf("model/stream changed on the compaction turn: %v/%v vs %v/%v", first.Model, first.Stream, compact.Model, compact.Stream)
	}
	if a, b := sumTokens(t, after), sumTokens(t, compact); a >= b {
		t.Fatalf("context was not shrunk by compaction: %d tokens after vs %d before", a, b)
	}
	if sys := msgAt(t, after, 0); sys.Role != "system" || sys.Content != "SYSTEM PROMPT" {
		t.Fatalf("system prompt is not byte-identical after compaction: %q", sys.Content)
	}
	if first := msgAt(t, after, 1); true {
		if !strings.Contains(first.GetText(), "<previous-summary>") || !strings.Contains(first.GetText(), "summarized work") {
			t.Fatalf("summary block does not head the context: %q", first.GetText())
		}
	}
	for _, raw := range after.Messages {
		if m := mustMsg(t, raw); IsCompactRequest(m) {
			t.Fatal("the compaction instruction survived into the next turn")
		}
	}
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

func sumTokens(t *testing.T, req capturedRequest) int {
	t.Helper()
	total := 0
	for _, raw := range req.Messages {
		total += countTokens(mustMsg(t, raw))
	}
	return total
}

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
		got := msgAt(t, after, 1)
		if !strings.Contains(got.GetText(), "auto summary") {
			t.Fatal("the summary does not head the rebuilt context")
		}
	}
	if a, b := sumTokens(t, srv.at(1)), sumTokens(t, srv.at(0)); a >= b {
		t.Fatalf("context not shrunk after compaction: %d tokens after vs %d before", a, b)
	}
	for i := 1; i < srv.count(); i++ {
		for _, raw := range srv.at(i).Messages {
			if m := mustMsg(t, raw); IsCompactRequest(m) {
				t.Fatalf("compaction re-triggered on request %d without the context growing first", i+1)
			}
		}
	}
}

func TestCompactionTurnWithoutSummaryRestoresTheContext(t *testing.T) {
	state := tools.NewContextState()
	srv := &captureServer{}
	srv.handler = func(w http.ResponseWriter, n int) {
		switch n {
		case 1:
			state.RequestCompaction("now")
			sseEmpty(w)
		case 2:
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
	for i := 2; i < srv.count(); i++ {
		for _, raw := range srv.at(i).Messages {
			if m := mustMsg(t, raw); IsCompactRequest(m) {
				t.Fatalf("request %d still carries a compaction instruction after a failed summary", i+1)
			}
		}
	}
}

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
	loop.compactionOn = false

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
		got := msgAt(t, after, 1)
		if !strings.Contains(got.GetText(), "resumed summary") {
			t.Fatal("resumed compaction did not head the context with the summary")
		}
	}
}

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
	if len(out) < 2 || !strings.Contains(out[1].GetText(), "manual summary") {
		t.Fatalf("summary does not head the rebuilt context: %+v", out[1].GetText())
	}
}
