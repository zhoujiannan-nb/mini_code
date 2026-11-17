package session

import (
	"context"
	"strings"
	"testing"
)


func escalationParams(cmd string) map[string]interface{} {
	return map[string]interface{}{
		"command":             cmd,
		"sandbox_permissions": "danger-full-access",
		"justification":       "需要写入 workspace 外的证书目录，一次即可",
	}
}

func TestSandboxEscalationAsksUser(t *testing.T) {
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("esc", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetMode(s.ID(), ModeCautious); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	ap := &recordingApprover{approved: true, reason: "user approved"}
	mgr.SetApprover(ap)
	gate := s.gateFor(nil)

	if allow, deny := gate(context.Background(), "pwsh", escalationParams("curl.exe -s https://example.com")); !allow {
		t.Fatalf("approved escalation denied: %q", deny)
	}
	if ap.called != 1 {
		t.Fatalf("escalation skipped the user (called=%d)", ap.called)
	}
	if !strings.Contains(ap.last.ArgsJSON, "[sandbox: asking for danger-full-access]") ||
		!strings.Contains(ap.last.ArgsJSON, "需要写入 workspace 外的证书目录") {
		t.Fatalf("approval card must carry the mode and the justification: %q", ap.last.ArgsJSON)
	}

	ap.approved, ap.reason = false, "user denied"
	allow, deny := gate(context.Background(), "pwsh", escalationParams("curl.exe -s https://example.com"))
	if allow || !strings.Contains(deny, "[sandbox:") || !strings.Contains(deny, "Do NOT retry") {
		t.Fatalf("denied escalation: allow=%v deny=%q", allow, deny)
	}
}

func TestSandboxEscalationValidation(t *testing.T) {
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("esc-val", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetMode(s.ID(), ModeCautious); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	ap := &recordingApprover{approved: true, reason: "user approved"}
	mgr.SetApprover(ap)
	gate := s.gateFor(nil)

	if allow, deny := gate(context.Background(), "write", map[string]interface{}{
		"file_path": "a.txt", "sandbox_permissions": "workspace-write",
	}); allow || !strings.Contains(deny, "justification") {
		t.Fatalf("pair without justification: allow=%v deny=%q", allow, deny)
	}
	if allow, deny := gate(context.Background(), "write", map[string]interface{}{
		"file_path": "a.txt", "sandbox_permissions": "read-etc", "justification": "x",
	}); allow || !strings.Contains(deny, "workspace-write") {
		t.Fatalf("unknown mode: allow=%v deny=%q", allow, deny)
	}
	if ap.called != 0 {
		t.Fatalf("malformed pairs must never reach the user (called=%d)", ap.called)
	}

	allow, deny := gate(context.Background(), "pwsh", map[string]interface{}{
		"command": "format c:", "sandbox_permissions": "danger-full-access", "justification": "trust me",
	})
	if allow || !strings.Contains(deny, "does not apply to hard blocks") || ap.called != 0 {
		t.Fatalf("hard block with escalation: allow=%v deny=%q called=%d", allow, deny, ap.called)
	}
}

func TestSandboxEscalationInertInFullMode(t *testing.T) {
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("esc-full", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ap := &recordingApprover{approved: false, reason: "should not be asked"}
	mgr.SetApprover(ap)
	gate := s.gateFor(nil)
	if allow, deny := gate(context.Background(), "pwsh", escalationParams("Get-Date")); !allow {
		t.Fatalf("full mode: %q", deny)
	}
	if _, deny := gate(context.Background(), "pwsh", map[string]interface{}{
		"command": "Get-Date", "sandbox_permissions": "workspace-write",
	}); !strings.Contains(deny, "justification") {
		t.Fatalf("full mode must still validate the pair: %q", deny)
	}
	if ap.called != 0 {
		t.Fatalf("full mode asked the user %d times", ap.called)
	}
}
