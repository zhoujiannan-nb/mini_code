package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

const (
	compactInputFraction = 0.85

	CompactKeepMessages = 3
	CompactKeepMaxTokens = 20000
)

func InputBudgetFraction(maxInput int, fraction float64) int {
	if maxInput <= 0 {
		return 0
	}
	limit := int(float64(maxInput) * fraction)
	if limit > maxInput {
		limit = maxInput
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

const compactRequestMarker = "__CTX_COMPACT_REQUEST__"

const compactRequestIntro = `CONTEXT COMPACTION — you are now acting as a compaction engine for this conversation. Do not continue the task, do not answer the user, and do not call any tool.

Condense the conversation ABOVE into a structured checkpoint that lets another model resume the work with no loss of essential context. Write the checkpoint in the same language as the conversation.`

const compactRequestFormat = `Output EXACTLY the Markdown structure below: keep every section, in order. Use terse bullets, not prose paragraphs. Write "(none)" for an empty section — never drop a section.

## Primary Request and Intent
- [the user's original and evolving goals; quote verbatim where the exact wording matters]

## Key Technical Concepts
- [technologies, frameworks, patterns, and conventions in play]

## Files and Code
- [exact path: why it matters, key changes or snippets]

## Errors and Fixes
- [error: how it was resolved, plus any related user feedback]

## Pending Jobs
- [explicitly requested work not yet completed]

## Current Work
- [precisely what was in progress at this checkpoint]

## Next Step
- [the single next action, directly in line with the most recent request, or "(none)"]

## Critical Context
- [decisions and their rationale, constraints, user preferences, open questions, data needed to continue]

Rules:
- Preserve exact file paths, commands, error strings, identifiers, numeric values, function signatures, and syntax fragments.
- Capture user feedback and explicit instructions faithfully, especially corrections.
- Do NOT mention this summarization request or that the context was compacted.
- Output only the checkpoint text: do not call any tool or take any other action.`

const compactFocusTemplate = `Additional focus requested before compressing: %q`

func BuildCompactRequest(instruction string) provider.Message {
	var b strings.Builder
	b.WriteString(compactRequestIntro)
	b.WriteString("\n\n")
	if s := strings.TrimSpace(instruction); s != "" {
		b.WriteString(fmt.Sprintf(compactFocusTemplate, s))
		b.WriteString("\n\n")
	}
	b.WriteString(compactRequestFormat)
	b.WriteString("\n\n")
	b.WriteString(compactRules())
	b.WriteString(compactRequestMarker)
	return provider.Message{Role: "user", Content: b.String()}
}

func compactRules() string {
	if p := agent.LoadPrompt("compaction.txt"); p != "" {
		return strings.TrimSpace(p) + "\n\n"
	}
	return ""
}

func IsCompactRequest(m provider.Message) bool {
	return strings.Contains(m.GetText(), compactRequestMarker)
}

func StripCompactRequests(messages []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(messages))
	for _, m := range messages {
		if IsCompactRequest(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func SummaryBlock(summary string) string {
	return "<previous-summary>\nCompressed summary:\n\n" + strings.TrimSpace(summary) +
		"\n</previous-summary>\n\nContinue the task from this checkpoint. Do not greet the user, do not acknowledge this summary, and do not re-read files or re-run commands whose results are already recorded above."
}

const compactToolName = "compact_context"

const todoToolName = "todo_write"

func stripCompactionCeremony(history []provider.Message) []provider.Message {
	compactIDs := make(map[string]bool)
	for _, m := range history {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == compactToolName && tc.ID != "" {
				compactIDs[tc.ID] = true
			}
		}
	}
	out := make([]provider.Message, 0, len(history))
	for _, m := range history {
		if m.Role == "tool" && (compactIDs[m.ToolCallID] || m.Name == compactToolName) {
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			kept := make([]provider.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != compactToolName {
					kept = append(kept, tc)
				}
			}
			if len(kept) != len(m.ToolCalls) {
				if len(kept) == 0 && m.GetText() == "" {
					continue
				}
				cp := m
				cp.ToolCalls = kept
				out = append(out, cp)
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func RebuildContext(messages []provider.Message, summary string, keep, budget int) (out []provider.Message, ok bool) {
	return RebuildContextEst(messages, summary, keep, budget, defaultTokenEstimator)
}

func RebuildContextEst(messages []provider.Message, summary string, keep, budget int, est tokenEstimator) (out []provider.Message, ok bool) {
	if est == nil {
		est = defaultTokenEstimator
	}
	if strings.TrimSpace(summary) == "" {
		return messages, false
	}
	var system *provider.Message
	history := make([]provider.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if system == nil {
				mm := m
				system = &mm
			}
			continue
		}
		if IsCompactRequest(m) || IsWorkerNotice(m) {
			continue
		}
		history = append(history, m)
	}
	history = stripCompactionCeremony(history)
	if len(history) == 0 {
		return messages, false
	}
	if keep < 0 {
		keep = 0
	}
	if budget <= 0 {
		budget = CompactKeepMaxTokens
	}

	taken := make([]provider.Message, 0, keep+1)
	used := 0
	for i := len(history) - 1; i >= 0 && len(taken) < keep; i-- {
		m := history[i]
		cost := est([]provider.Message{m})
		if used+cost > budget {
			if len(taken) == 0 {
				taken = append(taken, truncateToBudget(m, budget, est))
				used = budget
				break
			}
			break
		}
		taken = append(taken, m)
		used += cost
	}

	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		idx := len(history) - 1 - len(taken)
		if idx < 0 {
			break
		}
		parent := history[idx]
		if parent.Role != "assistant" || len(parent.ToolCalls) == 0 {
			break
		}
		cost := est([]provider.Message{parent})
		allow := budget - used
		if allow < minParentTokens {
			allow = minParentTokens
		}
		if used+cost > budget && cost > allow {
			parent = truncateToBudget(parent, allow, est)
			cost = est([]provider.Message{parent})
		}
		used += cost
		taken = append(taken, parent)
	}
	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		taken = taken[:len(taken)-1]
	}

	out = make([]provider.Message, 0, len(taken)+4)
	if system != nil {
		out = append(out, *system)
	}
	out = append(out, provider.Message{Role: "user", Content: SummaryBlock(summary)})
	if call, result, ok := latestTodoWrite(history); ok && !toolCallKept(taken, call.ID) {
		out = append(out, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{call}})
		out = append(out, result)
	}
	for i := len(taken) - 1; i >= 0; i-- {
		out = append(out, taken[i])
	}
	for i := 2; i < len(out)-1; i++ {
		out[i].ReasoningContent = ""
	}
	if budget > 0 && est(out) > budget {
		out = shrinkOversizedTail(out, budget, est)
	}
	return out, true
}

func shrinkOversizedTail(out []provider.Message, budget int, est tokenEstimator) []provider.Message {
	if len(out) == 0 {
		return out
	}
	keep := CompactKeepMessages
	if keep > len(out) {
		keep = len(out)
	}
	protected := len(out) - keep
	for i := protected - 1; i >= 2 && est(out) > budget; i-- {
		m := &out[i]
		switch m.Role {
		case "tool":
			if pruned, ok := pruneToolText(m.GetText(), ""); ok {
				m.SetTextContent(pruned)
			}
		case "assistant":
			for j := range m.ToolCalls {
				tc := &m.ToolCalls[j]
				if rep, ok := ageOutCallArgs(tc.Function.Name, tc.Function.Arguments); ok {
					tc.Function.Arguments = rep
				}
			}
		}
	}
	return out
}

func latestTodoWrite(history []provider.Message) (provider.ToolCall, provider.Message, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != "assistant" {
			continue
		}
		for j := len(m.ToolCalls) - 1; j >= 0; j-- {
			tc := m.ToolCalls[j]
			if tc.Function.Name != todoToolName {
				continue
			}
			result := provider.Message{Role: "tool", ToolCallID: tc.ID, Name: todoToolName, Content: "Updated todo list."}
			for _, r := range history {
				if r.Role == "tool" && r.ToolCallID == tc.ID {
					result = r
					break
				}
			}
			return tc, result, true
		}
	}
	return provider.ToolCall{}, provider.Message{}, false
}

func toolCallKept(taken []provider.Message, id string) bool {
	for _, m := range taken {
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				return true
			}
		}
	}
	return false
}

