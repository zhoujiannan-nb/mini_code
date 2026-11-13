package session

import (
	"context"
	"strings"
	"testing"
	"time"
)

// countNotices returns how many settlement notices (user role, tagged
// "<agent …" by current builds or "<subagent …" by earlier ones) the session's
// persisted history carries.
func countNotices(s *Session) int {
	n := 0
	for _, m := range s.Record().Messages {
		if m.Role == "user" && (strings.Contains(m.GetText(), "<agent ") || strings.Contains(m.GetText(), "<subagent ")) {
			n++
		}
	}
	return n
}

// TestSettleNoticeGoesToTheRunningLoop is the point of routing settlement
// notices through QueueUserMessage: while the parent's task is running the
// notice must reach the loop's per-turn drain (so the model sees it on its next
// turn), be visible to the UI immediately, and appear exactly ONCE in the
// history (the write-ahead copy is the copy the loop later persists).
func TestSettleNoticeGoesToTheRunningLoop(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	mgr.SetActiveTaskCheck(func(id string) bool { return id == parent.ID() }) // parent's loop is live
	delivered := 0
	mgr.SetDeliverHook(func(string, string) { delivered++ }) // idle delivery must NOT be used

	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(context.Context, *Session, string) (string, string, error) {
		return "bg result", "", nil
	}
	if _, err := svc.DispatchBg(parent.ID(), "work", "background job", false); err != nil {
		t.Fatalf("DispatchBg: %v", err)
	}
	waitForNotice(t, mgr, parent.ID(), 1)

	if got := len(parent.DrainUserMessages()); got != 1 {
		t.Fatalf("notice did not reach the loop's per-turn drain (drained %d)", got)
	}
	if got := countNotices(parent); got != 1 {
		t.Fatalf("notice in history = %d, want exactly 1 (duplicated by the write-ahead copy?)", got)
	}
	if delivered != 0 {
		t.Errorf("an idle delivery happened while the parent was running (%d)", delivered)
	}
}

// TestNoNoticeBeforeTheChildFullySettles pins the other half of the contract:
// nothing may be injected into the parent while the sub-agent is still working —
// the parent is told only once the job has actually finished.
func TestNoNoticeBeforeTheChildFullySettles(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	mgr.SetActiveTaskCheck(func(id string) bool { return id == parent.ID() })

	release := make(chan struct{})
	started := make(chan struct{})
	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(context.Context, *Session, string) (string, string, error) {
		close(started)
		<-release
		return "final answer", "", nil
	}
	jobID, err := svc.DispatchBg(parent.ID(), "work", "slow job", false)
	if err != nil {
		t.Fatalf("DispatchBg: %v", err)
	}
	<-started
	time.Sleep(150 * time.Millisecond) // give any premature injection a chance to land

	if got := countNotices(parent); got != 0 {
		release <- struct{}{}
		t.Fatalf("%d notice(s) injected while the child was still running", got)
	}
	if got := len(parent.DrainUserMessages()); got != 0 {
		release <- struct{}{}
		t.Fatalf("%d notice(s) queued before the child settled", got)
	}
	if info, _ := svc.Status(jobID); info.Status != "running" {
		release <- struct{}{}
		t.Fatalf("job should still be running, got %q", info.Status)
	}

	release <- struct{}{}
	waitForNotice(t, mgr, parent.ID(), 1)
	if got := countNotices(parent); got != 1 {
		t.Fatalf("notice count after settling = %d, want 1", got)
	}
	if txt := strings.Join(parent.DrainUserMessages(), "\n"); !strings.Contains(txt, "final answer") {
		t.Errorf("queued notice does not quote the child's result: %q", txt)
	}
}

// TestInjectMessageWakesAnIdleSession: an idle parent is woken up so the
// finished sub-agent's result is digested right away (default, no switch) — the
// notice is dispatched as a fresh task, not silently parked in the history.
func TestInjectMessageWakesAnIdleSession(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	mgr.SetActiveTaskCheck(func(string) bool { return false }) // idle
	type delivered struct{ sessionID, text string }
	got := make([]delivered, 0, 1)
	mgr.SetDeliverHook(func(sid, text string) { got = append(got, delivered{sid, text}) })

	if err := mgr.InjectMessage(parent.ID(), "<subagent id=\"abc12345\" state=\"completed\">Result: done</subagent>"); err != nil {
		t.Fatalf("InjectMessage: %v", err)
	}
	if len(got) != 1 || got[0].sessionID != parent.ID() || !strings.Contains(got[0].text, "Result: done") {
		t.Fatalf("the idle parent was not woken with the notice: %+v", got)
	}
	// The woken task writes the message itself, so injecting must not also append
	// it (that would show the notice twice).
	if n := countNotices(parent); n != 0 {
		t.Errorf("notice appended as well as dispatched (%d copies in history)", n)
	}
	if err := mgr.InjectMessage("does-not-exist", "x"); err == nil {
		t.Error("injecting into an unknown session must fail")
	}
}

// TestInjectMessageWithoutDeliverHook: with no wake path wired (CLI, tests) the
// notice is appended to the history exactly once instead of being lost.
func TestInjectMessageWithoutDeliverHook(t *testing.T) {
	mgr := newTestManager(t)
	parent, err := mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	mgr.SetActiveTaskCheck(func(string) bool { return false }) // idle, and no deliver hook
	if err := mgr.InjectMessage(parent.ID(), "<subagent id=\"abc12345\" state=\"completed\">Result: done</subagent>"); err != nil {
		t.Fatalf("InjectMessage: %v", err)
	}
	if n := countNotices(parent); n != 1 {
		t.Fatalf("notice count = %d, want 1", n)
	}
}

// waitForNotice polls until the parent has the settlement notice (queued or
// persisted) — settling happens on the sub-agent's own goroutine.
func waitForNotice(t *testing.T, mgr *SessionManager, sessionID string, want int) {
	t.Helper()
	s, err := mgr.Get(sessionID)
	if err != nil {
		t.Fatalf("Get(%s): %v", sessionID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if countNotices(s) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("settlement notice never arrived (history: %+v)", s.Record().Messages)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
