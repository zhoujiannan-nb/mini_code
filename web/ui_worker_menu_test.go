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

// TestWorkerBucketsHaveNoUnassignedRow pins the retirement of 未分配: every
// session is born under a worker (the fixed 其他 is the fallback), so the
// switcher lists exactly the non-builtin workers the server reports — no fake
// "{ worker_id: \"\" }" row pushed on the client.
func TestWorkerBucketsHaveNoUnassignedRow(t *testing.T) {
	js := jsModulesForTest(t)
	start := strings.Index(js, "function workerBuckets()")
	if start < 0 {
		t.Fatal("workers.js lost workerBuckets()")
	}
	body := js[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "state.workers || []") || !strings.Contains(body, "!x.builtin") {
		t.Fatalf("workerBuckets changed shape:\n%s", body)
	}
	if strings.Contains(body, `name: "未分配"`) || strings.Contains(body, "out.push") {
		t.Errorf("the fake 未分配 row must be gone — 其他 is a real server-side bucket:\n%s", body)
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

// TestWorkerBarTypedDeleteAndNoNewSessionButton pins two things about the
// worker sub-bar (📄 公约 / ✎ 摘要 / ✎ 名称 / 🗑):
//
//	(a) it no longer carries a "＋ 会话" button: the header's ＋ 新建会话 already
//	    lands a draft in the currently selected bucket, the small button was a
//	    duplicate of that one semantics;
//	(b) deleting a worker takes a typed phrase, not one click: deleting one takes
//	    every session in it along with it.
func TestWorkerBarTypedDeleteAndNoNewSessionButton(t *testing.T) {
	js := jsModulesForTest(t)

	bar := jsBlockForTest(t, js, "function paintWorkerBar()")
	if strings.Contains(bar, `id="wk-new-sess-btn"`) {
		t.Error(`the worker bar must not resurrect ＋ 会话 (#wk-new-sess-btn) — the header button already lands drafts in the current bucket`)
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

// TestSessionListRefreshPlaceholder pins the refresh UX: a worker switch or a
// worker delete must clear the list and show a skeleton instead of leaving the
// previous worker's rows on screen (the delete waits on the server deleting every
// session in it, which is exactly the moment stale rows read as "nothing happened").
func TestSessionListRefreshPlaceholder(t *testing.T) {
	js := jsModulesForTest(t)

	begin := jsBlockForTest(t, js, "function beginSessionsRefresh()")
	for _, want := range []string{"state.sessions = []", "sessionsLoading = true", "renderSessions()"} {
		if !strings.Contains(begin, want) {
			t.Errorf("beginSessionsRefresh should %s:\n%s", want, begin)
		}
	}
	load := jsBlockForTest(t, js, "async function loadSessions(select, fresh)")
	if !strings.Contains(load, "if (fresh) beginSessionsRefresh();") {
		t.Errorf("loadSessions must clear-then-fetch when asked:\n%s", load)
	}
	if !strings.Contains(load, "sessionsError = (e && e.message) || String(e)") {
		t.Errorf("a failed refresh must be reported (silently returning kept the old rows on screen):\n%s", load)
	}
	paint := jsBlockForTest(t, js, "function renderSessions()")
	if !strings.Contains(paint, "sessSkeleton()") || !strings.Contains(paint, "sessionsError") {
		t.Errorf("renderSessions should paint the skeleton and the failure hint:\n%s", paint)
	}
	sk := jsBlockForTest(t, js, "function sessSkeleton()")
	if !strings.Contains(sk, "for (let i = 0; i < 3; i++)") || !strings.Contains(sk, "sess-skeleton") {
		t.Errorf("the placeholder should be a few ghost rows:\n%s", sk)
	}
	sw := jsBlockForTest(t, js, "async function switchWorker(id)")
	if !strings.Contains(sw, "loadSessions(null, true)") {
		t.Errorf("switchWorker must refresh with the placeholder:\n%s", sw)
	}
	del := jsBlockForTest(t, js, "async function deleteWorker(workerID)")
	if !strings.Contains(del, "beginSessionsRefresh()") || !strings.Contains(del, "delBtn.disabled = true") {
		t.Errorf("deleteWorker must clear the list up front and grey out 🗑 while it runs:\n%s", del)
	}
}

// TestWorkerControlsHoverGreen pins the hover colours of the worker controls to
// the theme's own light green: they used to hover to a neutral grey, which looked
// like an unstyled web control next to everything else, and the current menu row
// hovered to its own selected colour, so hovering did nothing.
func TestWorkerControlsHoverGreen(t *testing.T) {
	css := uiSource(t)
	for _, sel := range []string{".wk-sub-btn:hover", ".wk-menu-item:hover", ".wk-menu-item.cur:hover"} {
		rule := cssRuleForTest(t, css, sel)
		if !strings.Contains(rule, "var(--accent-h)") {
			t.Errorf("%s should hover to the light-green --accent-h:\n%s", sel, rule)
		}
	}
	if btn := cssRuleForTest(t, css, ".wk-sub-btn:hover"); !strings.Contains(btn, "var(--accent-t)") {
		t.Errorf("the hover label should use the primary text colour:\n%s", btn)
	}
	// Both themes need the variable, or dark mode hovers to nothing.
	if n := strings.Count(css, "--accent-h:"); n < 2 {
		t.Errorf("--accent-h must be defined in the light and the dark palette, found %d", n)
	}
	// The delete button keeps its destructive red.
	if d := cssRuleForTest(t, css, ".wk-sub-danger:hover"); strings.Contains(d, "--accent-h") {
		t.Errorf("🗑 should stay red on hover:\n%s", d)
	}
	if sk := cssRuleForTest(t, css, ".sess-skeleton .sk"); !strings.Contains(sk, "animation:") {
		t.Errorf("the skeleton rows should breathe:\n%s", sk)
	}
}

// TestNewSessionButtonsShareOwnership pins the two "new session" entry points to
// one semantics: a draft is always owned by the bucket it is created in. The big
// header button used to clear state.worker (an ownerless session then got a join
// reminder and burned turns picking a worker); with ownership fixed at creation
// — the server files every session under the selected bucket, 其他 by default —
// both buttons merely pin state.worker, which is what send.js sends as
// body.worker_id.
func TestNewSessionButtonsShareOwnership(t *testing.T) {
	app := string(uiAssetBody(t, "ui/js/app.js"))
	wk := string(uiAssetBody(t, "ui/js/workers.js"))
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}

	i := strings.Index(app, `$("#btn-new").addEventListener`)
	if i < 0 {
		t.Fatal(`app.js lost the #btn-new click handler`)
	}
	body := app[i:]
	if j := strings.Index(body, "\n  });"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "detachWorkerForNewSession()") {
		t.Error(`#btn-new no longer detaches the draft from the worker — that makes it a duplicate of the worker-scoped ＋会话 button`)
	}
	if !strings.Contains(body, "resetToNew()") {
		t.Error(`#btn-new no longer resets to a fresh draft (resetToNew)`)
	}

	detach := strings.Index(wk, "function detachWorkerForNewSession()")
	if detach < 0 {
		t.Fatal("workers.js lost detachWorkerForNewSession")
	}
	if strings.Contains(wk, "function newSessionInWorker()") {
		t.Error(`the worker-scoped ＋会话 entry point must stay deleted — one new-session semantics (the header button)`)
	}
	detachBody := wk[detach:]
	if strings.Contains(detachBody, `state.worker = ""`) || strings.Contains(detachBody, `rememberWorker("")`) {
		t.Error(`detachWorkerForNewSession must not clear ownership — there is no unassigned state anymore`)
	}
	if !strings.Contains(detachBody, "workerBucketId()") || !strings.Contains(detachBody, "state.worker = id") {
		t.Error(`detachWorkerForNewSession must pin the draft to the current bucket (state.worker = id)`)
	}

	// The semantics have to be readable from the UI, not only from the code.
	if !strings.Contains(page, `id="btn-new" title="新建会话（落在当前选中的工作区`) {
		t.Error(`#btn-new lost the tooltip telling the user where the new session lands`)
	}
}
