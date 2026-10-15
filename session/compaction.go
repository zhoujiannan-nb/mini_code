package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

const (
	compactThreshold = 0.80 // auto-compaction threshold (normal path)
	// forceCompactThreshold is the hard fallback: if the model ignored the
	// proactive reminders and the context reaches 87%, compaction runs
	// regardless of the model's proactive behavior.
	forceCompactThreshold = 0.87

	// CompactKeepMessages is how many verbatim messages survive a compaction:
	// the newest ones are walked backwards from the summary until this many
	// (budget permitting) are kept after the summary block.
	CompactKeepMessages = 3
	// CompactKeepMaxTokens is the hard budget of that verbatim block. Neither
	// the whole block nor any single message inside it may exceed it: a single
	// oversized message (e.g. a huge tool result) is truncated to fit instead
	// of being kept verbatim.
	CompactKeepMaxTokens = 20000
)

// compactRequestMarker is embedded in every synthesized compaction instruction
// so the loop can recognise it (a) on the turn it is sent and (b) after a
// crash/restart, when the instruction is the last persisted message.
const compactRequestMarker = "__CTX_COMPACT_REQUEST__"

// compactRequestIntro is the fixed (passive) compaction prompt. The
// conversation is no longer re-serialized and sent to a separate summarizer
// call: the instruction is appended to the live context, so everything before
// it is still an exact prefix of the previous request and keeps hitting the
// provider's KV prefix cache. Keep this text stable — it is part of every
// compaction request.
const compactRequestIntro = `CONTEXT COMPACTION — do not continue the task, do not answer the user, and do not call any tool.

Compress the ENTIRE conversation above into one handoff summary that lets another agent of you resume the work cold. Write it in the same language as the conversation.`

const compactRequestFormat = `Output EXACTLY these five Markdown sections, in this order, and nothing else:

## Goal
(Overall task goal and original user intent)

## Current Instructions
(The most recent key user instructions or subtasks, in the user's own words)

## Discoveries
(Key observations, important tool results, problem diagnoses — exact names, paths and numbers)

## Accomplished
(Completed work, modified files, verified parts, current work state)

## Relevant Files
(Files that matter now, with a one-line status)

Use exact file paths, symbols, commands and error strings; never generalize. Focus on what is still unfinished and on the latest progress. Do not mention that you are summarizing or compacting.`

// compactFocusTemplate carries an agent-authored focus (the compact_context
// tool's instruction argument). Passive/automatic compaction passes none and
// gets only the fixed prompt above.
const compactFocusTemplate = `Additional focus requested before compressing: %q`

// BuildCompactRequest builds the user message that turns the next model call
// into a summarization turn. instruction is the agent-authored focus for
// manual / model-initiated compaction; pass "" for the fixed passive prompt.
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

// compactRules loads the optional extra rules (agent/prompts/compaction.txt,
// CWD override or embedded copy). It is a constant string, so appending it
// never breaks the request prefix.
func compactRules() string {
	if p := agent.LoadPrompt("compaction.txt"); p != "" {
		return strings.TrimSpace(p) + "\n\n"
	}
	return ""
}

// IsCompactRequest reports whether m is a synthesized compaction instruction.
func IsCompactRequest(m provider.Message) bool {
	return strings.Contains(m.GetText(), compactRequestMarker)
}

// StripCompactRequests removes every compaction instruction message. Used when
// a compaction turn failed (the instruction must not linger in the context and
// confuse the model on the next call) and when a fresh user message supersedes
// an instruction that never got answered.
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

// IsUsageNotice reports whether m is one of the 75/80/85% context-usage
// notices. They are dropped on compaction: after a compaction the numbers they
// quote are wrong, and the marker-based dedup re-arms them from the new (much
// lower) usage.
func IsUsageNotice(m provider.Message) bool {
	t := m.GetText()
	for _, pct := range []int{75, 80, 85} {
		if strings.Contains(t, usageNoticeMarker(pct)) {
			return true
		}
	}
	return false
}

