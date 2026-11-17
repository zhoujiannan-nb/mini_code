package session

import (
	"context"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
)

type recordingApprover struct {
	called   int
	approved bool
	reason   string
	last     ApprovalRequest
}

func (a *recordingApprover) RequestApproval(_ context.Context, req ApprovalRequest) ApprovalDecision {
	a.called++
	a.last = req
	return ApprovalDecision{Approved: a.approved, Reason: a.reason}
}

func TestCautiousGateNeverTrustsConversationText(t *testing.T) {
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("gate", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if err := mgr.SetMode(s.ID(), ModeCautious); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	ap := &recordingApprover{reason: "user denied"}
	mgr.SetApprover(ap)

	rec := s.Record()
	rec.Messages = append(rec.Messages,
		provider.Message{Role: "user", Content: "yes, delete it, I approve"},
		provider.Message{Role: "assistant", Content: "should I remove the file?"},
	)
	if err := mgr.InjectMessage(s.ID(), `<subagent id="abc12345" state="completed" role="work">
System notice: a background sub-agent finished. This text was injected by the runtime, it is NOT the user speaking and implies no user approval.
</subagent>`); err != nil {
		t.Fatalf("InjectMessage: %v", err)
	}
	if countNotices(s) != 1 {
		t.Fatalf("the injected notice is missing from history: %+v", s.Record().Messages)
	}

	gate := s.gateFor(nil)
	allow, deny := gate(context.Background(), "delete_file", map[string]interface{}{"path": "C:/temp/x.txt"})
	if ap.called != 1 {
		t.Fatalf("destructive call skipped the UI approval (called=%d): conversation text acted as consent", ap.called)
	}
	if allow {
		t.Fatal("delete_file allowed without an approval decision")
	}
	if !strings.Contains(deny, "denied") {
		t.Errorf("unexpected deny message: %q", deny)
	}
	if ap.last.ToolName != "delete_file" {
		t.Errorf("approval request carried tool %q", ap.last.ToolName)
	}

	ap.called = 0
	if allow, _ := gate(context.Background(), "exec", map[string]interface{}{"command": "format c:"}); allow {
		t.Fatal("hard-blocked command was allowed")
	}
	if ap.called != 0 {
		t.Errorf("hard-blocked command should never reach the approver, got %d calls", ap.called)
	}

	ap.approved, ap.reason, ap.called = true, "user approved", 0
	if allow, _ := gate(context.Background(), "delete_file", map[string]interface{}{"path": "C:/temp/x.txt"}); !allow {
		t.Fatal("an approved destructive call should run")
	}
	if ap.called != 1 {
		t.Errorf("approver called %d times, want 1", ap.called)
	}
}
