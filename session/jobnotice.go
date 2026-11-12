package session

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/job"
)

// Background commands, seen from the session side.
//
// The engine (tools/shell_async.go) starts a job and waits for it only as long
// as the model asked to wait. Two things must happen when that is not enough:
// the ledger has to know the work exists and how long it was held, and the model
// has to be told when it ends — otherwise promotion is just a way to lose
// results. Both belong to the session, because both are about the conversation:
// the ledger row is this session's, and the announcement is a message in it.
//
// The notice is deliberately the same shape as a sub-agent's settlement (see
// subagent.go's settleNotice): a tagged block that says out loud that it is the
// runtime speaking. It travels on the "user" role, the only role every chat
// template honors mid-conversation, so the text itself has to prevent the model
// from reading it as the user talking — and above all as the user approving
// something.

// jobListener builds the hook a session's job registry calls at every lifecycle
// step. It is installed once, when the session wires its tools, and runs on the
// goroutine of whichever command reached that step — a build finishing at 3 a.m.
// reports through the same code as one finishing while the model watches.
func jobListener(mgr *SessionManager, sessionID string) func(*job.Job, string) {
	return func(j *job.Job, event string) {
		if mgr == nil || mgr.store == nil {
			return
		}
		snap := j.Snapshot()
		switch event {
		case job.EventStart:
			// Only work somebody walked away from gets a ledger row at birth. A
			// foreground command that finished while the model watched is already
			// described, byte for byte, by its tool/call and tool/result rows; two
			// more rows per command (this run had 200 of them) is noise that makes
			// the interesting rows harder to find, not extra history.
			if j.Background {
				mgr.store.AppendEvent(sessionID, EventJobRegistered, 0, map[string]any{
					"job_id": j.ID, "kind": j.Kind, "label": j.Label,
				})
			}
		case job.EventPromote:
			// The wait expired and the work stayed alive: the single most
			// interesting number in this whole design is how long the model sat
			// waiting before that happened. A promoted foreground job was never
			// announced at start, so it gets its registration here — every settled
			// row must have the job row it belongs to.
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
				return // plain foreground command: the tool rows already tell it all
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
				return // a live wait already handed this result to the model
			}
			if notice := jobNotice(j); notice != "" {
				if err := mgr.InjectMessage(sessionID, notice); err != nil {
					slog.Warn("job settlement notice failed", "session", sessionID, "job", j.ID, "err", err)
				}
			}
		}
	}
}

// jobNoticeBudgetRunes bounds how much output the announcement carries. The
// settlement of a long build can be megabytes; the model needs to learn that the
// command ended, with what result, and enough of the tail to know whether that
// answer is usable — the rest is one job_output call away, and paying for it in
// every notice would make promotion expensive in exactly the cases it exists for.
const jobNoticeBudgetRunes = 900

// jobNotice renders the announcement of a finished background command.
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
