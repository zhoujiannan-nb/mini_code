package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/provider"
)

// TestClassifyDestructive verifies the cautious-mode classification of tool
// calls: delete_file always asks; exec commands are hard-blocked when
// catastrophic, ask when merely destructive, and pass otherwise.
func TestClassifyDestructive(t *testing.T) {
	cases := []struct {
		tool   string
		params map[string]interface{}
		action string
	}{
		{"delete_file", map[string]interface{}{"path": "C:/x.txt"}, "ask"},
		{"exec", map[string]interface{}{"command": "rm -rf /"}, "block"},
		{"exec", map[string]interface{}{"command": "del /f C:\\x.txt"}, "block"},
		{"exec", map[string]interface{}{"command": "rmdir /s build"}, "block"},
		{"exec", map[string]interface{}{"command": "format c:"}, "block"},
		{"exec", map[string]interface{}{"command": "del C:\\temp\\x.txt"}, "ask"},
		{"exec", map[string]interface{}{"command": "erase x.txt"}, "ask"},
		{"exec", map[string]interface{}{"command": "rd C:\\temp"}, "ask"},
		{"exec", map[string]interface{}{"command": "Remove-Item C:\\x"}, "ask"},
		{"exec", map[string]interface{}{"command": "dir"}, ""},
		{"exec", map[string]interface{}{"command": "go build ./..."}, ""},
		{"exec", map[string]interface{}{"command": "type del.txt"}, ""}, // file name, not the command
		{"exec", map[string]interface{}{"command": ""}, ""},
		{"write_file", map[string]interface{}{"path": "C:/x.txt"}, ""},
		{"read_file", map[string]interface{}{"path": "C:/x.txt"}, ""},
	}
	for _, c := range cases {
		action, _ := classifyDestructive(c.tool, c.params)
		if action != c.action {
			t.Errorf("classifyDestructive(%s, %v) = %q, want %q", c.tool, c.params, action, c.action)
		}
	}
}

// newTestManager builds a SessionManager over a temp DB with a nil model
// client (tests inject PromptFunc, so no model calls are made).
func newTestManager(t *testing.T) *SessionManager {
	t.Helper()
	mgr, err := NewSessionManager(nil, t.TempDir()+"/agent.db")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	return mgr
}

// TestSubagentDispatchSyncFork verifies a foreground dispatch: the child
// session is created under the parent, the parent's cautious mode is
// snapshotted into the child, and fork=true copies the parent's conversation
// (minus its system message) as the child's starting context.
func TestSubagentDispatchSyncFork(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if err := mgr.SetMode(parent.ID(), ModeCautious); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	parent.Record().Messages = []provider.Message{
		{Role: "system", Content: "parent system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	if err := mgr.GetStore().Update(parent.Record()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(ctx context.Context, s *Session, goal string) (string, string, error) {
		return "child result", "", nil
	}

	content, childID, err := svc.DispatchSync(context.Background(), parent.ID(), "work", "do the thing", true)
	if err != nil {
		t.Fatalf("DispatchSync: %v", err)
	}
	if content != "child result" {
		t.Fatalf("expected child result, got %q", content)
	}
	child, err := mgr.Get(childID)
	if err != nil || child == nil {
		t.Fatalf("child session missing: %v", err)
	}
	if child.ParentID() != parent.ID() {
		t.Fatalf("child parent = %q, want %q", child.ParentID(), parent.ID())
	}
	if got := mgr.ModeOf(child.ID()); got != ModeCautious {
		t.Fatalf("child mode = %q, want cautious (snapshot of parent)", got)
	}
	// fork: parent history minus the system message
	msgs := child.Record().Messages
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].GetText() != "hello" || msgs[1].Role != "assistant" {
		t.Fatalf("forked history mismatch: %+v", msgs)
	}
	// the job is recorded as completed
	info, ok := svc.Status(childID)
	if !ok || info.Status != "completed" || info.Result != "child result" {
		t.Fatalf("job status = %+v (ok=%v), want completed with result", info, ok)
	}
}

// TestSubagentDispatchBgSettle verifies a background dispatch: it returns
// immediately, the job settles asynchronously, and a settlement notice is
// injected into the parent's conversation.
func TestSubagentDispatchBgSettle(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}

	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(ctx context.Context, s *Session, goal string) (string, string, error) {
		time.Sleep(100 * time.Millisecond)
		return "bg result", "", nil
	}

	jobID, err := svc.DispatchBg(parent.ID(), "work", "background job", false)
	if err != nil {
		t.Fatalf("DispatchBg: %v", err)
	}
	// returns immediately: the job is still running right after dispatch
	if info, ok := svc.Status(jobID); !ok || info.Status != "running" {
		t.Fatalf("expected running right after dispatch, got %+v", info)
	}

	// wait for the job to settle
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, _ := svc.Status(jobID)
		if info.Status != "running" {
			if info.Status != "completed" {
				t.Fatalf("job settled as %q: %s", info.Status, info.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not settle in time")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// the settlement notice landed in the parent's conversation
	found := false
	for _, m := range parent.Record().Messages {
		if m.Role == "user" && strings.Contains(m.GetText(), "<agent ") && strings.Contains(m.GetText(), "bg result") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("settlement notice missing from parent conversation: %+v", parent.Record().Messages)
	}
}

// TestSubagentCancel verifies that Cancel stops a running background job and
// the job settles as canceled (the child's interrupt status is what the
// real Prompt path produces on context cancellation).
func TestSubagentCancel(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}

	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(ctx context.Context, s *Session, goal string) (string, string, error) {
		<-ctx.Done()
		s.updateStatus("interrupted") // what Session.Prompt does on cancel
		return "", "", nil
	}

	jobID, err := svc.DispatchBg(parent.ID(), "work", "long job", false)
	if err != nil {
		t.Fatalf("DispatchBg: %v", err)
	}
	if !svc.Cancel(jobID) {
		t.Fatal("Cancel reported no running job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, _ := svc.Status(jobID)
		if info.Status != "running" {
			if info.Status != "canceled" {
				t.Fatalf("job settled as %q, want canceled", info.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled job did not settle in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// a second cancel finds nothing running
	if svc.Cancel(jobID) {
		t.Fatal("second Cancel should report no running job")
	}
}

// TestSubagentConcurrencyLimit verifies the per-parent cap on concurrent
// background jobs.
func TestSubagentConcurrencyLimit(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}

	release := make(chan struct{})
	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(ctx context.Context, s *Session, goal string) (string, string, error) {
		select {
		case <-ctx.Done():
			s.updateStatus("interrupted")
			return "", "", nil
		case <-release:
			return "done", "", nil
		}
	}

	var ids []string
	for i := 0; i < maxConcurrentBg; i++ {
		id, err := svc.DispatchBg(parent.ID(), "work", "job", false)
		if err != nil {
			t.Fatalf("DispatchBg %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if _, err := svc.DispatchBg(parent.ID(), "work", "job", false); err == nil {
		t.Fatal("expected the concurrency cap to reject the fifth job")
	}
	// canceling one frees a slot
	if !svc.Cancel(ids[0]) {
		t.Fatal("Cancel failed")
	}
	time.Sleep(50 * time.Millisecond) // let the canceled job settle
	if _, err := svc.DispatchBg(parent.ID(), "work", "job", false); err != nil {
		t.Fatalf("expected a free slot after cancel: %v", err)
	}
	close(release)
}
