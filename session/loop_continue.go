package session

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/user/mini_code/provider"
)

// Continue guard: a "narration-only" stop — the model writes text promising
// the next action ("先把这两个悬空项一次查完") and stops WITHOUT emitting a
// tool call — must not close the task as completed.
//
// Design lineage, after the port to this file proved too eager: neither
// deepseek-harness nor codex guesses at prose to decide whether work is left,
// and neither lets a continuation be re-granted by the model's own labor.
//
//   - dsh repeat-tool-reminder (packages/guard): deterministic detection,
//     advisory wording, the reminder is ATTRIBUTED to the plugin, and a new
//     user message CLEARS the counter (guard/README: "a fresh instruction is
//     never treated as a loop").
//   - dsh goal-round-driver (packages/goal): continuation is opt-in state, and
//     the allowance is spent only by a round that ENTERS model history — never
//     re-armed by whatever the model did afterwards; human input arriving first
//     makes automatic work yield.
//   - codex Stop hooks (core/src/hook_runtime.rs:run_turn_stop_hooks): the
//     decision belongs to an external hook, and the hook is told
//     `stop_hook_active` — "a hook already blocked once" — so the loop can
//     recognize its own continuation instead of paying for it again.
//
// So this guard holds three properties:
//
//  1. deterministic gates first, and they lean toward CLOSING: a tool-less
//     task is never judged, and a reply that hands the decision back to the
//     user (a question, an approval request, an explicit "waiting on you") is
//     a stop the model cannot lawfully continue — steered or not, the next
//     action needs a human answer, not a tool call;
//  2. one-word judge call: a small self-contained non-streaming request
//     (no tools, low temperature) asks whether the trailing text promises
//     work that no tool call performed; an empty answer falls back to the
//     model's reasoning, and any error or still-unparseable verdict means
//     "finished" (never trap a completed task in the guard);
//  3. one steer per user turn, spent when the steer message enters the
//     conversation, and never re-armed by the tool calls that obeyed it. The
//     old design re-armed on exactly that: a compliant model that narrated
//     again after working earned another nag, up to three per task, which is
//     how the guard turned into "停不下来". Only a genuine user message
//     re-arms it (see rearmContinueGuard).
//
// maxContinueSteers is the cap on steers per user turn.
const maxContinueSteers = 1

// continueJudgePrompt is the judge's whole decision policy. Keep it a single
// strict question; the hand-off clause is what the 7500 session paid for —
// "给我这四个答案我就开跑" literally promises an unperformed action, so a
// policy that only knows "promised" vs "finished" must answer true.
const continueJudgePrompt = "You inspect one coding-agent turn that just ended on a text-only reply (no tool call). " +
	"Reply with exactly one word: true if the trailing text promises an action (reading, searching, running, writing, checking something) " +
	"that no tool call in the recent steps performed AND the agent could take that action on its own; " +
	"false if the reply reads as a finished answer to the user, or if it stops for the user — asking a question, " +
	"asking for approval or a choice, or saying it waits for the user before acting."

// continueSteerText is the user message injected when the judge says the
// stop dropped a pending action. Phrase it as an instruction to emit the
// call, but leave an honest exit for a model that truly is done.
const continueSteerText = "You described an action but did not call any tool. If the work is not finished, emit the tool call for it now. " +
	"If it is genuinely finished, give the final answer instead."

const (
	judgeMaxSteps   = 6                 // recent assistant steps rendered for the judge
	judgeItemBudget = 140               // per-step preview budget (chars)
	judgeTailBudget = 1200              // trailing-text budget (split head/tail)
	judgeTimeout    = 30 * time.Second  // judge call budget; timeout lets the turn close
	judgeMaxTokens  = 200               // judge output cap (room for reasoning models)
)

// SetJudgeClient overrides the model used for the continue-guard judge call
// (nil restores judging with the session client). Must be called before Run.
func (l *AgentLoop) SetJudgeClient(c *provider.ModelClient) { l.judgeClient = c }

// SetContinueGuard toggles the narration-only stop guard (default on).
// Must be called before Run.
func (l *AgentLoop) SetContinueGuard(on bool) { l.continueGuardOn = on }

