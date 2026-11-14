package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/user/mini_code/agent"
)

// RootAgentName is the one name a sub-agent may never take: it always means
// "the conversation you are talking to". DSH reserves it for the same reason —
// a child that could call itself "lead" would make every address ambiguous.
const RootAgentName = "lead"

// sortedAgentNames returns the sub-agent role names in a stable (sorted) order.
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

// JobInfo is the JSON-friendly view of one sub-agent (its most recent run),
// used by the agent tools and the web API.
type JobInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role"`
	Goal        string `json:"goal"`
	Status      string `json:"status"` // running | completed | failed | canceled
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

// AgentSpec is one dispatch request, as it arrives from the model.
type AgentSpec struct {
	Name        string // "" = pick the first free agent-N
	Description string // 3-5 words, what the caller will see in list_agents
	Role        string // agent role (build / work / custom); "" = work
	Goal        string
	Fork        bool   // start from the caller's conversation
	Model       string // optional model key for this child only
	Background  bool
}

// AgentRun is what a dispatch produced right now: an admitted background run,
// or a finished foreground one.
type AgentRun struct {
	Name    string
	ChildID string
	Status  string // running | completed | failed | canceled
	Result  string
	Error   string
}

// AgentDispatcher is what the agent tools need from the sub-agent service
// (implemented by session.SubagentService).
type AgentDispatcher interface {
	// Dispatch starts a sub-agent and returns as soon as it is admitted
	// (background) or has finished (foreground).
	Dispatch(ctx context.Context, parentID string, spec AgentSpec) (AgentRun, error)
	// Send delivers a message to an existing sub-agent: injected at its next
	// turn when it is running, or waking it with a new task when it is idle.
	Send(parentID, agent, message string) (mode, name string, err error)
	// Resolve maps a name (or session id) to the child session and its name.
	Resolve(parentID, agent string) (childID, name string, err error)
	Output(ctx context.Context, id string, wait bool, waitSeconds int) (string, error)
	Status(id string) (JobInfo, bool)
	Cancel(id string) bool
	List(parentID string) []JobInfo
}

// agentToolDocs is shared by every agent tool: one paragraph the model reads
// once, in the tool that starts agents.
const agentToolDocs = "A sub-agent is a separate conversation that runs on its own, with the caller's tool permissions (minus the agent tools) and the caller's permission mode. " +
	"Each one is addressable by `name` for as long as the app runs: send_message reaches it again after it finishes, and list_agents shows what exists."

// AgentTool starts a sub-agent (the DSH `subagent` shape).
type AgentTool struct {
	parentSessionID string
	dispatcher      AgentDispatcher
}

func NewAgentTool() *AgentTool { return &AgentTool{} }

func (t *AgentTool) SetSessionContext(parentSessionID string, dispatcher AgentDispatcher) {
	t.parentSessionID = parentSessionID
	t.dispatcher = dispatcher
}

func (t *AgentTool) Name() string { return "subagent" }
func (t *AgentTool) IsHidden() bool { return false }

// Description/Parameters: dsh's verbatim "subagent" surface. dsh's subagent
// is always asynchronous — it returns the name and the completion notice
// arrives later — so the exposed schema carries no run_in_background and
// Execute forces one. The historical foreground path stays reachable for
// in-process callers (tests) through the unlisted parameter.
func (t *AgentTool) Description() string                { return dshDesc("subagent") }
func (t *AgentTool) Parameters() map[string]interface{} { return dshParams("subagent") }

func (t *AgentTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return t.dispatch(ctx, params, false, true)
}