func truncateToBudget(m provider.Message, budget int, est tokenEstimator) provider.Message {
	if est == nil {
		est = defaultTokenEstimator
	}
	text := m.GetText()
	cost := est([]provider.Message{m})
	if cost <= budget || cost == 0 || text == "" {
		return m
	}
	marker := func(total int) string {
		return fmt.Sprintf("\n[... truncated to fit the %d token compaction budget, total %d tokens ...]", budget, total)
	}
	keep := int(float64(len(text)) * float64(budget) / float64(cost) * 0.9)
	for {
		if keep < 0 {
			keep = 0
		}
		if keep > len(text) {
			keep = len(text)
		}
		for keep > 0 && keep < len(text) && (text[keep]&0xC0) == 0x80 {
			keep--
		}
		out := m
		out.SetTextContent(text[:keep] + marker(cost))
		if est([]provider.Message{out}) <= budget || keep == 0 {
			return out
		}
		keep = keep * 95 / 100
		if keep == 0 && len(text) > 0 {
			out = m
			out.SetTextContent("[... content dropped: larger than the compaction budget]")
			return out
		}
	}
}

type tokenEstimator func([]provider.Message) int

func defaultTokenEstimator(messages []provider.Message) int {
	return util.CountMessagesTokens(messages)
}

func RebuildTargetTokens(maxInput int) int {
	if maxInput <= 0 {
		return CompactKeepMaxTokens
	}
	return maxInput / 2
}

