package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/user/mini_code/job"
)

// The job tools: how the model talks to work it has already walked away from.
//
// Promotion only pays off if a backgrounded command is cheap to ask about. These
// three are deliberately small — read, list, stop — and their descriptions carry
// the behavioral half of the design: a model that has been told "the output is
// buffered and you will be notified" still has to be told NOT to babysit it. The
// polling that a synchronous timeout teaches (sleep, re-run, check again) is the
// exact cost this machinery exists to remove, so the instruction against it is
// written where the model will read it at the moment it is tempted.

// jobOutputWaitMax bounds one job_output call, so a wait asked for in the tool
// cannot become longer than the wait the tool call itself would have had.
const jobOutputWaitMax = 600 * time.Second

// JobTool is one tool over a session's job registry. All three share the type
// because they share everything except their text and their parameters: they read
// the same registry the exec engine writes into, and they never own work.
type JobTool struct {
	name        string
	description string
	registry    *job.Registry
	properties  map[string]interface{}
	required    []string
}

// NewJobTools builds the three tools over one registry, in a stable order. The
// registry is shared by reference with the exec tool: this is the only way
// "what is still running" can be answered without asking the processes.
func NewJobTools(reg *job.Registry) []*JobTool {
	return []*JobTool{
		{
			name: "job_output",
			description: "Read the output of a background command started by exec (job id looks like \"exec-1a2b3c4d\"). " +
				"Use it when you need the result of a command that was moved to the background, or to check partial output of one that is still running. " +
				"wait=true blocks until the job finishes (up to timeout_ms). You will be notified when a job finishes, so do NOT poll this in a loop, do not sleep between calls, and do not re-run the same command to see whether it is done. " +
				"Before ending your turn, collect any job whose result you still need — a finished job that was never read is work nobody checked.",
			registry: reg,
			properties: map[string]interface{}{
				"job_id":     map[string]interface{}{"type": "string", "description": "The job id returned by exec"},
				"wait":       map[string]interface{}{"type": "boolean", "description": "Block until the job finishes (default false = report what it has produced so far)"},
				"timeout_ms": map[string]interface{}{"type": "integer", "description": "How long to wait when wait=true (default 30000, max 600000)"},
			},
			required: []string{"job_id"},
		},
		{
			name: "job_list",
			description: "List this session's background commands (started with exec run_in_background, or moved there when their timeout expired) with their state, elapsed time and unread output. " +
				"Call it once before finishing a task that launched any background command, so nothing is left uncollected.",
			registry: reg,
			properties: map[string]interface{}{},
		},
		{
			name:        "job_kill",
			description: "Stop a running background command and kill its process tree. The job settles as killed. Use it for a command you no longer need, one that is wedged, or one you launched by mistake — not as a way to re-run something that is merely slow.",
			registry:    reg,
			properties: map[string]interface{}{
				"job_id": map[string]interface{}{"type": "string", "description": "The job id to stop"},
				"reason": map[string]interface{}{"type": "string", "description": "Why it is being stopped (recorded in the settlement)"},
			},
			required: []string{"job_id"},
		},
	}
}

func (t *JobTool) Name() string { return t.name }
func (t *JobTool) Description() string {
	if t.description == "" {
		return "Deprecated: this tool is no longer available."
	}
	return t.description
}
func (t *JobTool) IsHidden() bool { return false }
func (t *JobTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": t.properties,
		"required":   t.required,
	}
}

func (t *JobTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	if t.registry == nil {
		return NewTextResult("Error: no background-job runtime is attached to this session."), nil
	}
	switch t.name {
	case "job_output":
		return t.output(ctx, params), nil
	case "job_list":
		return t.list(), nil
	case "job_kill":
		return t.kill(params), nil
	}
	return NewTextResult("Error: unknown job tool " + t.name), nil
}

// output reads one job, optionally waiting for it. The text of a finished job is
// the very text exec would have returned synchronously, which is what lets the
// model treat "it finished while I waited" and "it finished later" as the same
// piece of information.
func (t *JobTool) output(ctx context.Context, params map[string]interface{}) *ToolResult {
	id, _ := params["job_id"].(string)
	j := t.registry.Get(strings.TrimSpace(id))
	if j == nil {
		return NewTextResult(fmt.Sprintf("Error: no job with id %q. Call job_list() to see this session's background commands.", id))
	}
	wait, _ := params["wait"].(bool)
	if wait {
		d := 30 * time.Second
		if v, ok := params["timeout_ms"].(float64); ok && v > 0 {
			d = time.Duration(v) * time.Millisecond
			if d > jobOutputWaitMax {
				d = jobOutputWaitMax
			}
		}
		j.Wait(ctx, d)
	}
	snap := j.Snapshot()
	if snap.Status == job.StatusRunning {
		// Still running: hand over the tail and say plainly that this is not an
		// answer yet, so the model does not read a truncated build log as a
		// successful build.
		elapsed := time.Since(snap.StartedAt).Round(time.Second)
		return NewTextResult(fmt.Sprintf(
			"job %s is still running (%s elapsed, %s of output so far). It is not finished - no need to wait for it here; you will be notified.\n%s",
			j.ID, elapsed, humanBytes(snap.Total), tail(orNone(j.Output(true)), 4000)))
	}
	return NewTextResult(j.Output(true))
}

// list answers "what is still going on" without a process table.
func (t *JobTool) list() *ToolResult {
	jobs := t.registry.List("")
	if len(jobs) == 0 {
		return NewTextResult("No background commands in this session.")
	}
	var b strings.Builder
	running := 0
	fmt.Fprintf(&b, "%d background command(s) in this session:\n", len(jobs))
	for _, s := range jobs {
		elapsed := s.EndedAt.Sub(s.StartedAt)
		if s.Status == job.StatusRunning {
			elapsed = time.Since(s.StartedAt)
			running++
		}
		line := fmt.Sprintf("%s  %-9s %-8s %s", s.ID, s.Status, elapsed.Round(time.Second), shortLabel(s.Label, 70))
		if s.Detail != "" {
			line += "  [" + s.Detail + "]"
		}
		if s.Status == job.StatusRunning && s.Unread > 0 {
			line += fmt.Sprintf("  (%s unread)", humanBytes(s.Unread))
		}
		b.WriteString(line + "\n")
	}
	if running > 0 {
		b.WriteString(fmt.Sprintf("%d still running. Read what you need with job_output(job_id=...); you will be told as they finish.\n", running))
	}
	return NewTextResult(strings.TrimRight(b.String(), "\n"))
}

func (t *JobTool) kill(params map[string]interface{}) *ToolResult {
	id, _ := params["job_id"].(string)
	reason, _ := params["reason"].(string)
	j := t.registry.Get(strings.TrimSpace(id))
	if j == nil {
		return NewTextResult(fmt.Sprintf("Error: no job with id %q. Call job_list() to see this session's background commands.", id))
	}
	if !j.Cancel(reason) {
		return NewTextResult(fmt.Sprintf("job %s is not running (state: %s); nothing to stop.", j.ID, j.Status()))
	}
	return NewTextResult(fmt.Sprintf("job %s was told to stop; its process tree is being killed and it settles as killed.", j.ID))
}

// ---------------------------------------------------------------- small format

// shortLabel squeezes a command's first line into one table row.
func shortLabel(s string, n int) string {
	r := []rune(strings.ReplaceAll(strings.TrimSpace(s), "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no output yet)"
	}
	return s
}

// tail keeps the end of an output: for a command still running, the interesting
// part is wherever it got to, not where it started.
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return fmt.Sprintf("(%d chars skipped)\n", len(r)-n) + string(r[len(r)-n:])
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
