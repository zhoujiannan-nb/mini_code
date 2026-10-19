package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

const (
	// compactInputFraction is where the loop forces a compaction turn, measured
	// against the model's USABLE INPUT BUDGET — the largest request the provider
	// will actually accept (context window minus the output reserve). A few percent
	// below the ceiling: the estimate is calibrated, and the head-room keeps a
	// mis-estimate from turning the request into a provider-side length error.
	compactInputFraction = 0.95
	// noticeInputFractions are the two reminder stages on the SAME reference, so
	// the model gets a turn or two to call compact_context before the loop does it.
	noticeInputFraction1 = 0.85
	noticeInputFraction2 = 0.90

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

// InputBudgetFraction maps a fraction of the model's usable input budget to a
// token count. The budget is what a request may actually use — the context window
// minus the output reserve — so it is the only honest denominator: a percentage of
// the raw window would be measured against a size the request can never reach.
// A zero budget means the model's window is unknown, in which case no absolute
// threshold can be given and this returns 0 (callers then skip the check).
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

// IsUsageNotice reports whether m is one of the context-usage notices (85/90%,
// plus the legacy 75/80/90/95% ones still stored in older sessions). They are
// dropped on compaction: after a compaction the numbers they quote are wrong, and
// the marker-based dedup re-arms them from the new (much lower) usage.
func IsUsageNotice(m provider.Message) bool {
	t := m.GetText()
	for _, pct := range []int{75, 80, 85, 90, 95} {
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
// included). Output is [system][verbatim tail][summary]:
//
//   - the system message is kept byte-identical (it is the one prefix segment
//     that survives any rewrite);
//   - the last keep verbatim messages are walked backwards, never splitting a
//     tool result from the assistant tool_calls that produced it, and never
//     starting with a tool message; they stay a contiguous block, in order,
//     exactly as they were sent before — that block is what the provider's
//     prefix cache can still match, so it goes first;
//   - the summary is appended as a single user message *after* that block: it
//     is the only new bytes of the request, which keeps [system][tail] a
//     byte-stable prefix for every later turn and for the next compaction
//     (which retains a suffix of this very tail);
//   - the verbatim block stays within budget tokens; a single message that is
//     itself too large is truncated to fit.
//
// Compaction instructions and usage notices are dropped. ok is false when
// nothing could be kept and the caller should keep the original context.
func RebuildContext(messages []provider.Message, summary string, keep, budget int) (out []provider.Message, ok bool) {
	return RebuildContextEst(messages, summary, keep, budget, defaultTokenEstimator)
}

// RebuildContextEst is RebuildContext with an explicit token estimator. est must
// be the function the compaction threshold is judged with (the loop's calibrated
// count): the budget arithmetic below and the next turn's threshold decision have
// to be in the same unit, otherwise the rebuilt context comes back ~2x larger
// than budgeted and immediately triggers another compaction turn.
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
		cost := est([]provider.Message{m})
		if used+cost > budget {
			if len(taken) == 0 {
				// The single newest message is itself over budget: keep a
				// truncated copy rather than losing the newest turn entirely.
				taken = append(taken, truncateToBudget(m, budget, est))
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
	// (extra messages beyond `keep` are allowed for integrity), but charge it
	// against the budget: its tool_call arguments are counted too, and one large
	// call payload must not silently push the tail past what was budgeted. The
	// tool_calls themselves are never touched, so the pairing stays valid.
	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		idx := len(history) - 1 - len(taken) // index in history of the message just before the block
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
	// Drop a leading tool message with no retrievable parent (should not
	// happen, but a malformed history must not produce a malformed request).
	for len(taken) > 0 && taken[len(taken)-1].Role == "tool" {
		taken = taken[:len(taken)-1]
	}

	out = make([]provider.Message, 0, len(taken)+2)
	if system != nil {
		out = append(out, *system)
	}
	for i := len(taken) - 1; i >= 0; i-- {
		out = append(out, taken[i])
	}
	// The summary closes the context: everything before it is byte-identical to
	// what has already been sent (and cached), only these new bytes are not.
	out = append(out, provider.Message{Role: "user", Content: SummaryBlock(summary)})
	return out, true
}

// truncateToBudget cuts m down to at most budget tokens (as counted by est, the
// same estimator the threshold logic uses), keeping its head — where the payload
// usually matters most — and marking the cut.
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
	// Shrink proportionally first, then verify with the estimator and keep
	// shaving 5% until it actually fits. Bounded and always terminating.
	keep := int(float64(len(text)) * float64(budget) / float64(cost) * 0.9)
	for {
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
		out := m
		out.SetTextContent(text[:keep] + marker(cost))
		if est([]provider.Message{out}) <= budget || keep == 0 {
			return out
		}
		keep = keep * 95 / 100
		if keep == 0 && len(text) > 0 {
			// Guard against an infinite loop when even the marker alone does
			// not fit: fall back to dropping the content entirely.
			out = m
			out.SetTextContent("[... content dropped: larger than the compaction budget]")
			return out
		}
	}
}

// tokenEstimator counts the tokens of a message slice. Two implementations are
// used: the raw local estimate and the loop's calibrated count (the number the
// compaction thresholds are judged with).
type tokenEstimator func([]provider.Message) int

func defaultTokenEstimator(messages []provider.Message) int {
	return util.CountMessagesTokens(messages)
}

// RebuildTargetTokens is the size the context should have right after a
// compaction: half of the input budget. Half is always below the compaction
// trigger (which is a fraction of the window clamped to the input budget), so
// the turn right after a compaction is a normal turn and not another
// (uncached) compaction turn.
func RebuildTargetTokens(maxInput int) int {
	if maxInput <= 0 {
		return CompactKeepMaxTokens
	}
	return maxInput / 2
}

// CompactKeepBudget is the token budget of the verbatim tail kept after a
// summary. The rule is the 20k token window (CompactKeepMaxTokens): the last
// CompactKeepMessages messages are retained verbatim up to that budget.
// A window too small to afford it is clamped to what fits under the rebuild
// target — otherwise compaction would "finish" with a context still above the
// trigger and immediately compact again — and minKeepTokens keeps the tail from
// ever being empty for free. overhead is what the rebuilt request carries
// anyway (system prompt, tool schemas, the summary block), measured with the
// same estimator the threshold logic uses.
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

// minKeepTokens keeps at least one (possibly truncated) message after the
// summary even on a window so small that the system prompt alone eats half of
// it — dropping the newest turn entirely is worse than going slightly over.
const minKeepTokens = 256

// minParentTokens is what a tool-call parent message is always allowed to keep
// when the tail budget is already spent: enough of its narration to make the
// retained tool result readable, while a megabyte of call arguments cannot
// become an escape hatch out of the budget.
const minParentTokens = 256

// RebuildOverhead estimates what the rebuilt request carries besides the
// verbatim tail: the system prompt, the tool schemas (re-sent, and billed, on
// every request) and the summary block itself. Must be measured with the same
// estimator as the budget, or the tail ends up larger than intended.
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

// calibratedEstimator maps the local token estimate onto the provider's token
// scale, matching AgentLoop.calibratedCount, so the manual (no-loop) compaction
// path budgets the tail the same way a running loop does.
func calibratedEstimator(model string, tools []provider.ToolSchema) tokenEstimator {
	cal := calibratorFor(model, tools)
	return func(messages []provider.Message) int {
		return cal.apply(util.CountMessagesTokens(messages))
	}
}

// recompactGrowTokens is the re-arm guard: after a compaction the context has
// to grow by at least this many tokens before the passive threshold may
// trigger another compaction turn. Without it, a context that is still over the
// threshold right after a compacting (large retained tool output, oversized
// system prompt) would spend every single turn on a compaction turn — a full
// re-prefill each time, for nothing.
func recompactGrowTokens(maxInput int) int {
	g := maxInput / 20 // 5% of the input budget
	if g < 512 {
		g = 512
	}
	return g
}

// ContextCompactor holds the thresholds and the one-shot summarization path
// used when no agent loop is running (the manual "compact now" API).
type ContextCompactor struct {
	client *provider.ModelClient
}

func NewContextCompactor(client *provider.ModelClient) *ContextCompactor {
	return &ContextCompactor{client: client}
}

// ShouldCompactCount reports whether the (calibrated, provider-scale) context
// count reached an absolute token limit. The limit comes from
// AgentLoop.autoCompactLimit — a fraction of the context window clamped to the
// input budget — so the threshold no longer depends on how large the output
// reserve happens to be. limit <= 0 means "unknown budget": never compact.
func (c *ContextCompactor) ShouldCompactCount(currentTokens, limit int) bool {
	return limit > 0 && currentTokens >= limit
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
	compEst := calibratedEstimator(c.client.ModelName(), tools)
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
	out, ok := RebuildContextEst(messages, summary, CompactKeepMessages,
		CompactKeepBudget(c.client.GetMaxInput(), RebuildOverhead(messages, summary, tools, compEst)), compEst)
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
