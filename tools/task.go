package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/user/mini_code/agent"
)

// sortedAgentNames returns the sub-agent names in a stable (sorted) order.
// Go randomizes map iteration order, and these names flow into model-visible
// text (tool description / parameter schema / error messages) that is part
// of the request prefix the provider's prefix cache matches — an unstable
// order would re-render that prefix differently on every request.
func sortedAgentNames() []string {
	agents := agent.ListAllAgents()
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// JobInfo is the JSON-friendly view of one sub-agent job (background or
// foreground), used by the task_* tools and the web API.
type JobInfo struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Goal       string `json:"goal"`
	Status     string `json:"status"` // running | completed | failed | canceled
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// SubagentDispatcher is what the task tools need from the sub-agent service
// (implemented by session.SubagentService).
type SubagentDispatcher interface {
	// DispatchSync runs a sub-agent in the foreground (blocks on ctx).
	DispatchSync(ctx context.Context, parentID, role, goal string, fork bool) (content string, childID string, err error)
	// DispatchBg starts a sub-agent in the background and returns its ID.
	DispatchBg(parentID, role, goal string, fork bool) (jobID string, err error)
	Output(ctx context.Context, id string, wait bool, waitSeconds int) (string, error)
	Status(id string) (JobInfo, bool)
	Cancel(id string) bool
	List(parentID string) []JobInfo
}

// TaskTool dispatches sub-tasks to sub-agents. The default sub-agent is
// "work" (full-permission worker). Sub-agents run in the foreground (the
// call blocks until the result) or in the background (the call returns
// immediately; the result is retrieved with task_output and a settlement
// notice is injected into the parent conversation when the job finishes).
// Multiple background dispatches in one turn run in parallel.
type TaskTool struct {
	parentSessionID string
	dispatcher      SubagentDispatcher
}

func NewTaskTool() *TaskTool {
	return &TaskTool{}
}

func (t *TaskTool) SetSessionContext(parentSessionID string, dispatcher SubagentDispatcher) {
	t.parentSessionID = parentSessionID
	t.dispatcher = dispatcher
}

func (t *TaskTool) Name() string { return "task" }
func (t *TaskTool) Description() string {
	agents := agent.ListAllAgents()
	var lines []string
	for _, name := range sortedAgentNames() {
		lines = append(lines, fmt.Sprintf("  - %s: %s", name, agents[name]))
	}
	return fmt.Sprintf(
		"Dispatch a sub-task to a dedicated sub-agent. The sub-agent inherits the caller's full tool permissions (minus task) and the current permission mode.\n\n"+
			"Available sub-agents:\n%s",
		strings.Join(lines, "\n"),
	)
}
func (t *TaskTool) IsHidden() bool { return false }
func (t *TaskTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"goal": map[string]interface{}{
				"type":        "string",
				"description": "Detailed task description for the sub-agent.",
			},
			"sub_agent": map[string]interface{}{
				"type":        "string",
				"description": fmt.Sprintf("Name of sub-agent (default: work). Available: %s", strings.Join(sortedAgentNames(), ", ")),
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "Run the sub-agent in the background and return immediately with a job id (default false = wait for the result).",
			},
			"fork": map[string]interface{}{
				"type":        "boolean",
				"description": "Copy the current conversation history into the sub-agent as its starting context (default false).",
			},
		},
		"required": []string{"goal"},
	}
}

func (t *TaskTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	goal, _ := params["goal"].(string)
	if goal == "" {
		return NewTextResult("Error: missing required parameter 'goal'"), nil
	}
	subAgent, _ := params["sub_agent"].(string)
	if subAgent == "" {
		subAgent = "work"
	}
	runBg, _ := params["run_in_background"].(bool)
	fork, _ := params["fork"].(bool)

	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), nil
	}

	agents := agent.ListAllAgents()
	if _, ok := agents[subAgent]; !ok {
		return NewTextResult(fmt.Sprintf("Error: unknown sub-agent '%s'. Available: %s", subAgent, strings.Join(sortedAgentNames(), ", "))), nil
	}

	if runBg {
		jobID, err := t.dispatcher.DispatchBg(t.parentSessionID, subAgent, goal, fork)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error: failed to start background sub-agent: %s", err)), nil
		}
		return NewTextResult(fmt.Sprintf(
			"Background sub-agent started: job %s (role %s). It runs independently while you continue working.\n"+
				"- task_output(id=\"%s\", wait=true) to retrieve the result (blocks until done or timeout)\n"+
				"- task_status(id) / task_list() to check status\n"+
				"- task_cancel(id) to stop it\n"+
				"A settlement notice will be added to this conversation when it finishes, and it will wake you up if you have stopped in the meantime.\n"+
				"Do NOT sleep, poll for progress, ask the sub-agent for status, or duplicate its work; avoid editing the same files or topics it is working on. "+
				"Either continue with non-overlapping work, or briefly tell the user what you launched and end your turn.",
			jobID, subAgent, jobID)), nil
	}

	content, childID, err := t.dispatcher.DispatchSync(ctx, t.parentSessionID, subAgent, goal, fork)
	if err != nil {
		if ctx.Err() != nil {
			return NewTextResult(fmt.Sprintf("[Sub-agent '%s' interrupted] The task was canceled.", subAgent)), nil
		}
		return NewTextResult(fmt.Sprintf("[Sub-agent '%s' failed] Error: %s", subAgent, err)), nil
	}
	return NewTextResult(fmt.Sprintf("[Sub-agent '%s' completed (session %s)]\n\n%s", subAgent, childID, content)), nil
}

