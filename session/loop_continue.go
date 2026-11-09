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
// tool call — must not close the task as completed. This mirrors the
// dsh-loop-continue plugin design from deepseek-harness:
//
//  1. deterministic gate: the guard only evaluates a text-only stop that
//     happened in a task where at least one tool call ran earlier, so plain
//     one-shot answers are never touched and no judge call is wasted;
//  2. one-word judge call: a small self-contained non-streaming request
//     (no tools, low temperature) asks whether the trailing text promises
//     work that no tool call performed; any error or unparseable verdict
//     means "finished" (never trap a completed task in the guard);
//  3. steer + stall detection: a "true" verdict appends the assistant text
//     and a steer user message so the loop runs another step; if a steer
//     produced no new tool call before the next stop, the model ignored it
//     and the turn is closed instead of burning the remaining budget.
//
// maxContinueSteers is the hard per-task cap on steers.
const maxContinueSteers = 3

// continueJudgePrompt is the judge's whole decision policy (adapted from
// dsh-loop-continue's built-in default). Keep it a single strict question.
const continueJudgePrompt = "You inspect one coding-agent turn that just ended on a text-only reply (no tool call). " +
	"Reply with exactly one word: true if the trailing text promises an action (reading, searching, running, writing, checking something) " +
	"that no tool call in the recent steps performed; false if the reply reads as a finished answer to the user."

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
	// Gate: guard off, a task that never used tools (plain Q&A), or the
	// steer budget is spent — let the task close.
	if !l.continueGuardOn || l.toolCallsSeen == 0 || l.continueSteers >= maxContinueSteers {
		return messages, false
	}
	// Stall detection: the previous steer produced no new tool call, so the
	// model ignored it; closing is better than burning the remaining budget.
	if l.continueSteers > 0 && l.toolCallsSeen == l.toolsSeenAtSteer {
		slog.Info("continue guard: previous steer produced no tool call, closing turn", "steers", l.continueSteers)
		return messages, false
	}
	if !l.judgeUnfinished(ctx, content, messages) {
		return messages, false
	}
	l.continueSteers++
	l.toolsSeenAtSteer = l.toolCallsSeen
	slog.Info("continue guard: steering unfinished turn", "steer", l.continueSteers)
	messages = append(messages,
		provider.Message{Role: "assistant", Content: content, ReasoningContent: reasoning},
		provider.Message{Role: "user", Content: continueSteerText},
	)
	return messages, true
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
	// Only the answer field votes: reasoning often meanders over "true"
	// and "false" in prose and would make the verdict noise.
	verdict, parsed := parseJudgeVerdict(resp.Content)
	slog.Info("continue guard: judge verdict", "answer", strings.TrimSpace(resp.Content), "unfinished", verdict, "parsed", parsed)
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
