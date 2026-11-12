package session

import (
	"strings"

	"github.com/user/mini_code/provider"
)

// Injected context arrives as a role:"user" turn. That is deliberate: a user
// message is the one role every chat template honors (a trailing "system"
// notice is dropped by templates that only read the first system message), and
// appending it keeps the request an exact extension of the previous one, so the
// provider's prefix cache survives. The cost is that the transcript can no
// longer tell the human's words apart from the runtime's own — which is exactly
// how "the app posted a message and the UI drew it as ME saying it" happened.
//
// deepseek-harness does not have this problem because its guards attribute
// their advice out loud ("Reminders appear in the conversation … attributed to
// the plugin" — packages/guard/repeat-tool-reminder), and its goal driver
// injects a self-describing <goal_round> block the Chat renders as its own
// request header. Codex is stricter still: hook context enters as a
// ContextualUserFragment, tagged as contextual rather than human input.
//
// mc keeps the same message bytes (changing them would break cached prefixes
// and old sessions) and labels them here instead, so the API can carry the
// origin without the model seeing a word more or less.
const (
	// NoticeWorker is a worker (工作大类) broadcast: the 公约 delivery or the
	// join reminder (worker.go).
	NoticeWorker = "worker"
	// NoticeContext is a context-usage reminder at 80%/85% (loop.go usageNotice).
	NoticeContext = "context"
	// NoticeSubagent is a background sub-agent's settlement notice (subagent.go).
	NoticeSubagent = "subagent"
	// NoticeJob is a background command's settlement notice (job/jobs.go): a
	// command that outlived its exec wait and finished later, announced in the
	// conversation it was started in (jobnotice.go).
	NoticeJob = "job"
	// NoticeSteer labels the continue guard's old nudge. The guard itself is
	// gone (loop_continue.go deleted); the exact bytes stay registered here so
	// steer rows written into older sessions still render as runtime notices
	// instead of user speech.
	NoticeSteer = "steer"
	// NoticeRetry is the empty-response nudge (loop.go).
	NoticeRetry = "retry"
	// NoticeCompact is the synthesized compaction instruction.
	NoticeCompact = "compact"
	// NoticeSummary is a compaction summary standing in for the compacted past.
	NoticeSummary = "summary"
)

// emptyRetryNotice is the nudge appended when the model answers with nothing.
// Named so the transcript can label it: it is the runtime prompting, not the
// user repeating themselves.
const emptyRetryNotice = "Your previous response was empty. Continue the task: take the next concrete step (a tool call) or answer the user directly."

// legacySteerText is the deleted continue guard's steer message, verbatim. Old
// session transcripts contain these exact bytes as role:"user" rows; keeping
// the constant here is what lets NoticeKind still label them.
const legacySteerText = "You described an action but did not call any tool. " +
	"If the work is not finished, emit the tool call for it now. " +
	"If it is genuinely finished, give the final answer instead."

// NoticeKind classifies a user-role message the runtime injected itself. ok is
// false for anything the human actually typed. The classification leans on the
// stable markers each injector already embeds, so it keeps working on sessions
// written by earlier builds.
func NoticeKind(m provider.Message) (kind string, ok bool) {
	if m.Role != "user" {
		return "", false
	}
	t := m.GetText()
	trimmed := strings.TrimSpace(t)
	switch {
	case IsWorkerNotice(m):
		return NoticeWorker, true
	case strings.Contains(t, usageNoticeMarkerPrefix):
		return NoticeContext, true
	case IsCompactRequest(m):
		return NoticeCompact, true
	case strings.Contains(t, "<previous-summary>"):
		return NoticeSummary, true
	case strings.HasPrefix(trimmed, "<subagent "):
		return NoticeSubagent, true
	case strings.HasPrefix(trimmed, "<job "):
		return NoticeJob, true
	case trimmed == legacySteerText:
		return NoticeSteer, true
	case trimmed == strings.TrimSpace(emptyRetryNotice):
		return NoticeRetry, true
	}
	return "", false
}

// IsInjectedNotice reports whether m is runtime-injected context.
func IsInjectedNotice(m provider.Message) bool {
	_, ok := NoticeKind(m)
	return ok
}
