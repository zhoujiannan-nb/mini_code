package web

import (
	"strings"
	"testing"
)

// TestUILinksChatPathsToTheOpener verifies the UI ships the "click a path in
// the chat to open it" wiring: the renderer recognises paths, links them to
// /api/fs/open, and the click/keyboard handlers post the path with the
// session's work dir as the base for relative ones.
func TestUILinksChatPathsToTheOpener(t *testing.T) {
	src := uiSource(t)
	for _, want := range []string{
		`function mdIsFilePath`,
		`function fileOpenLinkHTML`,
		`function mdLinkifyPaths`,
		`class="md-file"`,
		`data-file="`,
		`/api/fs/open`,
		`base: state.workDir || ""`,
		`openFilePath(`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the UI is missing %q", want)
		}
	}
	// The paths must be linked from inside inline markdown — a whole-message
	// rewrite would also mangle code blocks, where a path is just code.
	if !strings.Contains(src, `s = mdLinkifyPaths(s, keep);`) {
		t.Error("inlineMd does not call mdLinkifyPaths")
	}
	// A previous build rendered only the last HIST_PAGE messages; 100 is the
	// current window, so a long tool-heavy conversation opens fully.
	if !strings.Contains(src, "const HIST_PAGE = 100;") {
		t.Error("HIST_PAGE is not 100")
	}
}

// TestResolveOpenPathAbsoluteIsUnchanged is a belt-and-braces check that the
// resolver keeps the user's own path: it must not rewrite D:\x\y into anything
// else (a resolved path is what the opener gets, and what the toast shows).
func TestResolveOpenPathAbsoluteIsUnchanged(t *testing.T) {
	for _, in := range []string{`D:\a\b\c.html`, `\\srv\share\x.txt`} {
		got, msg := resolveOpenPath(in, "")
		if msg != "" {
			t.Fatalf("resolveOpenPath(%q) failed: %s", in, msg)
		}
		if !strings.HasSuffix(strings.ReplaceAll(got, "/", `\`), strings.ReplaceAll(in, "/", `\`)) {
			t.Errorf("resolveOpenPath(%q) = %q, want the same path", in, got)
		}
	}
}
