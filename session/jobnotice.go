package session

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/job"
)


func jobListener(mgr *SessionManager, sessionID string) func(*job.Job, string) {
	return func(j *job.Job, event string) {
		if mgr == nil || mgr.store == nil {
			return
		}
		snap := j.Snapshot()
		switch event {
		case job.EventStart:
			if j.Background {
				mgr.store.AppendEvent(sessionID, EventJobRegistered, 0, map[string]any{
					"job_id": j.ID, "kind": j.Kind, "label": j.Label,
				})
			}
		case job.EventPromote:
			if !j.Background {
				mgr.store.AppendEvent(sessionID, EventJobRegistered, 0, map[string]any{
					"job_id": j.ID, "kind": j.Kind, "label": j.Label, "late": true,
				})
			}
			mgr.store.AppendEvent(sessionID, EventJobPromoted, 0, map[string]any{
				"job_id": j.ID, "kind": j.Kind, "label": j.Label, "waited_ms": snap.WaitedMS,
			})
		case job.EventSettle:
			promoted, _ := j.Promoted()
			if !j.Background && !promoted {
				return
			}
			notified := !j.Awaited()
			payload := map[string]any{
				"job_id": j.ID, "kind": j.Kind, "status": string(snap.Status),
				"elapsed_ms": snap.EndedAt.Sub(snap.StartedAt).Milliseconds(),
				"bytes":      snap.Total, "notified": notified,
				"waited_ms": snap.WaitedMS,
			}
			if snap.Detail != "" {
				payload["detail"] = snap.Detail
			}
			mgr.store.AppendEvent(sessionID, EventJobSettled, 0, payload)
			if !notified {
				return
			}
			if notice := jobNotice(j); notice != "" {
				if err := mgr.InjectMessage(sessionID, notice); err != nil {
					slog.Warn("job settlement notice failed", "session", sessionID, "job", j.ID, "err", err)
				}
			}
		}
	}
}

func (s *Session) Jobs() *job.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execTool == nil {
		return nil
	}
	return s.execTool.Jobs()
}

func (m *SessionManager) Peek(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

const jobNoticeBudgetRunes = 900

func jobNotice(j *job.Job) string {
	snap := j.Snapshot()
	out := strings.TrimRight(j.Output(true), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "<job id=%q state=%q", j.ID, string(snap.Status))
	if snap.Detail != "" {
		fmt.Fprintf(&b, " exit=%q", snap.Detail)
	}
	b.WriteString(">\n")
	b.WriteString("System notice: a background command finished. This text was injected by the runtime, ")
	b.WriteString("it is NOT the user speaking and implies no user approval.\n")
	fmt.Fprintf(&b, "Command: %q\n", j.Label)
	if strings.TrimSpace(out) == "" {
		b.WriteString("Output: (none)")
	} else if r := []rune(out); len(r) > jobNoticeBudgetRunes {
		fmt.Fprintf(&b, "Output (last %d of %d chars):\n%s\nCall job_output(job_id=%q) for the full result.",
			jobNoticeBudgetRunes, len(r), string(r[len(r)-jobNoticeBudgetRunes:]), j.ID)
	} else {
		b.WriteString("Output:\n" + out)
	}
	b.WriteString("\n</job>")
	return b.String()
}
