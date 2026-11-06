package web

import (
	"strings"
	"testing"
)

// jsFunc returns the body of a top-level function in the concatenated UI
// modules (from its "function name(" up to the first line that closes it).
func jsFunc(t *testing.T, js, name string) string {
	t.Helper()
	start := strings.Index(js, "function "+name+"(")
	if start < 0 {
		t.Fatalf("the UI modules lost function %s()", name)
	}
	body := js[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	return body
}

// TestSessionOpenFollowsItsWorker pins the sidebar follow: opening a session
// switches the worker switcher to the worker that session belongs to. The
// session-jump chips (mc-session://) go straight to selectSession, and without
// this the chat pane showed a session from another worker while the switcher
// and the list still showed the old one — it read as a broken jump, and the
// jumped-to session was not even in the list.
func TestSessionOpenFollowsItsWorker(t *testing.T) {
	js := jsModulesForTest(t)

	follow := jsFunc(t, js, "followSessionWorker")
	// It must not pick "the first session of the new bucket" the way
	// switchWorker does: the session being opened is the one to show, and the
	// caller (selectSession) is already showing it.
	if strings.Contains(follow, "selectSession") {
		t.Errorf("followSessionWorker must not select a session (that is switchWorker's job):\n%s", follow)
	}
	if !strings.Contains(follow, "loadWorkers()") || !strings.Contains(follow, "loadSessions(") {
		t.Errorf("followSessionWorker must refresh the worker bar and the session list:\n%s", follow)
	}

	sel := jsFunc(t, js, "selectSession")
	if !strings.Contains(sel, "followSessionWorker(s.worker_id") {
		t.Errorf("selectSession must follow the opened session's worker:\n%s", sel)
	}
}

// TestWorkerToolEndResyncsSidebar covers the other way a session changes
// worker: the agent calls the workers tool itself (join/create/leave) from
// inside a chat. Nothing pushes that to the browser, so the tool_end branch has
// to re-read the session and follow it — otherwise a new session the agent
// just joined stays under 未分配 and the switcher looks stuck.
func TestWorkerToolEndResyncsSidebar(t *testing.T) {
	js := jsModulesForTest(t)

	at := strings.Index(js, `case "tool_end":`)
	if at < 0 {
		t.Fatal(`events.js lost the "tool_end" branch`)
	}
	branch := js[at:]
	if end := strings.Index(branch, "break;"); end > 0 {
		branch = branch[:end]
	}
	switch {
	case !strings.Contains(branch, `m.tool_name === "workers"`):
		t.Errorf(`the tool_end branch must react to the workers tool:\n%s`, branch)
	case !strings.Contains(branch, "resyncSessionWorker(state.current)"):
		t.Errorf(`the tool_end branch must resync the sidebar for the open session:\n%s`, branch)
	}

	// The resync re-reads the session and only then follows it — and it must
	// drop a stale answer (the user may have switched in the meantime).
	resync := jsFunc(t, js, "resyncSessionWorker")
	if !strings.Contains(resync, `"/api/sessions/" + sid`) || !strings.Contains(resync, "state.current !== sid") {
		t.Errorf("resyncSessionWorker must re-read the session and ignore stale responses:\n%s", resync)
	}
}