// --- task control tools (task_output / task_status / task_cancel / task_list) ---

type TaskControlTool struct {
	name            string
	description     string
	parentSessionID string
	dispatcher      SubagentDispatcher
	properties      map[string]interface{}
	required        []string
}

func NewTaskControlTool(name, description, parentSessionID string, dispatcher SubagentDispatcher, properties map[string]interface{}, required []string) *TaskControlTool {
	return &TaskControlTool{
		name: name, description: description,
		parentSessionID: parentSessionID, dispatcher: dispatcher,
		properties: properties, required: required,
	}
}

func (t *TaskControlTool) Name() string        { return t.name }
func (t *TaskControlTool) Description() string { return t.description }
func (t *TaskControlTool) IsHidden() bool      { return false }
func (t *TaskControlTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": t.properties,
		"required":   t.required,
	}
}

func (t *TaskControlTool) dispatcherOrError() (*ToolResult, bool) {
	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), false
	}
	return nil, true
}

// NewTaskOutputTool builds the task_output tool.
func NewTaskOutputTool(parentSessionID string, d SubagentDispatcher) *TaskControlTool {
	return NewTaskControlTool("task_output",
		"Retrieve the result of a sub-agent job (from a task call with run_in_background=true). With wait=true it blocks until the job finishes or wait_seconds elapses.",
		parentSessionID, d,
		map[string]interface{}{
			"id":           map[string]interface{}{"type": "string", "description": "Job id returned by the task call."},
			"wait":         map[string]interface{}{"type": "boolean", "description": "Block until the job finishes (default false)."},
			"wait_seconds": map[string]interface{}{"type": "integer", "description": "Max seconds to wait when wait=true (default 60, max 300)."},
		},
		[]string{"id"},
	)
}

// NewTaskStatusTool builds the task_status tool.
func NewTaskStatusTool(parentSessionID string, d SubagentDispatcher) *TaskControlTool {
	return NewTaskControlTool("task_status",
		"Check the current status of one sub-agent job (running / completed / failed / canceled) without waiting.",
		parentSessionID, d,
		map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Job id returned by the task call."},
		},
		[]string{"id"},
	)
}

// NewTaskCancelTool builds the task_cancel tool.
func NewTaskCancelTool(parentSessionID string, d SubagentDispatcher) *TaskControlTool {
	return NewTaskControlTool("task_cancel",
		"Cancel a running sub-agent job.",
		parentSessionID, d,
		map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Job id returned by the task call."},
		},
		[]string{"id"},
	)
}

// NewTaskListTool builds the task_list tool.
func NewTaskListTool(parentSessionID string, d SubagentDispatcher) *TaskControlTool {
	return NewTaskControlTool("task_list",
		"List all known sub-agent jobs of this session (running and recently finished).",
		parentSessionID, d,
		map[string]interface{}{},
		[]string{},
	)
}

func (t *TaskControlTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	if err, ok := t.dispatcherOrError(); !ok {
		return err, nil
	}
	id, _ := params["id"].(string)

	switch t.name {
	case "task_output":
		wait, _ := params["wait"].(bool)
		waitSec := 0
		if v, ok := params["wait_seconds"].(float64); ok {
			waitSec = int(v)
		}
		out, err := t.dispatcher.Output(ctx, id, wait, waitSec)
		if err != nil {
			return NewTextResult(fmt.Sprintf("Error: %s", err)), nil
		}
		return NewTextResult(out), nil

	case "task_status":
		info, ok := t.dispatcher.Status(id)
		if !ok {
			return NewTextResult(fmt.Sprintf("Error: unknown sub-agent job %q%s", id, UnknownIDHint(id))), nil
		}
		return NewTextResult(formatJobInfo(info)), nil

	case "task_cancel":
		if !t.dispatcher.Cancel(id) {
			return NewTextResult(fmt.Sprintf("Sub-agent job %s is not running (already finished, canceled, or unknown).", id)), nil
		}
		return NewTextResult(fmt.Sprintf("Cancel requested for sub-agent job %s.", id)), nil

	case "task_list":
		jobs := t.dispatcher.List(t.parentSessionID)
		if len(jobs) == 0 {
			return NewTextResult("No sub-agent jobs for this session."), nil
		}
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&b, "- %s [%s] %s: %q (started %s%s)\n",
				j.ID, j.Status, j.Role, truncateRunes(j.Goal, 100), j.StartedAt,
				jobFinishedSuffix(j))
		}
		return NewTextResult(strings.TrimRight(b.String(), "\n")), nil
	}
	return NewTextResult("Error: unknown control tool"), nil
}

func jobFinishedSuffix(j JobInfo) string {
	if j.FinishedAt != "" {
		return ", finished " + j.FinishedAt
	}
	return ""
}

func formatJobInfo(j JobInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Job %s [%s] role=%s\nGoal: %q\nStarted: %s", j.ID, j.Status, j.Role, j.Goal, j.StartedAt)
	if j.FinishedAt != "" {
		fmt.Fprintf(&b, "\nFinished: %s", j.FinishedAt)
	}
	if j.Status == "failed" && j.Error != "" {
		fmt.Fprintf(&b, "\nError: %s", j.Error)
	}
	if j.Status == "completed" && j.Result != "" {
		fmt.Fprintf(&b, "\nResult (truncated to 500 chars):\n%s", truncateRunes(j.Result, 500))
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
