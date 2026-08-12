package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/mini_code/agent"
)

type SessionPrompter interface {
	Prompt(ctx context.Context, goal string, maxTurns int) (string, error)
}

type SessionCreator interface {
	CreateSession(title, agentRole, workDir string, parentID *string) (SessionPrompter, error)
}

type TaskTool struct {
	executor        SessionCreator
	parentSessionID string
	workDir         string
	allowDispatch   bool
}

func NewTaskTool() *TaskTool {
	return &TaskTool{}
}

func (t *TaskTool) SetSessionContext(executor SessionCreator, parentSessionID, workDir string) {
	t.executor = executor
	t.parentSessionID = parentSessionID
	t.workDir = workDir
	t.allowDispatch = true
}

func (t *TaskTool) Name() string { return "task" }
func (t *TaskTool) Description() string {
	agents := agent.ListAllAgents()
	var lines []string
	for name, desc := range agents {
		lines = append(lines, fmt.Sprintf("  - %s: %s", name, desc))
	}
	return fmt.Sprintf(
		"Dispatch a sub-task to a dedicated sub-agent.\n\nAvailable sub-agents:\n%s\n\nUsage: specify sub_agent name and a clear goal.",
		strings.Join(lines, "\n"),
	)
}
func (t *TaskTool) IsHidden() bool { return false }
func (t *TaskTool) Parameters() map[string]interface{} {
	agents := agent.ListAllAgents()
	var names []string
	for name := range agents {
		names = append(names, name)
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sub_agent": map[string]interface{}{
				"type":        "string",
				"description": fmt.Sprintf("Name of sub-agent. Available: %s", strings.Join(names, ", ")),
			},
			"goal": map[string]interface{}{
				"type":        "string",
				"description": "Detailed task description for the sub-agent.",
			},
		},
		"required": []string{"sub_agent", "goal"},
	}
}

func (t *TaskTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	subAgent, _ := params["sub_agent"].(string)
	goal, _ := params["goal"].(string)

	if subAgent == "" {
		return NewTextResult("Error: missing required parameter 'sub_agent'"), nil
	}
	if goal == "" {
		return NewTextResult("Error: missing required parameter 'goal'"), nil
	}
	if !t.allowDispatch {
		return NewTextResult("Error: sub-agents are not allowed to dispatch further sub-tasks. Only the root agent can use the task tool."), nil
	}
	if t.executor == nil {
		return NewTextResult("Error: session context not initialized."), nil
	}

	agents := agent.ListAllAgents()
	if _, ok := agents[subAgent]; !ok {
		var names []string
		for n := range agents {
			names = append(names, n)
		}
		return NewTextResult(fmt.Sprintf("Error: unknown sub-agent '%s'. Available: %s", subAgent, strings.Join(names, ", "))), nil
	}

	session, err := t.executor.CreateSession(goal[:min(50, len(goal))], subAgent, t.workDir, &t.parentSessionID)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: failed to create sub-session: %s", err)), nil
	}

	result, err := session.Prompt(ctx, goal, 999)
	if err != nil {
		return NewTextResult(fmt.Sprintf("[Sub-agent '%s' failed] Error: %s", subAgent, err)), nil
	}
	return NewTextResult(fmt.Sprintf("[Sub-agent '%s' completed]\n\n%s", subAgent, result)), nil
}
