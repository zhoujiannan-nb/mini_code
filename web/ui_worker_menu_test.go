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
