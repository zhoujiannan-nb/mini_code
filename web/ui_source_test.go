package web

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// uiSource returns everything the built-in UI ships to the browser: the HTML
// shell plus every embedded stylesheet and script, concatenated in a stable
// order. The UI used to be a single index.html; after the front-end was split
// into html/css/js the markup lives in the shell while most of the behaviour
// lives in the modules, so tests that assert "the UI carries X" have to look at
// all of it rather than at index.html alone.
func uiSource(t *testing.T) string {
	t.Helper()
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	var b strings.Builder
	b.WriteString(page)
	for _, p := range embeddedUIAssets(t, ".css", ".js") {
		b.WriteString("\n/* ---- ")
		b.WriteString(p)
		b.WriteString(" ---- */\n")
		b.Write(uiAssetBody(t, p))
	}
	return b.String()
}

// jsModulesForTest returns the concatenated embedded JS modules.
func jsModulesForTest(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range embeddedUIAssets(t, ".js") {
		b.Write(uiAssetBody(t, p))
		b.WriteString("\n")
	}
	return b.String()
}

// embeddedUIAssets lists embedded UI files with one of exts, sorted by path.
func embeddedUIAssets(t *testing.T, exts ...string) []string {
	t.Helper()
	want := make(map[string]bool, len(exts))
	for _, e := range exts {
		want[e] = true
	}
	var paths []string
	err := fs.WalkDir(uiFS, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if want[strings.ToLower(path.Ext(p))] {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded ui: %v", err)
	}
	sort.Strings(paths)
	return paths
}

// uiAssetBody reads one embedded UI file.
func uiAssetBody(t *testing.T, p string) []byte {
	t.Helper()
	body, err := uiFS.ReadFile(p)
	if err != nil {
		t.Fatalf("read embedded %s: %v", p, err)
	}
	return body
}