// dispatch is the shared body of subagent and subagent_fork.
func (t *AgentTool) dispatch(ctx context.Context, params map[string]interface{}, forceFork, forceAsync bool) (*ToolResult, error) {
	prompt, _ := params["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return NewTextResult("Error: missing required parameter 'prompt'"), nil
	}
	description, _ := params["description"].(string)
	if strings.TrimSpace(description) == "" {
		return NewTextResult("Error: missing required parameter 'description' (3-5 words, shown by list_agents)"), nil
	}
	role, _ := params["role"].(string)
	if role == "" {
		role = "work"
	}
	if _, ok := agent.ListAllAgents()[role]; !ok {
		return NewTextResult(fmt.Sprintf("Error: unknown role %q. Available: %s", role, strings.Join(sortedAgentNames(), ", "))), nil
	}
	fork, _ := params["fork"].(bool)
	if forceFork {
		fork = true
	}
	runBg, _ := params["run_in_background"].(bool)
	if forceAsync {
		runBg = true
	}
	model, _ := params["model"].(string)
	name, _ := params["name"].(string)
	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), nil
	}

	run, err := t.dispatcher.Dispatch(ctx, t.parentSessionID, AgentSpec{
		Name:        name,
		Description: strings.TrimSpace(description),
		Role:        role,
		Goal:        prompt,
		Fork:        fork,
		Model:       strings.TrimSpace(model),
		Background:  runBg,
	})
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: could not start the sub-agent: %s", err)), nil
	}

	if runBg {
		return NewTextResult(fmt.Sprintf(
			"Sub-agent %q started (role %s, session %s). It runs independently while you continue working.\n"+
				"- A settlement message with its result arrives when it finishes; it will wake you if you have stopped.\n"+
				"- send_message(agent=%q, message=...) reaches it while it runs (delivered at its next turn) or wakes it afterwards.\n"+
				"- list_agents() shows every agent's state; wait_agent blocks until the next input (a settlement notice, an agent message, user steering) arrives.\n"+
			"Do NOT sleep in a loop, poll for progress, or duplicate its work; avoid editing the same files it is working on. "+
			"Either continue with non-overlapping work, or briefly tell the user what you launched and end your turn.",
			run.Name, role, run.ChildID, run.Name)), nil
	}

	switch run.Status {
	case "completed":
		return NewTextResult(fmt.Sprintf("[Sub-agent %q completed (session %s)]\n\n%s\n\n(Send it another message any time with send_message.)", run.Name, run.ChildID, run.Result)), nil
	case "canceled":
		return NewTextResult(fmt.Sprintf("[Sub-agent %q was interrupted] The task was canceled.", run.Name)), nil
	default:
		return NewTextResult(fmt.Sprintf("[Sub-agent %q failed] %s", run.Name, run.Error)), nil
	}
}

// SubagentForkTool is dsh's subagent_fork: the same dispatch, seeded with
// this conversation's completed turns (MiniCode's fork=true).
type SubagentForkTool struct{ inner *AgentTool }

func NewSubagentForkTool(parentSessionID string, dispatcher AgentDispatcher) *SubagentForkTool {
	inner := NewAgentTool()
	inner.SetSessionContext(parentSessionID, dispatcher)
	return &SubagentForkTool{inner: inner}
}

func (t *SubagentForkTool) Name() string                       { return "subagent_fork" }
func (t *SubagentForkTool) Description() string                { return dshDesc("subagent_fork") }
func (t *SubagentForkTool) Parameters() map[string]interface{} { return dshParams("subagent_fork") }
func (t *SubagentForkTool) IsHidden() bool                     { return false }

func (t *SubagentForkTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return t.inner.dispatch(ctx, params, true, true)
}

// SendMessageTool delivers a message to an existing sub-agent.
type SendMessageTool struct {
	parentSessionID string
	dispatcher      AgentDispatcher
}

func NewSendMessageTool(parentSessionID string, dispatcher AgentDispatcher) *SendMessageTool {
	return &SendMessageTool{parentSessionID: parentSessionID, dispatcher: dispatcher}
}

func (t *SendMessageTool) Name() string { return "send_message" }

// Description/Parameters: dsh's verbatim surface.
func (t *SendMessageTool) Description() string                { return dshDesc("send_message") }
func (t *SendMessageTool) IsHidden() bool                     { return false }
func (t *SendMessageTool) Parameters() map[string]interface{} { return dshParams("send_message") }

func (t *SendMessageTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	agentRef, _ := params["agent"].(string)
	message, _ := params["message"].(string)
	if strings.TrimSpace(agentRef) == "" || strings.TrimSpace(message) == "" {
		return NewTextResult("Error: both 'agent' and 'message' are required"), nil
	}
	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), nil
	}
	mode, name, err := t.dispatcher.Send(t.parentSessionID, agentRef, message)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s%s", err, unknownAgentHint(t.parentSessionID, t.dispatcher))), nil
	}
	if mode == "steer" {
		return NewTextResult(fmt.Sprintf("Delivered to %q; it is running and will see this message at its next turn. Its reply arrives as a separate message.", name)), nil
	}
	return NewTextResult(fmt.Sprintf("Woke %q with this message. It is running again and its reply arrives as a separate message.", name)), nil
}