func CompactKeepBudget(maxInput, overhead int) int {
	budget := CompactKeepMaxTokens
	if maxInput > 0 {
		if room := RebuildTargetTokens(maxInput) - overhead; room < budget {
			budget = room
		}
	}
	if budget < minKeepTokens {
		budget = minKeepTokens
	}
	return budget
}

const minKeepTokens = 256

const minParentTokens = 256

func RebuildOverhead(messages []provider.Message, summary string, tools []provider.ToolSchema, est tokenEstimator) int {
	if est == nil {
		est = defaultTokenEstimator
	}
	var fixed []provider.Message
	for _, m := range messages {
		if m.Role == "system" {
			fixed = append(fixed, m)
			break
		}
	}
	if len(tools) > 0 {
		if data, err := json.Marshal(tools); err == nil {
			fixed = append(fixed, provider.Message{Role: "system", Content: string(data)})
		}
	}
	cost := est(fixed) + est([]provider.Message{{Role: "user", Content: SummaryBlock(summary)}})
	return cost
}

func calibratedEstimator(model string, tools []provider.ToolSchema) tokenEstimator {
	cal := calibratorFor(model, tools)
	return func(messages []provider.Message) int {
		return cal.apply(util.CountMessagesTokens(messages))
	}
}

func recompactGrowTokens(maxInput int) int {
	g := maxInput / 20
	if g < 512 {
		g = 512
	}
	return g
}

type ContextCompactor struct {
	client *provider.ModelClient
}

func NewContextCompactor(client *provider.ModelClient) *ContextCompactor {
	return &ContextCompactor{client: client}
}

func (c *ContextCompactor) ShouldCompactCount(currentTokens, limit int) bool {
	return limit > 0 && currentTokens >= limit
}

const compactionSummaryTokens = 8192

func compactionSummaryMaxTokens(client *provider.ModelClient) int {
	window := client.GetContextWindow()
	if window <= 0 {
		return compactionSummaryTokens
	}
	n := compactionSummaryTokens
	if room := window / 4; room < n {
		n = room
	}
	if n < 1024 {
		n = 1024
	}
	return n
}

