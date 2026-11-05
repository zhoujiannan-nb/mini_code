package web

import (
	"strings"
	"testing"
)

// TestWorkerMenuIsBodyLevel pins the one markup fact that keeps the worker
// dropdown readable. The dropdown is position:fixed, but <aside> carries a
// backdrop-filter — and that makes the aside the containing block for fixed
// descendants. With the menu node inside the sidebar it was positioned against
// the aside and cut off at its right edge, so every long worker name lost its
// tail (no ellipsis, just a hard cut). It has to be a body-level sibling.
func TestWorkerMenuIsBodyLevel(t *testing.T) {
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	closeAside := strings.Index(page, "</aside>")
	menu := strings.Index(page, `id="worker-menu"`)
	switch {
	case closeAside < 0:
		t.Fatal(`index.html has no "</aside>"`)
	case menu < 0:
		t.Fatal(`index.html lost the #worker-menu node`)
	case menu < closeAside:
		t.Errorf("#worker-menu must sit outside <aside> (aside's backdrop-filter makes it the containing block for a fixed dropdown): menu at %d, </aside> at %d", menu, closeAside)
	}

	// Placement is viewport-aware rather than "just below the anchor": a narrow
	// window must not push the dropdown off-screen.
	js := jsModulesForTest(t)
	if !strings.Contains(js, "placeWorkerMenu") || !strings.Contains(js, "innerWidth - w") {
		t.Error(`workers.js no longer clamps the worker dropdown into the viewport (placeWorkerMenu)`)
	}
}

// TestUnassignedBucketSortsLast pins the order of the worker switcher: the real
// lines of work come first and 未分配 goes last. It is the fallback bucket, so
// having it on the first row put the row you almost never want at the top.
func TestUnassignedBucketSortsLast(t *testing.T) {
	js := jsModulesForTest(t)
	start := strings.Index(js, "function workerBuckets()")
	if start < 0 {
		t.Fatal("workers.js lost workerBuckets()")
	}
	body := js[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	workers := strings.Index(body, "state.workers || []")
	unassigned := strings.Index(body, `name: "未分配"`)
	switch {
	case workers < 0 || unassigned < 0:
		t.Fatalf("workerBuckets changed shape:\n%s", body)
	case unassigned < workers:
		t.Errorf("未分配 must be appended after the real workers:\n%s", body)
	}
}

// TestWorkerSwitchFollowsAndShowsBusy pins two switcher behaviours:
//
//	(a) switching a worker also switches what the chat pane shows — the new
//	    bucket's first row, or a fresh draft when that bucket is empty;
//	(b) every menu row carries a status dot in the session-list colours
//	    (amber = something still runs in that worker, green = nothing does), and
//	    the slot is rendered even when there is nothing to say, or the count
//	    column would drift from row to row (same treatment as the ✓ column).
func TestWorkerSwitchFollowsAndShowsBusy(t *testing.T) {
	js := jsModulesForTest(t)

	sw := jsBlockForTest(t, js, "async function switchWorker(id)")
	for _, want := range []string{
		"const first = (state.sessions || [])[0]",
		"selectSession(first.session_id)",
		"resetToNew()",
	} {
		if !strings.Contains(sw, want) {
			t.Errorf("switchWorker no longer follows the switch (%s missing):\n%s", want, sw)
		}
	}

	if !strings.Contains(js, "state.unassignedActive") {
		t.Error(`workers.js dropped state.unassignedActive (the 未分配 bucket's busy count)`)
	}
	menu := jsBlockForTest(t, js, "function openWorkerMenu(anchor)")
	// NB: match the class attribute in full — "wk-menu-n" is a prefix of
	// "wk-menu-name", so bare prefixes would both land on the same index.
	name, dot, count := strings.Index(menu, `class="wk-menu-name"`), strings.Index(menu, "wkDot(w)"), strings.Index(menu, `class="wk-menu-n"`)
	if !(name >= 0 && dot > name && count > dot) {
		t.Errorf("the status dot must be rendered per row, between the name and the count: name=%d dot=%d count=%d\n%s",
			name, dot, count, menu)
	}

	// Colours come from the same theme variables the session rows use, so the
	// two dots can never drift apart.
	css := uiSource(t)
	for _, rule := range []struct{ sel, color string }{
		{".wk-menu-dot.running", "var(--amber)"},
		{".wk-menu-dot.done", "var(--ok-dot)"},
	} {
		body := cssRuleForTest(t, css, rule.sel)
		if !strings.Contains(body, rule.color) {
			t.Errorf("%s should use %s (the session dot colour):\n%s", rule.sel, rule.color, body)
		}
	}
	if slot := cssRuleForTest(t, css, ".wk-menu-dot"); !strings.Contains(slot, "flex: 0 0 7px") {
		t.Errorf("the dot slot must be fixed-width so the count column does not drift:\n%s", slot)
	}
}

// jsBlockForTest returns the source of a top-level function, from its signature
// to the first line that closes it.
func jsBlockForTest(t *testing.T, js, signature string) string {
	t.Helper()
	start := strings.Index(js, signature)
	if start < 0 {
		t.Fatalf("the UI lost %s", signature)
	}
	block := js[start:]
	if end := strings.Index(block, "\n}"); end > 0 {
		block = block[:end]
	}
	return block
}

// cssRuleForTest returns one CSS rule's body (up to its closing brace).
func cssRuleForTest(t *testing.T, css, selector string) string {
	t.Helper()
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("the stylesheets lost %s", selector)
	}
	rule := css[start:]
	if end := strings.Index(rule, "}"); end > 0 {
		rule = rule[:end]
	}
	return rule
}