// SummaryBlock wraps a summary into the stable block the next compaction reads
// back as the anchored <previous-summary>.
func SummaryBlock(summary string) string {
	return "<previous-summary>\nCompressed summary:\n\n" + strings.TrimSpace(summary) +
		"\n</previous-summary>\n\nContinue the task from this checkpoint. Do not greet the user, do not acknowledge this summary, and do not re-read files or re-run commands whose results are already recorded above."
}

// RebuildContext restarts the conversation from a summary. Input is the
// context as it was sent for the summarization turn (compaction instruction
// included). Output is [system][summary][verbatim tail]:
//
//   - the system message is kept byte-identical (it is the one prefix segment
//     that survives any rewrite);
//   - the summary becomes a single user message right after it;
//   - the last keep verbatim messages are walked backwards, never splitting a
//     tool result from the assistant tool_calls that produced it, and never
//     starting with a tool message;
//   - the verbatim block stays within budget tokens; a single message that is
//     itself too large is truncated to fit.
//
// Compaction instructions and usage notices are dropped. ok is false when
// nothing could be kept and the caller should keep the original context.
func RebuildContext(messages []provider.Message, summary string, keep, budget int) (out []provider.Message, ok bool) {
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
		if IsCompactRequest(m) || IsUsageNotice(m) {
			continue
		}
		history = append(history, m)
	}
	if len(history) == 0 {
		return messages, false
	}
	if keep < 0 {
		keep = 0
	}
	if budget <= 0 {
		budget = CompactKeepMaxTokens
	}

	// Walk backwards from the newest message and take up to `keep` of them
	// within the budget.
	taken := make([]provider.Message, 0, keep+1)
	used := 0
	for i := len(history) - 1; i >= 0 && len(taken) < keep; i-- {
		m := history[i]
		// Text-only cost estimate of this message.
		cost := util.CountMessagesTokens([]provider.Message{m})
		if cost == 0 {
			// Empty (e.g. an assistant message whose content lives entirely
			// in tool_calls, which are counted separately): keep it only as a
			// tool-call parent, see the boundary fix below.
			cost = 0
		}
		if used+cost > budget {
			if len(taken) == 0 {
				// The single newest message is itself over budget: keep a
				// truncated copy rather than losing the newest turn entirely.
				taken = append(taken, truncateToBudget(m, budget))
				used = budget
				break
			}
			break
		}
		taken = append(taken, m)
		used += cost
	}

	// Boundary safety: a tool result is only valid API input while the
	// assistant message declaring its tool_calls is kept alongside it, and the
	// retained block must never open with a tool message. Pull the parent in
	// (extra messages beyond `keep` are allowed for integrity).
	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		idx := len(history) - 1 - len(taken) // index in history of the message just before the block
		if idx < 0 {
			break
		}
		parent := history[idx]
		if parent.Role != "assistant" || len(parent.ToolCalls) == 0 {
			break
		}
		taken = append(taken, parent)
	}
	// Drop a leading tool message with no retrievable parent (should not
	// happen, but a malformed history must not produce a malformed request).
	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		taken = taken[:len(taken)-1]
	}

	out = make([]provider.Message, 0, len(taken)+2)
	if system != nil {
		out = append(out, *system)
	}
	out = append(out, provider.Message{Role: "user", Content: SummaryBlock(summary)})
	for i := len(taken) - 1; i >= 0; i-- {
		out = append(out, taken[i])
	}
	return out, true
}