func compactionPromptBudget(client *provider.ModelClient, est tokenEstimator, tools []provider.ToolSchema) int {
	window := client.GetContextWindow()
	if window <= 0 {
		return 0
	}
	schemas := make([]provider.Message, 0, len(tools))
	for _, d := range tools {
		b, _ := json.Marshal(d)
		schemas = append(schemas, provider.Message{Role: "user", Content: string(b)})
	}
	overhead := est(schemas) + compactionSummaryMaxTokens(client) + 1024
	budget := window - overhead
	if budget < 4096 {
		budget = 4096
	}
	return budget
}

func fitCompactionRequest(req []provider.Message, client *provider.ModelClient, _ tokenEstimator, tools []provider.ToolSchema) []provider.Message {
	raw := defaultTokenEstimator
	budget := compactionPromptBudget(client, raw, tools)
	if budget <= 0 || raw(req) <= budget {
		return req
	}
	start := 0
	if len(req) > 0 && req[0].Role == "system" {
		start = 1
	}
	lastAssistant := -1
	for i := len(req) - 1; i >= start; i-- {
		if req[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	for i := start; i < len(req); i++ {
		if i != lastAssistant {
			req[i].ReasoningContent = ""
		}
	}
	for i := len(req) - 2; i >= start && raw(req) > budget; i-- {
		m := &req[i]
		switch m.Role {
		case "tool":
			if pruned, ok := pruneToolText(m.GetText(), ""); ok {
				m.SetTextContent(pruned)
			}
		case "assistant":
			for j := range m.ToolCalls {
				tc := &m.ToolCalls[j]
				if rep, ok := ageOutCallArgs(tc.Function.Name, tc.Function.Arguments); ok {
					tc.Function.Arguments = rep
				}
			}
		}
	}
	for i := len(req) - 2; i >= start && raw(req) > budget; i-- {
		m := &req[i]
		if m.Role == "tool" && raw([]provider.Message{*m}) > 512 {
			t := truncateToBudget(*m, 512, raw)
			m.SetTextContent(t.GetText())
		}
	}
	return req
}

func (c *ContextCompactor) SummarizeAndRebuild(ctx context.Context, messages []provider.Message, tools []provider.ToolSchema, instruction string) ([]provider.Message, error) {
	if len(messages) == 0 {
		return messages, fmt.Errorf("会话为空,无需压缩")
	}
	compEst := calibratedEstimator(c.client.ModelName(), tools)
	req := append(append([]provider.Message(nil), messages...), BuildCompactRequest(instruction))
	req = fitCompactionRequest(req, c.client, compEst, tools)
	resp, err := c.client.ChatStream(ctx, req, tools, nil, provider.WithMaxTokens(compactionSummaryMaxTokens(c.client)))
	if err != nil {
		return messages, fmt.Errorf("生成压缩摘要失败: %w", err)
	}
	if resp == nil {
		return messages, fmt.Errorf("生成压缩摘要失败: 空响应")
	}
	if resp.Error != "" {
		return messages, fmt.Errorf("生成压缩摘要失败: %s", resp.Error)
	}
	summary := ExtractSummary(resp.Content, resp.ReasoningContent)
	if summary == "" {
		return messages, fmt.Errorf("模型未返回摘要")
	}
	out, ok := RebuildContextEst(messages, summary, CompactKeepMessages,
		CompactKeepBudget(inputBudgetFor(c.client), RebuildOverhead(messages, summary, tools, compEst)), compEst)
	if !ok {
		return messages, fmt.Errorf("没有可压缩的历史(内容太短或没有安全的切分点)")
	}
	return out, nil
}

func ExtractSummary(content, reasoning string) string {
	if s := strings.TrimSpace(stripCodeFence(content)); s != "" {
		return s
	}
	return strings.TrimSpace(stripCodeFence(reasoning))
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return s
	}
	s = strings.TrimRight(s, "`\n\r\t ")
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
