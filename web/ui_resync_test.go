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
// if the current session is marked running, re-read the session event ledger
// (GET /api/sessions/:id/events?tail_run=1 — exactly the current run's facts,
// the window the gap could have hidden), fill any soft step whose tool/result
// did land, and freeze the rest only when the task itself is over.
func TestResyncOnSseReconnect(t *testing.T) {
	js := jsModulesForTest(t)

	fn := jsFunc(t, js, "resyncRunning")
	// It must only run for a session the UI believes is running...
	if !strings.Contains(fn, "state.running") || !strings.Contains(fn, "state.current") {
		t.Errorf("resyncRunning must no-op unless the current session is running:\n%s", fn)
	}
	// ...against the event ledger, scoped to the current run (tail_run)...
	if !strings.Contains(fn, `"/api/sessions/"`) || !strings.Contains(fn, "/events?tail_run=1") {
		t.Errorf("resyncRunning must re-read the current run's event ledger:\n%s", fn)
	}
	// ...and it must repair steps from tool/result events matched by tool name,
	// consuming each result once so parallel same-name calls do not share one.
	if !strings.Contains(fn, `ev.type === "tool/result"`) || !strings.Contains(fn, "st.dataset.name") {
		t.Errorf("resyncRunning must match soft steps to tool/result events by name:\n%s", fn)
	}
	if !strings.Contains(fn, "used[i]") || !strings.Contains(fn, "stepsFill(") {
		t.Errorf("resyncRunning must consume each result once and fill the step:\n%s", fn)
	}
	// Pruned results: a later tool/result row with the same call_id supersedes
	// the full-text row — resync must fold by call_id and count each call once.
	if !strings.Contains(fn, "lastByCall") {
		t.Errorf("resyncRunning must fold tool/result replacements by call_id:\n%s", fn)
	}
	// Steps with no recorded result stay untouched (they may truly run);
	// only a finished task (active=false) freezes whatever is left over.
	if !strings.Contains(fn, "stepsRunFreeze()") || !strings.Contains(fn, "data.active") {
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
	// A prep step whose args never finished generating never dispatched the
	// tool at all — it must be marked 未执行, not "executed without receipt".
	if !strings.Contains(freeze, `"1"`) || !strings.Contains(freeze, "未执行") {
		t.Errorf("stepsRunFreeze must mark never-dispatched prep steps 未执行:\n%s", freeze)
	}
}

// TestUserMessageClosesStrip pins the refresh layout bug: replaying history
// into a running session merged every step — before AND after the user's
// 「继续」— into one 执行过程 strip, so the user's own card landed BELOW the
// whole strip (错位). A user message is a task boundary: appendHistoryMsg
// must close the current strip in place before adding the user card, and the
// next task's steps start a fresh strip below it.
func TestUserMessageClosesStrip(t *testing.T) {
	js := jsModulesForTest(t)

	fn := jsFunc(t, js, "appendHistoryMsg")
	closeAt := strings.Index(fn, "stepsClose()")
	userAt := strings.Index(fn, "addUser(")
	if closeAt < 0 || userAt < 0 || closeAt > userAt {
		t.Errorf("appendHistoryMsg must close the strip before adding a user card:\n%s", fn)
	}
}

// TestResyncRestoresRunClock pins the "刷新后工具执行 UI 消失" fix: after a
// refresh the still-running call was a static「待返回」with no clock at all.
// The ledger knows when the call started (tool/call.created_at), so the
// resync restores the step as「执行中」with its real elapsed clock running —
// a call that has been going 260s must not restart at 0s just because the
// page was reloaded. Without a ledger event there is no honest start time,
// so the fallback stays a spinner with no invented number.
func TestResyncRestoresRunClock(t *testing.T) {
	js := jsModulesForTest(t)

	fn := jsFunc(t, js, "resyncRunning")
	// Unanswered = the last tool/call of that name sits after the last
	// tool/result of that name; its created_at becomes the clock start.
	for _, want := range []string{"lastCall", "lastRes", "parseEvTime", "st.dataset.t0", `st.dataset.live = "1"`, "stepsRunStart()", `"run", "执行中"`} {
		if !strings.Contains(fn, want) {
			t.Errorf("resyncRunning must restore the running clock from the ledger (missing %q):\n%s", want, fn)
		}
	}
	// No event, no honest start: spinner only, no invented timer.
	if !strings.Contains(fn, `stepsTag(st, "soft", "待返回")`) {
		t.Errorf("resyncRunning must fall back to the plain spinner without a clock:\n%s", fn)
	}

	// RFC3339 with nanoseconds: Date.parse chokes on more than 3 fractional
	// digits, so parseEvTime truncates to milliseconds first.
	pe := jsFunc(t, js, "parseEvTime")
	if !strings.Contains(pe, `(\.\d{3})\d+`) || !strings.Contains(pe, "Date.parse") {
		t.Errorf("parseEvTime must truncate nanoseconds before parsing:\n%s", pe)
	}
}

// TestRefreshKeepsRunningStepOutsideStrip pins the "有时候这样有时候不是"
// report: the streaming path keeps the running tool step OUTSIDE the
// collapsible (its own「正在执行」card, folded away only once the receipt
// lands), but a refreshed/reopened running session renders history with
// histRendering, which drops the unanswered call INTO the strip — and the
// ledger resync then re-tagged it「执行中」with a running clock while leaving
// it in there. Same task, two different views: whoever reloaded lost the
// spinner and the clock behind a collapsed bar. resync must now hand the step
// to stepsUnfoldStep, which moves it back into the live card in exactly the
// shape the streaming path builds (open, args preview in the expanded body,
// full args carried in st._args for the trip back).
func TestRefreshKeepsRunningStepOutsideStrip(t *testing.T) {
	js := jsModulesForTest(t)

	unfold := jsFunc(t, js, "stepsUnfoldStep")
	for _, want := range []string{"stepsLiveStrip()", "ms-live-step", "st.open = true", "stepsLivePreview("} {
		if !strings.Contains(unfold, want) {
			t.Errorf("stepsUnfoldStep must rebuild the live-step shape (missing %q):\n%s", want, unfold)
		}
	}
	// The live card's expanded body holds only output/preview: the args block
	// goes with it as st._args so stepsFoldStep can put it back once folded.
	if !strings.Contains(unfold, "st._args") || !strings.Contains(unfold, "argsEl.remove()") {
		t.Errorf("stepsUnfoldStep must carry the args out of the expanded body:\n%s", unfold)
	}
	// Pulling the only step out must not leave a「执行过程 0 步」bar behind.
	if !strings.Contains(unfold, "children.length === 0") || !strings.Contains(unfold, "stepsStripReset()") {
		t.Errorf("stepsUnfoldStep must drop an emptied strip:\n%s", unfold)
	}
	// Reclaiming the strip (renderHistory's stepsClose dropped the refs) must
	// never adopt a strip merged into an answer card as the standalone shell:
	// stepsMergeInto would then remove() the answer card itself.
	rec := jsFunc(t, js, "stepsReclaimStrip")
	if !strings.Contains(rec, "ms-inline") || !strings.Contains(rec, "STEPS.card = ") {
		t.Errorf("stepsReclaimStrip must refuse an inline strip as STEPS.card:\n%s", rec)
	}

	// The resync only resurrects a step the ledger actually says is
	// unanswered, and only among the steps that can still be waiting.
	fn := jsFunc(t, js, "resyncRunning")
	if !strings.Contains(fn, "stepsUnfoldStep(") {
		t.Errorf("resyncRunning must move the recovered step out of the collapsible:\n%s", fn)
	}
	if !strings.Contains(fn, "details.m-steps") || !strings.Contains(fn, "st.dataset.frozen") {
		t.Errorf("resyncRunning must scope candidates to the live pool and the newest strip:\n%s", fn)
	}
	if !strings.Contains(fn, "const pending = ") {
		t.Errorf("resyncRunning must recover only calls the ledger says are unanswered:\n%s", fn)
	}

	// A frozen leftover is terminal: freezing stamps a marker so a later run
	// cannot re-spin a years-old row inside the live card.
	freeze := jsFunc(t, js, "stepsRunFreeze")
	if !strings.Contains(freeze, `st.dataset.frozen = "1"`) {
		t.Errorf("stepsRunFreeze must mark leftover steps as terminal:\n%s", freeze)
	}
}

// TestBootRestoresCurrentSession pins the refresh restore: the worker bucket
// must be restored BEFORE the session list is fetched (otherwise boot always
// lists the unassigned bucket while the bar shows the last worker), and the
// saved session opens by id — the old check demanded it sit in the current
// bucket's list and threw the record away when the session belonged to
// another worker ("刷新后当前会话没有了"). Opening a running session must also
// fire the ledger resync: es.onopen's resync runs before state.running is
// set, so without this hook the refreshed view never gets the spinner back
// or the clock restored.
func TestBootRestoresCurrentSession(t *testing.T) {
	js := jsModulesForTest(t)

	barAt := strings.Index(js, "initWorkerBar();")
	listAt := strings.Index(js, "await loadSessions();")
	if barAt < 0 || listAt < 0 || barAt > listAt {
		t.Error("boot must restore the worker bucket before fetching the session list")
	}
	if !strings.Contains(js, `api("/api/sessions/" + saved)`) || !strings.Contains(js, "selectSession(saved)") {
		t.Error("boot must open the saved session by id")
	}
	if strings.Contains(js, "state.sessions.some(s => s.session_id === saved)") {
		t.Error("restore must not gate on the current bucket's list")
	}

	sel := jsFunc(t, js, "selectSession")
	if !strings.Contains(sel, "resyncRunning()") || !strings.Contains(sel, "s.active") {
		t.Errorf("opening a running session must fire the ledger resync:\n%s", sel)
	}
}
