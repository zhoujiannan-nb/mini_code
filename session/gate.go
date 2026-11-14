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
//     for a UI confirmation. A call carrying dsh's sandbox_permissions +
//     justification pair (see sandboxEscalation) always asks, even when the
//     call itself would run friction-free.
//
// The mode is read live on every call, so switching a session's mode while
// a task is running takes effect from the next tool call on.
//
// Authorization comes ONLY from the UI round-trip in case "ask". Nothing in the
// conversation grants permission: not a user message, and least of all a
// runtime-injected one — sub-agent settlement notices and context-usage notices
// travel on the "user" role because that is the only role every chat template
// honours mid-conversation, so their text must never be mistaken for consent
// (codex encodes the same rule in is_guardian_context_message: "hidden runtime
// context is not user authorization"). Should a future feature want an "the
// user already agreed" shortcut, it has to check an explicit approval flag, not
// history text — TestCautiousGateNeverTrustsConversationText pins this.
func (s *Session) gateFor(cfg *agent.AgentConfig) ToolGate {
	return func(ctx context.Context, toolName string, params map[string]interface{}) (bool, string) {
		if cfg != nil && len(cfg.Permissions.Tools) > 0 && !cfg.Permissions.IsToolAllowed(toolName, "") {
			// The compiled role table only knows core tools; a plugin tool is
			// absent from it by construction, so an absent entry is not a deny.
			// An explicit "deny" from the role still wins.
			if !explicitlyDenied(cfg, toolName) && isPluginTool(toolName) {
				// allowed: fall through to the mode check
			} else {
				return false, fmt.Sprintf("Error: tool %q is not permitted for agent role %q. Use only the tools available to you.", toolName, s.record.AgentRole)
			}
		}
		// dsh's one-shot escalation pair, validated in every mode: the schema
		// promises an enum and a mandatory justification, and a promise the
		// schema makes is a promise the gate enforces. MiniCode runs no OS
		// sandbox (the ledger records the deviation), so "wider access" has
		// exactly one meaning here: ask the user for this one call.
		escMode, just, esc := sandboxEscalation(params)
		if esc {
			if deny := validateEscalation(escMode, just); deny != "" {
				return false, deny
			}
		}
		// The worker 公约 is a contract with every other session of that worker,
		// so the model cannot write it at all: the workers tool only offers
		// note_read, and the sole write path is a human editing it in the
		// sidebar (see web handleWorkersNoteWrite).
		if s.manager == nil || s.manager.ModeOf(s.ID()) != ModeCautious {
			return true, ""
		}
		// A plugin tool runs an external program the host knows nothing about,
		// so in cautious mode it gets the same treatment as a destructive shell
		// command: ask the user first. This is the one place the plugin layer
		// touches the gate, and it only ever makes the gate stricter.
		if isPluginTool(toolName) {
			decision := s.requestApproval(ctx, toolName, params)
			if !decision.Approved {
				return false, fmt.Sprintf("Error: Operation denied in cautious mode (%s). Do NOT retry this operation. State the limitation to the user and continue with alternatives if any.", decision.Reason)
			}
			return true, ""
		}
		action, _ := classifyDestructive(toolName, params)
		if esc {
			// The escalation retry (dsh FsSandboxController): a hard block is
			// a hard block — wider access cannot widen a prohibition, and the
			// model must not shop for a mode that passes.
			if action == "block" {
				return false, "Error: Command blocked — dangerous pattern detected. sandbox_permissions does not apply to hard blocks; no wider mode makes this legal. Choose a different approach."
			}
			// Everything else: the retry asks the user, with the model's own
			// justification front and center. One approval buys one call —
			// dsh's perCall precedence; the session-level wider setting in
			// MiniCode is the full/cautious mode itself.
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

// sandboxEscalation reads dsh's one-shot escalation pair off a tool call's
// parameters (write/edit/pwsh expose them in the schema; a hallucinated call
// elsewhere is treated the same — the pair only ever tightens the gate).
// requested is true as soon as sandbox_permissions is non-empty, so a bad
// pair is denied rather than silently run as a normal call.
func sandboxEscalation(params map[string]interface{}) (mode, justification string, requested bool) {
	mode, _ = params["sandbox_permissions"].(string)
	if strings.TrimSpace(mode) == "" {
		return "", "", false
	}
	justification, _ = params["justification"].(string)
	return strings.TrimSpace(mode), strings.TrimSpace(justification), true
}

// validateEscalation enforces the schema pair: the enum, and the mandatory
// one-sentence justification the schema promises the user gets to read.
func validateEscalation(mode, justification string) string {
	if mode != "workspace-write" && mode != "danger-full-access" {
		return "Error: sandbox_permissions must be \"workspace-write\" or \"danger-full-access\". Drop the parameter and run the call normally."
	}
	if justification == "" {
		return "Error: sandbox_permissions requires a one-sentence justification for the user (why this exact call needs the wider access). Run the call without the parameter instead."
	}
	return ""
}

// isPluginTool reports whether name belongs to an installed, enabled plugin.
// Only the deny / cautious paths call it, so the directory read never happens on
// the hot path of an allowed core tool call.
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

// requestApproval sends a destructive operation to the approver (UI popup)
// and blocks until it is decided, the task is canceled, or it times out.
func (s *Session) requestApproval(ctx context.Context, toolName string, params map[string]interface{}) ApprovalDecision {
	return s.requestApprovalArgs(ctx, toolName, approvalArgs(toolName, params))
}

// requestEscalation is requestApproval for a sandbox escalation retry: the
// approval card leads with the mode being asked for and the model's own
// justification, so the user reads the sentence the schema promised before
// deciding on the wider access.
func (s *Session) requestEscalation(ctx context.Context, toolName string, params map[string]interface{}, mode, justification string) ApprovalDecision {
	args := approvalArgs(toolName, params)
	return s.requestApprovalArgs(ctx, toolName, fmt.Sprintf("[sandbox: asking for %s] %s\n%s", mode, justification, args))
}

// requestApprovalArgs is the shared UI round-trip behind both paths.
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

// classifyDestructive reports how cautious mode treats a tool call:
// ("ask", why) needs user confirmation; ("block", "") is catastrophic and
// denied outright; ("", "") runs without friction.
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

// approvalArgs renders the tool parameters for the approval UI: the raw
// command for pwsh/exec, compact JSON otherwise.
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