// tryContinueGuard evaluates the guard at a pure-text stop. It returns
// (messages, true) when the loop must NOT terminate: the assistant reply
// and the steer message are appended to the returned messages. Every
// bail-out path keeps the original termination behavior.
func (l *AgentLoop) tryContinueGuard(ctx context.Context, messages []provider.Message, content, reasoning string) ([]provider.Message, bool) {
	// Gate: guard off, a task that never used tools (plain Q&A), or the steer
	// allowance for this user turn is spent — let the task close.
	if !l.continueGuardOn || l.toolCallsSeen == 0 || l.continueSteers >= maxContinueSteers {
		return messages, false
	}
	// Hand-off gate, deterministic and ahead of the judge: the reply stopped
	// for the user's answer. No tool call can take that step, so judging it
	// would only buy a nag the model must ignore.
	if awaitingUserInput(content) {
		slog.Info("continue guard: reply hands the decision to the user, closing turn")
		return messages, false
	}
	if !l.judgeUnfinished(ctx, content, messages) {
		return messages, false
	}
	l.continueSteers++
	l.emitAssistantMessage(l.curTurn, content, reasoning, nil, nil)
	l.emitEvent(EventGuardSteer, l.curTurn, map[string]any{"steer": l.continueSteers, "text": continueSteerText})
	messages = append(messages,
		provider.Message{Role: "assistant", Content: content, ReasoningContent: reasoning},
		provider.Message{Role: "user", Content: continueSteerText},
	)
	return messages, true
}

// rearmContinueGuard hands the steer allowance back to the next user turn.
// Only a genuine human message qualifies (see the drain in Run): the model
// being given new work is the one event that legitimately makes a later
// narration-only stop worth steering again.
func (l *AgentLoop) rearmContinueGuard() {
	if l.continueSteers > 0 {
		slog.Info("continue guard: user message re-armed the steer allowance")
		l.continueSteers = 0
	}
}

// awaitingTailRunes is how far back the hand-off check reads; a reply always
// leaves the "your turn now" at the end.
const awaitingTailRunes = 600

// awaitingUserPhrases are the explicit "my next step needs your answer" shapes,
// in the two languages agents answer in. Multi-word on purpose: a bare "哪个" or
// "给我" also shows up inside narration ("这个函数给我返回了空指针"), and a
// false hand-off silently disables the guard, so the phrases stay unambiguous.
// An open question mark in the tail is the catch-all for everything else.
var awaitingUserPhrases = []string{
	"等你", "请你", "请确认", "请拍板", "请回答", "请回复", "要不要", "是否继续", "要继续吗",
	"要我现", "要我改", "你决定", "你拍板", "说了算", "给我答案", "给我个话", "给我答复",
	"告诉我", "拿主意", "等一句", "确认后再", "答复后", "选哪个", "哪种好", "同意吗",
	"可以吗", "行吗", "好吗", "还没你答复",
	"awaiting", "your call", "your decision", "your answer", "let me know", "tell me",
	"waiting for you", "waiting on you", "once you confirm", "please confirm", "should i",
	"which do you", "do you want me", "need your", "go-ahead", "ok to proceed",
}

// awaitingUserInput reports that a text-only reply handed the decision back to
// the user. Both reference designs lean this way: the guard's own contract is
// to never hold a finished task hostage, and steering a question costs the user
// a message that looks like their own nagging.
func awaitingUserInput(trailing string) bool {
	r := []rune(strings.TrimSpace(trailing))
	if len(r) > awaitingTailRunes {
		r = r[len(r)-awaitingTailRunes:]
	}
	tail := strings.ToLower(string(r))
	if strings.ContainsAny(tail, "?？") {
		return true
	}
	for _, p := range awaitingUserPhrases {
		if strings.Contains(tail, p) {
			return true
		}
	}
	return false
}

// judgeUnfinished asks the one-word question. Any transport error, timeout,
// API error or unparseable verdict counts as "finished": the guard may
// rescue a stalled task, it must never hold a finished one hostage.
func (l *AgentLoop) judgeUnfinished(ctx context.Context, trailing string, messages []provider.Message) bool {
	judge := l.judgeClient
	if judge == nil {
		judge = l.client
	}
	ctx, cancel := context.WithTimeout(ctx, judgeTimeout)
	defer cancel()
	req := []provider.Message{
		{Role: "system", Content: continueJudgePrompt},
		{Role: "user", Content: renderJudgeRequest(trailing, messages)},
	}
	resp, err := judge.Chat(ctx, req, nil, provider.WithTemperature(0), provider.WithMaxTokens(judgeMaxTokens))
	if err != nil {
		slog.Warn("continue guard: judge call failed, letting the turn close", "err", err)
		return false
	}
	if resp == nil || resp.Error != "" {
		slog.Warn("continue guard: judge api error, letting the turn close", "api_error", respErrText(resp))
		return false
	}
	// The answer field votes first: reasoning often meanders over "true"
	// and "false" in prose and would make the verdict noise. But a thinking
	// model can answer with an EMPTY content and leave the one word in
	// reasoning_content, and reading that as "no verdict → finished" closes
	// the turn on a narration-only stop, so an unparseable answer falls back
	// to the reasoning text.
	verdict, parsed := parseJudgeVerdict(resp.Content)
	source := "content"
	answer := resp.Content
	if !parsed {
		verdict, parsed = parseJudgeVerdict(resp.ReasoningContent)
		source, answer = "reasoning", resp.ReasoningContent
	}
	slog.Info("continue guard: judge verdict", "source", source, "answer", clip(answer, 160), "unfinished", verdict, "parsed", parsed)
	return verdict
}