// TestWorkerBarNewSessionAndTypedDelete pins two things about the worker
// sub-bar (📄 公约 / ✎ 摘要 / ✎ 名称 / ＋ 会话 / 🗑):
//
//	(a) it can start a session for that worker, and a session started there is
//	    born inside the worker — the send path attaches the selected worker to the
//	    first message, so the session gets its shared dir and 公约 from the start;
//	(b) deleting a worker takes a typed phrase, not one click: deleting one takes
//	    every session in it along with it.
func TestWorkerBarNewSessionAndTypedDelete(t *testing.T) {
	js := jsModulesForTest(t)

	bar := jsBlockForTest(t, js, "function paintWorkerBar()")
	newBtn, delBtn := strings.Index(bar, `id="wk-new-sess-btn"`), strings.Index(bar, `id="wk-del-btn"`)
	switch {
	case newBtn < 0:
		t.Error(`the worker bar lost ＋ 会话 (#wk-new-sess-btn)`)
	case delBtn >= 0 && delBtn < newBtn:
		t.Errorf("＋ 会话 must be rendered before 🗑 (right after 名称): new=%d del=%d", newBtn, delBtn)
	}
	if !strings.Contains(bar, `bind("#wk-new-sess-btn", newSessionInWorker)`) {
		t.Error(`#wk-new-sess-btn is not wired to newSessionInWorker`)
	}

	draft := jsBlockForTest(t, js, "function newSessionInWorker()")
	for _, want := range []string{"resetToNew()", `$("#input")`} {
		if !strings.Contains(draft, want) {
			t.Errorf("newSessionInWorker should carry %s:\n%s", want, draft)
		}
	}
	// The other half of "created here, lives here" is the send path: a session
	// created from the draft carries the selected worker.
	if !strings.Contains(js, "if (state.worker) body.worker_id = state.worker;") {
		t.Error("a new session no longer carries the selected worker (send.js)")
	}

	del := jsBlockForTest(t, js, "async function deleteWorker(workerID)")
	if !strings.Contains(js, `const WK_DELETE_CONFIRM = "我确认删除工作区"`) {
		t.Error(`the typed delete phrase ("我确认删除工作区") is gone`)
	}
	if !strings.Contains(del, "prompt(") || !strings.Contains(del, "typed !== WK_DELETE_CONFIRM") {
		t.Errorf("deleteWorker must ask for the phrase and match it exactly:\n%s", del)
	}
	if strings.Contains(del, "if (!confirm(") {
		t.Errorf("deleteWorker is back to the one-click confirm() — a worker takes all its sessions with it:\n%s", del)
	}
	// Deleting also took the shown session with it → the pane must not keep
	// rendering a conversation that no longer exists.
	if !strings.Contains(del, "shown.worker_id === workerID") || !strings.Contains(del, "resetToNew()") {
		t.Errorf("deleteWorker should fall back to a draft when the shown session was in that worker:\n%s", del)
	}
}
