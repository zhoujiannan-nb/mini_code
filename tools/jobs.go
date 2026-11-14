package tools

import (
	"context"
	"fmt"
	"regexp"
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
			name:        "job_output",
			description: "Read a background command's output (job id like \"exec-1a2b3c4d\"), or the partial output of one still running. wait=true blocks until it finishes (up to timeout_ms). You are notified when a job ends — do not poll in a loop, do not sleep between calls, do not re-run the command to see whether it is done. Before ending your turn, collect the jobs whose results you still need.",
			registry:    reg,
			properties: map[string]interface{}{
				"job_id":     map[string]interface{}{"type": "string", "description": "The job id returned by exec"},
				"wait":       map[string]interface{}{"type": "boolean", "description": "Block until the job finishes (default false = report what it has produced so far)"},
				"timeout_ms": map[string]interface{}{"type": "integer", "description": "How long to wait when wait=true (default 30000, max 600000)"},
			},
			required: []string{"job_id"},
		},
		{
			name:        "job_list",
			description: "List this session's background commands (started with exec run_in_background, or moved there when the foreground wait ran out) with state, elapsed time and unread output.",
			registry:    reg,
			properties:  map[string]interface{}{},
		},
		{
			name:        "job_kill",
			description: "Stop a running background command and kill its process tree; the job settles as killed. Use it for work you no longer need or a wedged command — not for something merely slow.",
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

// Description/Parameters come from the embedded dsh spec (dsh_surface.go);
// the parameter names already matched (job_id / wait / timeout_ms / reason).
func (t *JobTool) Description() string {
	if d := dshDesc(t.name); d != "" {
		return d
	}
	if t.description == "" {
		return "Deprecated: this tool is no longer available."
	}
	return t.description
}
func (t *JobTool) IsHidden() bool { return false }
func (t *JobTool) Parameters() map[string]interface{} {
	if d := dshSpecs()[t.name]; d.Parameters != nil {
		return d.Parameters
	}
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

// reJobID matches the ids the job runtime hands out ("<kind>-<8 hex>", e.g.
// "exec-1a2b3c4d").
var reJobID = regexp.MustCompile(`^[a-z][a-z0-9]*-[0-9a-f]{8}$`)

// IsJobID reports whether an id has the shape of a background command id.
func IsJobID(id string) bool { return reJobID.MatchString(strings.TrimSpace(id)) }

// UnknownIDHint is the tail of an "unknown job <id>" error. The confusion is
// real and keeps happening in runs: a background command id looks exactly like a
// sub-agent task id, and a model that launched a command in the background
// reaches for the task tool it already knows. Saying "unknown" alone sends it
// around that loop again; naming the right tool ends it.
func UnknownIDHint(id string) string {
	if IsJobID(id) {
		return " — that is the shape of a background command id from exec; read it with job_output(job_id=...) and see job_list for the rest"
	}
	return " (use list_agents to see the agents of this conversation)"
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