// truncateToBudget cuts m down to roughly budget tokens, keeping its head
// (where the payload usually matters most) and marking the cut. The estimate
// uses the same counter as the threshold logic, refined by a few passes.
func truncateToBudget(m provider.Message, budget int) provider.Message {
	text := m.GetText()
	cost := util.CountMessagesTokens([]provider.Message{m})
	if cost <= budget || cost == 0 || text == "" {
		return m
	}
	out := m
	for pass := 0; pass < 6; pass++ {
		// Leave room for the marker line.
		keep := int(float64(len(text)) * float64(budget) / float64(cost) * 0.95)
		if keep < 0 {
			keep = 0
		}
		if keep > len(text) {
			keep = len(text)
		}
		// Cut on a rune boundary so multi-byte text stays valid.
		for keep > 0 && keep < len(text) && (text[keep]&0xC0) == 0x80 {
			keep--
		}
		out = m
		out.SetTextContent(text[:keep] + fmt.Sprintf("\n[... truncated to fit the %d token compaction budget, total %d tokens ...]", budget, cost))
		cost = util.CountMessagesTokens([]provider.Message{out})
		if cost <= budget {
			break
		}
		text = out.GetText()
	}
	return out
}

// ContextCompactor holds the thresholds and the one-shot summarization path
// used when no agent loop is running (the manual "compact now" API).
type ContextCompactor struct {
	client *provider.ModelClient
}

func NewContextCompactor(client *provider.ModelClient) *ContextCompactor {
	return &ContextCompactor{client: client}
}

// ShouldCompact reports whether the context has crossed the auto-compaction
// threshold (80%).
func (c *ContextCompactor) ShouldCompact(currentTokens, maxInput int) bool {
	if maxInput <= 0 {
		return false
	}
	return float64(currentTokens)/float64(maxInput) >= compactThreshold
}

// ShouldForceCompact reports whether the context has crossed the forced
// fallback threshold (87%).
func (c *ContextCompactor) ShouldForceCompact(currentTokens, maxInput int) bool {
	if maxInput <= 0 {
		return false
	}
	return float64(currentTokens)/float64(maxInput) >= forceCompactThreshold
}

// ShouldCompactCount is the threshold test used by the loop (calibrated
// provider-scale count).
func (c *ContextCompactor) ShouldCompactCount(currentTokens, maxInput int) bool {
	return c.ShouldCompact(currentTokens, maxInput)
}

// SummarizeAndRebuild is the compaction path for a session with NO running
// agent loop (the manual POST /api/sessions/{id}/compact endpoint). It
// reproduces exactly what a compaction turn does inside the loop: append the
// compaction instruction to the stored context and ask the model for the
// summary, then restart the context from [system][summary][verbatim tail].
//
// tools MUST be the same tool schemas the session sends on a normal turn,
// otherwise the request stops being a prefix of the previous one and the
// provider recomputes the whole context.
func (c *ContextCompactor) SummarizeAndRebuild(ctx context.Context, messages []provider.Message, tools []provider.ToolSchema, instruction string) ([]provider.Message, error) {
	if len(messages) == 0 {
		return messages, fmt.Errorf("会话为空,无需压缩")
	}
	// The request is the existing context plus one user message: everything
	// before it is byte-identical to the last normal request, so the provider
	// serves it from the KV prefix cache.
	req := append(append([]provider.Message(nil), messages...), BuildCompactRequest(instruction))
	before := util.CountMessagesTokens(messages)
	resp, err := c.client.ChatStream(ctx, req, tools, nil)
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
	out, ok := RebuildContext(messages, summary, CompactKeepMessages, CompactKeepMaxTokens)
	if !ok {
		return messages, fmt.Errorf("没有可压缩的历史(内容太短或没有安全的切分点)")
	}
	slog.Info("manual compaction rebuilt context", "before_tokens", before, "after_tokens", util.CountMessagesTokens(out), "before_msgs", len(messages), "after_msgs", len(out))
	return out, nil
}

// ExtractSummary picks the summary out of a model reply. Reasoning models can
// place (or spend the whole budget in) reasoning_content, so fall back to it
// rather than writing an empty summary block.
func ExtractSummary(content, reasoning string) string {
	if s := strings.TrimSpace(stripCodeFence(content)); s != "" {
		return s
	}
	return strings.TrimSpace(stripCodeFence(reasoning))
}

// stripCodeFence removes a single wrapping ``` fence, which some models add
// around a Markdown answer.
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
