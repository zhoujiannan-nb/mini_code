package web

import (
	"strings"
	"testing"
)

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
	if !strings.Contains(src, `s = mdLinkifyPaths(s, keep);`) {
		t.Error("inlineMd does not call mdLinkifyPaths")
	}
	if !strings.Contains(src, "const HIST_PAGE = 100;") {
		t.Error("HIST_PAGE is not 100")
	}
}

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
