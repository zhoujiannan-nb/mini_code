package web

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

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

func jsModulesForTest(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range embeddedUIAssets(t, ".js") {
		b.Write(uiAssetBody(t, p))
		b.WriteString("\n")
	}
	return b.String()
}

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

func uiAssetBody(t *testing.T, p string) []byte {
	t.Helper()
	body, err := uiFS.ReadFile(p)
	if err != nil {
		t.Fatalf("read embedded %s: %v", p, err)
	}
	return body
}