// ListAgentsTool shows the agent tree.
type ListAgentsTool struct {
	parentSessionID string
	dispatcher      AgentDispatcher
}

func NewListAgentsTool(parentSessionID string, dispatcher AgentDispatcher) *ListAgentsTool {
	return &ListAgentsTool{parentSessionID: parentSessionID, dispatcher: dispatcher}
}

func (t *ListAgentsTool) Name() string { return "list_agents" }
func (t *ListAgentsTool) Description() string {
	return "List the agents of this conversation with their status: running means a turn is executing right now, inactive means it is not (which says nothing about whether its last run succeeded). Use it before send_message when you are unsure of a name."
}
func (t *ListAgentsTool) IsHidden() bool { return false }
func (t *ListAgentsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	}
}

func (t *ListAgentsTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	var b strings.Builder
	b.WriteString("- " + RootAgentName + "  [this conversation]\n")
	if t.dispatcher != nil {
		for _, j := range t.dispatcher.List(t.parentSessionID) {
			state := "inactive"
			if j.Status == "running" {
				state = "running"
			}
			fmt.Fprintf(&b, "- %s  [%s]", j.Name, state)
			if j.Description != "" {
				fmt.Fprintf(&b, "  %q", j.Description)
			}
			if j.Role != "" {
				fmt.Fprintf(&b, "  role=%s", j.Role)
			}
			if j.Status != "running" && j.Status != "" && j.Status != "completed" {
				fmt.Fprintf(&b, "  last=%s", j.Status)
			}
			fmt.Fprintf(&b, "  goal: %q", truncateRunes(j.Goal, 120))
			if j.FinishedAt != "" {
				fmt.Fprintf(&b, " (finished %s)", j.FinishedAt)
			}
			if j.Result != "" {
				fmt.Fprintf(&b, "  result: %q", truncateRunes(oneLine(j.Result), 200))
			}
			b.WriteString("\n")
		}
	}
	return NewTextResult(strings.TrimRight(b.String(), "\n")), nil
}

// InputWaiter is the session-side half of wait_agent, implemented by
// *session.Session: it reports whether an input (user steering, a sub-agent
// message, a job settlement notice — in MiniCode they all arrive through the
// injection queue) is already waiting, and blocks for the next one without
// consuming it.
type InputWaiter interface {
	HasPendingInput() bool
	WaitForInput(ctx context.Context, timeout time.Duration) bool
}

// WaitAgentTool is dsh's wait_agent: block until the next input the agent
// could process arrives — a sub-agent message, a settlement notice, user
// steering — or the timeout fires. dsh's schema exposes only timeout_ms, and
// MiniCode honours that: with no agent given the tool waits on this session's
// injection queue. The historical wait-for-one-agent's-result behaviour stays
// reachable through the unlisted `agent` parameter (dsh-faithful surfaces
// never send it; tests and old transcripts do).
type WaitAgentTool struct {
	parentSessionID string
	dispatcher      AgentDispatcher
	inputs          InputWaiter
}

func NewWaitAgentTool(parentSessionID string, dispatcher AgentDispatcher) *WaitAgentTool {
	return &WaitAgentTool{parentSessionID: parentSessionID, dispatcher: dispatcher}
}

// SetInputWaiter wires the session's injection-queue doorbell. Without it the
// no-agent mode degrades to the honest "not wired" error instead of blocking
// on nothing.
func (t *WaitAgentTool) SetInputWaiter(w InputWaiter) { t.inputs = w }

func (t *WaitAgentTool) Name() string { return "wait_agent" }

// Description/Parameters: dsh's verbatim surface.
func (t *WaitAgentTool) Description() string                { return dshDesc("wait_agent") }
func (t *WaitAgentTool) IsHidden() bool                     { return false }
func (t *WaitAgentTool) Parameters() map[string]interface{} { return dshParams("wait_agent") }

