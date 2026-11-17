package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/plugin"
	"github.com/user/mini_code/tools"
)

type ToolGate func(ctx context.Context, toolName string, params map[string]interface{}) (allow bool, denyMsg string)

type ApprovalRequest struct {
	SessionID    string
	ParentID     string
	SessionTitle string
	ToolName     string
	ArgsJSON     string
}

type ApprovalDecision struct {
	Approved bool
	Reason   string
}

type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) ApprovalDecision
}

func (s *Session) gateFor(cfg *agent.AgentConfig) ToolGate {
	return func(ctx context.Context, toolName string, params map[string]interface{}) (bool, string) {
		if cfg != nil && len(cfg.Permissions.Tools) > 0 && !cfg.Permissions.IsToolAllowed(toolName, "") {
			if !explicitlyDenied(cfg, toolName) && !cfg.DenyPluginTools && isPluginTool(toolName) {
			} else {
				return false, fmt.Sprintf("Error: tool %q is not permitted for agent role %q. Use only the tools available to you.", toolName, s.record.AgentRole)
			}
		}
		escMode, just, esc := sandboxEscalation(params)
		if esc {
			if deny := validateEscalation(escMode, just); deny != "" {
				return false, deny
			}
		}
		if s.manager == nil || s.manager.ModeOf(s.ID()) != ModeCautious {
			return true, ""
		}
		if isPluginTool(toolName) {
			decision := s.requestApproval(ctx, toolName, params)
			if !decision.Approved {
				return false, fmt.Sprintf("Error: Operation denied in cautious mode (%s). Do NOT retry this operation. State the limitation to the user and continue with alternatives if any.", decision.Reason)
			}
			return true, ""
		}
		action, _ := classifyDestructive(toolName, params)
		if esc {
			if action == "block" {
				return false, "Error: Command blocked — dangerous pattern detected. sandbox_permissions does not apply to hard blocks; no wider mode makes this legal. Choose a different approach."
			}
			decision := s.requestEscalation(ctx, toolName, params, escMode, just)
			if !decision.Approved {
				return false, fmt.Sprintf("[sandbox: the user did not grant %s for this call (%s). Do NOT retry it — not with the same arguments, not without the wider access. Explain the blocked step to the user instead.]", escMode, decision.Reason)
			}
			return true, ""
		}
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

func sandboxEscalation(params map[string]interface{}) (mode, justification string, requested bool) {
	mode, _ = params["sandbox_permissions"].(string)
	if strings.TrimSpace(mode) == "" {
		return "", "", false
	}
	justification, _ = params["justification"].(string)
	return strings.TrimSpace(mode), strings.TrimSpace(justification), true
}

func validateEscalation(mode, justification string) string {
	if mode != "workspace-write" && mode != "danger-full-access" {
		return "Error: sandbox_permissions must be \"workspace-write\" or \"danger-full-access\". Drop the parameter and run the call normally."
	}
	if justification == "" {
		return "Error: sandbox_permissions requires a one-sentence justification for the user (why this exact call needs the wider access). Run the call without the parameter instead."
	}
	return ""
}

func isPluginTool(name string) bool {
	for _, p := range plugin.Load() {
		for _, t := range p.Manifest.Tools {
			if t.Name == name {
				return true
			}
		}
	}
	return false
}

func (s *Session) requestApproval(ctx context.Context, toolName string, params map[string]interface{}) ApprovalDecision {
	return s.requestApprovalArgs(ctx, toolName, approvalArgs(toolName, params))
}

func (s *Session) requestEscalation(ctx context.Context, toolName string, params map[string]interface{}, mode, justification string) ApprovalDecision {
	args := approvalArgs(toolName, params)
	return s.requestApprovalArgs(ctx, toolName, fmt.Sprintf("[sandbox: asking for %s] %s\n%s", mode, justification, args))
}

func (s *Session) requestApprovalArgs(ctx context.Context, toolName, argsJSON string) ApprovalDecision {
	a := s.manager.approverRef()
	if a == nil {
		return ApprovalDecision{Approved: false, Reason: "no approval channel available"}
	}
	return a.RequestApproval(ctx, ApprovalRequest{
		SessionID:    s.ID(),
		ParentID:     s.record.ParentID,
		SessionTitle: s.record.Title,
		ToolName:     toolName,
		ArgsJSON:     argsJSON,
	})
}

func classifyDestructive(toolName string, params map[string]interface{}) (action, why string) {
	switch toolName {
	case "delete_file":
		return "ask", "file deletion"
	case "pwsh", "exec":
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

func approvalArgs(toolName string, params map[string]interface{}) string {
	if toolName == "pwsh" || toolName == "exec" {
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