func respErrText(resp *provider.ChatResponse) string {
	if resp == nil {
		return "nil response"
	}
	return resp.Error
}

// parseJudgeVerdict extracts the first standalone true/false word,
// case-insensitively. ok=false means no verdict word at all.
var judgeVerdictRe = regexp.MustCompile(`(?i)\b(?:true|false)\b`)

func parseJudgeVerdict(s string) (verdict, ok bool) {
	m := judgeVerdictRe.FindString(s)
	if m == "" {
		return false, false
	}
	return strings.EqualFold(m, "true"), true
}

// renderJudgeRequest builds the whole judge input from scratch: the task
// line, the recent assistant steps, and the trailing text. Self-contained
// on purpose (see the guard comment): the verdict must not drift with a
// replayed session history, and the cost stays O(1) per evaluated stop.
func renderJudgeRequest(trailing string, messages []provider.Message) string {
	var b strings.Builder
	b.WriteString("## Task\n")
	b.WriteString(clip(firstUserText(messages), judgeItemBudget*3) + "\n\n")
	b.WriteString("## Recent assistant steps (oldest first)\n")
	b.WriteString(renderStepSummary(messages))
	b.WriteString("\n## Trailing text of the final text-only reply\n\"\"\"\n")
	b.WriteString(clipMiddle(trailing, judgeTailBudget) + "\n\"\"\"\n\n")
	b.WriteString("Answer with exactly one word: true or false.")
	return b.String()
}

// renderStepSummary renders up to the last judgeMaxSteps assistant messages
// (text preview + tool call names) oldest-first.
func renderStepSummary(messages []provider.Message) string {
	type step struct{ text, calls string }
	var steps []step
	for i := len(messages) - 1; i >= 0 && len(steps) < judgeMaxSteps; i-- {
		m := messages[i]
		if m.Role != "assistant" {
			continue
		}
		s := step{}
		if t := strings.TrimSpace(m.Content); t != "" {
			s.text = clip(t, judgeItemBudget)
		}
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name+"("+clip(tc.Function.Arguments, 60)+")")
			}
			s.calls = strings.Join(names, ", ")
		}
		steps = append(steps, s)
	}
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. ", i+1)
		switch {
		case s.calls != "" && s.text != "":
			fmt.Fprintf(&b, "tool_calls: %s | text: %q", s.calls, s.text)
		case s.calls != "":
			fmt.Fprintf(&b, "tool_calls: %s", s.calls)
		case s.text != "":
			fmt.Fprintf(&b, "text only: %q", s.text)
		default:
			b.WriteString("(empty)")
		}
		b.WriteString("\n")
	}
	if len(steps) == 0 {
		b.WriteString("(none)\n")
	}
	return b.String()
}

func firstUserText(messages []provider.Message) string {
	for _, m := range messages {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

// clip trims s to n bytes at a rune boundary, appending an ellipsis.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s, cut) {
		cut--
	}
	return s[:cut] + "…"
}

// clipMiddle keeps the first and last halves of s within n bytes so the
// judge sees how the trailing text opens AND how it ends.
func clipMiddle(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	half := n / 2
	head, tail := half, len(s)-half
	for head > 0 && !utf8Start(s, head) {
		head--
	}
	for tail < len(s) && !utf8Start(s, tail) {
		tail++
	}
	return s[:head] + "\n…[trimmed]…\n" + s[tail:]
}

// utf8Start reports whether i is the first byte of a rune (or the end).
func utf8Start(s string, i int) bool {
	return i >= len(s) || s[i]&0xC0 != 0x80
}