// waitAgentTimeoutMS reads dsh's timeout_ms (schema default 30000, bounds
// 10000..3600000) with the historical `timeout` (seconds) kept as a hidden
// alias.
func waitAgentTimeoutMS(params map[string]interface{}) int {
	if v, ok := params["timeout_ms"].(float64); ok && v > 0 {
		return int(v)
	}
	if v, ok := params["timeout"].(float64); ok && v > 0 {
		return int(v * 1000)
	}
	return 30000
}

func clampTimeoutMS(ms int) time.Duration {
	if ms < 10000 {
		ms = 10000
	}
	if ms > 3600000 {
		ms = 3600000
	}
	return time.Duration(ms) * time.Millisecond
}

func (t *WaitAgentTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	timeout := clampTimeoutMS(waitAgentTimeoutMS(params))

	// dsh mode: wait for the next input to this session. The input is not
	// consumed — the loop drains it at the start of the next turn, so the
	// model sees the content itself right after this tool result.
	agentRef, _ := params["agent"].(string)
	if strings.TrimSpace(agentRef) == "" {
		if t.inputs == nil {
			return NewTextResult("Error: wait_agent is not wired to this session's input queue in this build."), nil
		}
		if t.inputs.HasPendingInput() {
			return NewTextResult("An input is already waiting. It enters your conversation as the next user message."), nil
		}
		if !t.inputs.WaitForInput(ctx, timeout) {
			if ctx.Err() != nil {
				return NewTextResult("Wait aborted: the task was interrupted."), nil
			}
			return NewTextResult(fmt.Sprintf("No new input within %s. Nothing is waiting; decide what to do next.", timeout.Truncate(time.Second))), nil
		}
		return NewTextResult("An input has arrived. It enters your conversation as the next user message — read it there before deciding your next step."), nil
	}

	// Historical mode: wait for one agent's current run and return its reply.
	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), nil
	}
	childID, name, err := t.dispatcher.Resolve(t.parentSessionID, agentRef)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s%s", err, unknownAgentHint(t.parentSessionID, t.dispatcher))), nil
	}
	out, err := t.dispatcher.Output(ctx, childID, true, int(timeout/time.Second))
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: agent %q: %s", name, err)), nil
	}
	return NewTextResult(out), nil
}

// CancelAgentTool stops one running agent.
type CancelAgentTool struct {
	parentSessionID string
	dispatcher      AgentDispatcher
}

func NewCancelAgentTool(parentSessionID string, dispatcher AgentDispatcher) *CancelAgentTool {
	return &CancelAgentTool{parentSessionID: parentSessionID, dispatcher: dispatcher}
}

func (t *CancelAgentTool) Name() string { return "cancel_agent" }
func (t *CancelAgentTool) Description() string {
	return "Stop an agent that is running right now. Its conversation and history are kept, so send_message can wake it again later."
}
func (t *CancelAgentTool) IsHidden() bool { return false }
func (t *CancelAgentTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent": map[string]interface{}{
				"type":        "string",
				"description": "The agent's name or session id.",
			},
		},
		"required": []string{"agent"},
	}
}

func (t *CancelAgentTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	agentRef, _ := params["agent"].(string)
	if strings.TrimSpace(agentRef) == "" {
		return NewTextResult("Error: 'agent' is required"), nil
	}
	if t.dispatcher == nil {
		return NewTextResult("Error: sub-agent service not available in this session."), nil
	}
	childID, name, err := t.dispatcher.Resolve(t.parentSessionID, agentRef)
	if err != nil {
		return NewTextResult(fmt.Sprintf("Error: %s%s", err, unknownAgentHint(t.parentSessionID, t.dispatcher))), nil
	}
	if !t.dispatcher.Cancel(childID) {
		return NewTextResult(fmt.Sprintf("Agent %q is not running (idle, already finished, or already stopped).", name)), nil
	}
	return NewTextResult(fmt.Sprintf("Stopped agent %q.", name)), nil
}

// unknownAgentHint names the agents that do exist, so a typo costs one retry
// instead of a list_agents round trip.
func unknownAgentHint(parentID string, d AgentDispatcher) string {
	if d == nil {
		return ""
	}
	rows := d.List(parentID)
	if len(rows) == 0 {
		return " (no sub-agents in this conversation yet)"
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return " Known agents: " + strings.Join(names, ", ")
}

// oneLine collapses a multi-line result into one line for list_agents.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
