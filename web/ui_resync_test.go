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

// TestLateToolEndFindsItsStep pins the refresh path of the same bug class:
// refreshing mid-tool re-renders the running call as a soft「待返回」step
// inside a closed strip, so the live pools (LIVE/STEPS refs are dropped by
// stepsClose) can no longer see it — the arriving tool_end was dropped on the
// floor and the step stayed「待返回」forever. stepsSoft must fall back to a
// name-matched whole-document search, and when the task is over, stepsRunFreeze
// must relabel whatever is still soft as「已执行·未收到回执」: a dispatched
// tool call has run, and the UI must say so instead of waiting forever.
func TestLateToolEndFindsItsStep(t *testing.T) {
	js := jsModulesForTest(t)

	soft := jsFunc(t, js, "stepsSoft")
	if !strings.Contains(soft, "messagesEl()") || !strings.Contains(soft, `data-soft="1"`) {
		t.Errorf("stepsSoft must fall back to a whole-document search for the named soft step:\n%s", soft)
	}

	freeze := jsFunc(t, js, "stepsRunFreeze")
	if !strings.Contains(freeze, "已执行·未收到回执") {
		t.Errorf("stepsRunFreeze must relabel leftover soft steps as executed-without-result:\n%s", freeze)
	}
}
