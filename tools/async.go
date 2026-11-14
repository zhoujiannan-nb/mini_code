package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/user/mini_code/job"
)

// The loop has one shape for running a tool: call it, wait, hand the text back.
// Some work does not fit that shape — a build that takes seventeen minutes must
// neither hold the turn nor die because a wait got impatient. An AsyncTool
// keeps the same call signature but returns a JOB instead of an answer, and the
// wait becomes a policy decision the tool makes on the spot: finish in time and
// the result looks exactly like a synchronous result; run out of patience and
// the job is promoted (the work continues) while the tool returns a note.
//
// What the loop is promised:
//
//   - Settled jobs carry their answer in Job.Output, in the tool's normal
//     format. A command that finished in 3s reads the same whether or not the
//     engine could have backgrounded it, so nothing in the transcript depends
//     on which engine or which timing path served the call.
//   - A running job was ALREADY handed over when StartAsync returns: the tool
//     has released its own hold on it (its shell lease, its wait), so the loop
//     never has to wonder who is still watching.
//   - (nil, err) means nothing was started. The loop is then free to fall back
//     to the synchronous Execute, which is how a rejected command keeps its
//     familiar error text instead of a job-shaped paraphrase.
type AsyncCall struct {
	// Job is the registered work, running or settled.
	Job *job.Job
	// Settled reports that the work finished while the tool was waiting for it
	// in the foreground, so Job.Output is the tool's ordinary result text.
	Settled bool
	// Note is what the model reads when the job is still running: the promotion
	// text (a wait expired) or the start text (run_in_background).
	Note string
}

// AsyncTool is implemented by tools whose work can outlive the call that
// started it. owner is the session id, passed per call because a tool instance
// is built once per session but the job must carry the id of whoever started
// it, so a session only ever sees and settles its own jobs.
type AsyncTool interface {
	Name() string
	StartAsync(ctx context.Context, owner string, params map[string]interface{}, sink func(string)) (*AsyncCall, error)
}

// JobHost is implemented by the tool that owns a session's registry (exec).
// Wiring happens once, at prompt-build time: the session takes the registry to
// install its ledger-and-notice hook, and the job_* tools are handed the same
// registry so "who is still running" and "what did it print" resolve to the
// same objects the engine writes into.
type JobHost interface {
	Jobs() *job.Registry
}

// promoteNote is the model-facing handover text, shaped exactly like dsh's
// promoted foreground result: one consuming read of the output so far (so a
// later job_output continues right after it), then the still-running marker.
func promoteNote(j *job.Job, waited time.Duration) string {
	body := j.Output(true)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fmt.Sprintf("%s[still running after %dms; moved to background job %s]",
		body, waited.Milliseconds(), j.ID)
}

// backgroundNote is dsh's exact background-start ack: one short line naming
// the job; everything else flows through the job_* tools.
func backgroundNote(j *job.Job) string {
	return fmt.Sprintf("started background job %s", j.ID)
}
