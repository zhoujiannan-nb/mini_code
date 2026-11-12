package web

import (
	"context"
	"strings"
	"testing"
)

// A background sub-agent runs inside its OWN child session, so while it works
// the parent session is idle: the list and the session view must keep
// active=false (the flag the composer locks on) and report the live work in
// children_active instead.
//
// Regression: both used to travel in `active`, and the front end maps active →
// "this session has a task" → hide the send button and refuse send(). The
// result was that dispatching a background sub-agent locked the input box, and
// the user could not talk to the main agent again until the child finished.
func TestBgSubagentLeavesParentActiveFalse(t *testing.T) {
	be, srv := newTestWorkerBackend(t)

	parent, err := be.mgr.CreateSessionRecord("parent", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	pid := parent.ID()
	child, err := be.mgr.CreateSessionRecord("child", "work", t.TempDir(), &pid)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	be.hub.RegisterExternalTask(child.ID(), cancel) // = a running background job

	var list []SessionSummary
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &list)
	var got *SessionSummary
	for i := range list {
		if list[i].SessionID == pid {
			got = &list[i]
		}
	}
	if got == nil {
		t.Fatal("the parent session is not in /api/sessions")
	}
	if got.Active {
		t.Error("active = true while only a background sub-agent runs: the composer would lock")
	}
	if !got.ChildrenActive {
		t.Error("children_active = false: the sidebar dot and the stop button would go dark")
	}

	var detail struct {
		Session SessionSummary `json:"session"`
	}
	workerReq(t, "GET", srv.URL+"/api/sessions/"+pid, "", &detail)
	if detail.Session.Active || !detail.Session.ChildrenActive {
		t.Errorf("session view reads active=%v children_active=%v; want false/true",
			detail.Session.Active, detail.Session.ChildrenActive)
	}

	// Stopping from the parent's view still reaches the child (the parent has no
	// task of its own), and the response says so instead of claiming there was
	// nothing to stop.
	var cr struct {
		Canceled         bool `json:"canceled"`
		ChildrenCanceled int  `json:"children_canceled"`
	}
	workerReq(t, "POST", srv.URL+"/api/sessions/"+pid+"/cancel", "", &cr)
	if cr.Canceled {
		t.Error("canceled = true: the parent has no task of its own")
	}
	if cr.ChildrenCanceled != 1 {
		t.Errorf("children_canceled = %d, want 1", cr.ChildrenCanceled)
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("the background sub-agent was not cancelled by the parent's stop")
	}

	// Once the job leaves the registry, both flags go back to false.
	be.hub.UnregisterExternalTask(child.ID())
	workerReq(t, "GET", srv.URL+"/api/sessions", "", &list)
	for _, s := range list {
		if s.SessionID == pid && (s.Active || s.ChildrenActive) {
			t.Errorf("after the job settled: active=%v children_active=%v; want false/false",
				s.Active, s.ChildrenActive)
		}
	}
}

// TestComposerIgnoresBackgroundSubagents pins the front-end half of the same
// rule: the send button and the input lock follow state.running (the session's
// own task) only. state.childrenActive may light the stop button, it must never
// take the composer away.
func TestComposerIgnoresBackgroundSubagents(t *testing.T) {
	js := jsModulesForTest(t)

	paint := jsFunc(t, js, "paintRunButtons")
	if !strings.Contains(paint, `$("#btn-send").style.display = state.running ?`) {
		t.Errorf("send must hide on state.running alone (not on sub-agent state):\n%s", paint)
	}
	if !strings.Contains(paint, "state.childrenActive") {
		t.Errorf("the stop button must also show while background sub-agents run:\n%s", paint)
	}
	if strings.Contains(paint, `btn-send").style.display = (state.running || state.childrenActive`) {
		t.Error("send must not be hidden by childrenActive")
	}

	sync := jsFunc(t, js, "syncRunning")
	if !strings.Contains(sync, "setRunning(!!s.active)") {
		t.Errorf("syncRunning must calibrate running from the session's own active flag:\n%s", sync)
	}
	if !strings.Contains(sync, "setChildrenActive(!!s.children_active)") {
		t.Errorf("syncRunning must feed the sub-agent flag through setChildrenActive:\n%s", sync)
	}
	if strings.Contains(sync, "setRunning(!!s.children_active") {
		t.Error("syncRunning must not lock the composer on children_active")
	}

	send := jsFunc(t, js, "send")
	if !strings.Contains(send, "|| state.running) return") {
		t.Errorf("send() must still refuse while the session's own task runs:\n%s", send)
	}
	if strings.Contains(send, "state.childrenActive") {
		t.Error("send() must not gate on background sub-agents")
	}
}
