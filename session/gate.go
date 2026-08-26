package session

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/tools"
)

// ToolGate decides whether one tool call may execute. It runs inside the
// agent loop, per tool call, before the tool itself is invoked. Returning
// allow=false hands denyMsg to the model as the tool result.
type ToolGate func(ctx context.Context, toolName string, params map[string]interface{}) (allow bool, denyMsg string)

// ApprovalRequest describes a destructive operation that needs user
// confirmation in cautious mode.
type ApprovalRequest struct {
	SessionID    string // the session (possibly a sub-agent) making the call
	ParentID     string // its parent session, when it is a sub-agent
	SessionTitle string
	ToolName     string
	ArgsJSON     string
}

// ApprovalDecision is the outcome of one approval request.
type ApprovalDecision struct {
	Approved bool
	Reason   string // "user approved" | "user denied" | "approval timed out" | "task canceled" | "no approval channel available"
}

// Approver resolves approval requests. Implemented by the approval service
// (wired via SessionManager.SetApprover); it pushes the request to the UI
// and blocks until the user decides, the task is canceled, or it times out.
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) ApprovalDecision
}

// gateFor builds the runtime permission gate for one session:
//
//  1. role check — a tool the agent role does not allow is denied even if
//     the model hallucinates a call for it (permissions previously only
//     filtered the tool schema, never the execution);
//  2. full mode — everything else runs (the shell's hard-block list still
//     applies inside the exec tool itself);
//  3. cautious mode — destructive calls (delete_file, rm/del/... commands)
//     are denied outright when catastrophic, otherwise sent to the approver
//     for a UI confirmation.
//
// The mode is read live on every call, so switching a session's mode while
// a task is running takes effect from the next tool call on.
func (s *Session) gateFor(cfg *agent.AgentConfig) ToolGate {
	return func(ctx context.Context, toolName string, params map[string]interface{}) (bool, string) {
		if cfg != nil && len(cfg.Permissions.Tools) > 0 && !cfg.Permissions.IsToolAllowed(toolName, "") {
			return false, fmt.Sprintf("Error: tool %q is not permitted for agent role %q. Use only the tools available to you.", toolName, s.record.AgentRole)
		}
		if s.manager == nil || s.manager.ModeOf(s.ID()) != ModeCautious {
			return true, ""
		}
		action, _ := classifyDestructive(toolName, params)
		switch action {
		case "block":
			return false, "Error: Command blocked — dangerous pattern detected. This operation is prohibited in all modes. Choose a different approach."
		case "ask":
			decision := s.requestApproval(ctx, toolName, params)
			if !decision.Approved {
				return false, fmt.Sprintf("Error: Operation denied in cautious mode (%s). Do NOT retry this operation. State the limitation to the user and continue with alternatives if any.", decision.Reason)
			}
		}
		return true, ""
	}
}

// requestApproval sends a destructive operation to the approver (UI popup)
// and blocks until it is decided, the task is canceled, or it times out.
func (s *Session) requestApproval(ctx context.Context, toolName string, params map[string]interface{}) ApprovalDecision {
	a := s.manager.approverRef()
	if a == nil {
		return ApprovalDecision{Approved: false, Reason: "no approval channel available"}
	}
	return a.RequestApproval(ctx, ApprovalRequest{
		SessionID:    s.ID(),
		ParentID:     s.record.ParentID,
		SessionTitle: s.record.Title,
		ToolName:     toolName,
		ArgsJSON:     approvalArgs(toolName, params),
	})
}

// classifyDestructive reports how cautious mode treats a tool call:
// ("ask", why) needs user confirmation; ("block", "") is catastrophic and
// denied outright; ("", "") runs without friction.
func classifyDestructive(toolName string, params map[string]interface{}) (action, why string) {
	switch toolName {
	case "delete_file":
		return "ask", "file deletion"
	case "exec":
		cmd, _ := params["command"].(string)
		if cmd == "" {
			return "", ""
		}
		if tools.IsHardBlocked(cmd) {
			return "block", ""
		}
		if tools.IsDestructive(cmd) {
			return "ask", "destructive shell command"
		}
	}
	return "", ""
}

// approvalArgs renders the tool parameters for the approval UI: the raw
// command for exec, compact JSON otherwise.
func approvalArgs(toolName string, params map[string]interface{}) string {
	if toolName == "exec" {
		if cmd, ok := params["command"].(string); ok && cmd != "" {
			return cmd
		}
	}
	b, err := json.Marshal(params)
	if err != nil {
		return fmt.Sprintf("%v", params)
	}
	return string(b)
}
