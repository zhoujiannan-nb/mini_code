package web

import (
	"strings"
	"testing"
)

// TestResyncOnSseReconnect pins the reconnect reconciliation (the "durable log
// is the truth, the live stream only accelerates" invariant, borrowed from
// deepseek-harness). One-shot events like tool_end are lost forever when the
// SSE stream drops mid-tool: the「正在执行」card kept ticking above every later
// message while the DB already held the tool result — the view diverged from
// the truth with no path back. The fix: every time the event stream (re)opens,
// if the current session is marked running, re-fetch the persisted messages
// and fill any soft step whose result did land; freeze the rest only when the
// task itself is over.
func TestResyncOnSseReconnect(t *testing.T) {
	js := jsModulesForTest(t)

	fn := jsFunc(t, js, "resyncRunning")
	// It must only run for a session the UI believes is running...
	if !strings.Contains(fn, "state.running") || !strings.Contains(fn, "state.current") {
		t.Errorf("resyncRunning must no-op unless the current session is running:\n%s", fn)
	}
	// ...against the persisted truth (the same endpoint selectSession uses)...
	if !strings.Contains(fn, `"/api/sessions/"`) {
		t.Errorf("resyncRunning must re-fetch the session's persisted messages:\n%s", fn)
	}
	// ...and it must repair steps from tool results matched by tool name,
	// consuming each result once so parallel same-name calls do not share one.
	if !strings.Contains(fn, `m.role !== "tool"`) || !strings.Contains(fn, "st.dataset.name") {
		t.Errorf("resyncRunning must match soft steps to persisted tool results by name:\n%s", fn)
	}
	if !strings.Contains(fn, "used[i]") || !strings.Contains(fn, "stepsFill(") {
		t.Errorf("resyncRunning must consume each result once and fill the step:\n%s", fn)
	}
	// Steps with no persisted result stay untouched (they may truly run);
	// only a finished task (active=false) freezes whatever is left over.
	if !strings.Contains(fn, "stepsRunFreeze()") || !strings.Contains(fn, "active") {
		t.Errorf("resyncRunning must freeze leftover steps only when the task is over:\n%s", fn)
	}

	// The hook: every (re)connect of the event stream reconciles. EventSource
	// fires open on the first connect and on every automatic reconnect, and the
	// watchdog's connectEvents() path lands here too.
	ce := jsFunc(t, js, "connectEvents")
	if !strings.Contains(ce, "es.onopen") || !strings.Contains(ce, "resyncRunning()") {
		t.Errorf("connectEvents must resync whenever the event stream (re)opens:\n%s", ce)
	}
}
