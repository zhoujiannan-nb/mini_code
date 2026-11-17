package session

import (
	"strings"

	"github.com/user/mini_code/provider"
)

const (
	NoticeWorker = "worker"
	NoticeContext = "context"
	NoticeSubagent = "subagent"
	NoticeJob = "job"
	NoticeSteer = "steer"
	NoticeRetry = "retry"
	NoticeCompact = "compact"
	NoticeSummary = "summary"
	NoticePersist = "persist"
	NoticeGoal = "goal"
)

const emptyRetryNotice = "Your previous response was empty. Continue the task: take the next concrete step (a tool call) or answer the user directly."

const legacySteerText = "You described an action but did not call any tool. " +
	"If the work is not finished, emit the tool call for it now. " +
	"If it is genuinely finished, give the final answer instead."

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
	case strings.HasPrefix(trimmed, "__MC_PERSIST_FAILED__"):
		return NoticePersist, true
	case strings.HasPrefix(trimmed, goalRoundTag):
		return NoticeGoal, true
	case strings.HasPrefix(trimmed, "<agent "), strings.HasPrefix(trimmed, "<subagent "):
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

func IsInjectedNotice(m provider.Message) bool {
	_, ok := NoticeKind(m)
	return ok
}
