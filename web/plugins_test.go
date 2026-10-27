package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginHome points the process at a throwaway home with a plugins directory.
func pluginHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".mini_code", "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// installPlugin writes ~/.mini_code/plugins/<id>/ with a manifest and files.
func installPlugin(t *testing.T, home, id, manifest string, files map[string]string) {
	t.Helper()
	base := filepath.Join(home, ".mini_code", "plugins", id)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for name, body := range files {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestPluginAssetsNotInjectedWithoutMarkerOrPlugins: the page is unchanged when
// no plugin is installed (byte-for-byte), which is what keeps the shipped UI
// identical for everyone who never installs one.
func TestPluginAssetsNotInjectedWithoutPlugins(t *testing.T) {
	pluginHome(t)
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	if !strings.Contains(page, pluginAssetsMarker) {
		t.Fatal("index.html lost the plugin asset marker")
	}
	got := injectPluginAssets(page)
	want := strings.Replace(page, pluginAssetsMarker, "", 1)
	if got != want {
		t.Fatal("injectPluginAssets changed the page although no plugin is installed")
	}
}

// TestPluginAssetsInjected checks skins and a wallpaper land in the page.
func TestPluginAssetsInjected(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["skin.css"],"wallpaper":"bg.webp"}`,
		map[string]string{"skin.css": "body{}", "bg.webp": "fake"})

	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	got := injectPluginAssets(page)
	if !strings.Contains(got, `<link rel="stylesheet" href="/plugins/skin/skin.css">`) {
		t.Fatalf("skin stylesheet not injected:\n%s", got)
	}
	if !strings.Contains(got, `/plugins/skin/bg.webp`) {
		t.Fatal("wallpaper not injected")
	}
	// The wallpaper must be one scaled image, not a tiled pattern: the CSS
	// default for background-repeat is `repeat`, and a tiled photo is never
	// what a wallpaper means.
	if !strings.Contains(got, "background-size:cover") {
		t.Error("wallpaper is not sized with cover")
	}
	if !strings.Contains(got, "background-repeat:no-repeat") {
		t.Error("wallpaper would tile (background-repeat:no-repeat missing)")
	}
	if strings.Contains(got, pluginAssetsMarker) {
		t.Fatal("marker was not replaced")
	}
	// The injected tags must come after the built-in stylesheets so a skin can
	// override them.
	if strings.Index(got, "/css/layers.css") > strings.Index(got, "/plugins/skin/skin.css") {
		t.Fatal("plugin skin was injected before the built-in stylesheets")
	}
}

// TestPluginAssetRouteServes: 200 + content type for a whitelisted file,
// 404 for a missing one, 403 for a disallowed extension, and containment for
// a traversal attempt.
func TestPluginAssetRouteServes(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["skin.css"]}`, map[string]string{
		"skin.css":   "body{--x:1}",
		"evil.html":  "<script>alert(1)</script>",
		"notes.txt":  "plain",
		"../outside": "nope",
		"sub/in.css": "body{}",
	})
	be := newTestBackend(t)
	srv := httptest.NewServer(NewServer(be))
	defer srv.Close()

	get := func(p string) (int, string, string) {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}

	code, ct, body := get("/plugins/skin/skin.css")
	if code != 200 || !strings.Contains(ct, "text/css") || !strings.Contains(body, "--x:1") {
		t.Fatalf("css: code=%d ct=%q body=%q", code, ct, body)
	}
	code, _, _ = get("/plugins/skin/sub/in.css")
	if code != 200 {
		t.Errorf("nested css: code = %d, want 200", code)
	}
	code, _, _ = get("/plugins/skin/missing.css")
	if code != 404 {
		t.Errorf("missing file: code = %d, want 404", code)
	}
	// HTML a plugin merely happens to contain is NOT served (it would turn the
	// plugin directory into a web root on the app's origin); only a fragment the
	// manifest declares as a slot is. Plain text is never served.
	for _, p := range []string{"/plugins/skin/evil.html", "/plugins/skin/notes.txt"} {
		code, _, _ = get(p)
		if code != 403 {
			t.Errorf("%s: code = %d, want 403", p, code)
		}
	}
	// Traversal attempts never resolve. Note: net/http cleans ".." segments
	// before the handler runs, so "/plugins/skin/../skin/skin.css" is
	// rewritten into the legitimate "/plugins/skin/skin.css" — no escape ever
	// reaches the handler. The cases below are the ones that survive cleaning
	// (percent-encoded dot segments, unknown dir, wrong shape).
	for _, p := range []string{
		"/plugins/skin/%2e%2e/skin.css",
		"/plugins/skin/%2e%2e%2fskin.css",
		"/plugins/nope/skin.css",
		"/plugins/../skin.css",
		"/plugins/skin/",
		"/plugins/",
		"/plugins/skin",
	} {
		code, _, _ = get(p)
		if code == 200 {
			t.Errorf("%s must not be served", p)
		}
	}
}

// TestPluginAssetHeaders: a plugin stylesheet must not be sniffed into
// something executable and must not be cached over an edit.
func TestPluginAssetHeaders(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["skin.css"]}`, map[string]string{"skin.css": "body{}"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/plugins/skin/skin.css")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want a locked-down default-src", csp)
	}
}

// TestPluginsListAPI: GET /api/plugins lists what is installed, with kinds and
// tool names, and is stable.
func TestPluginsListAPI(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "b-skin", `{"id":"b-skin","name":"B Skin","version":"2.0.0","styles":["s.css"]}`,
		map[string]string{"s.css": "body{}"})
	installPlugin(t, home, "a-tools", `{"id":"a-tools","name":"A Tools",
		"tools":[{"name":"z_tool","command":"x"},{"name":"a_tool","command":"x"}],"memory":"m.md"}`,
		map[string]string{"m.md": "notes"})
	installPlugin(t, home, "disabled", `{"id":"disabled","enabled":false}`, nil)

	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatalf("GET /api/plugins: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, want := range []string{`"a-tools"`, `"b-skin"`, `"A Tools"`, `"2.0.0"`, `"a_tool"`, `"z_tool"`, `"skin"`, `"memory"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/api/plugins is missing %s\n%s", want, body)
		}
	}
	if strings.Contains(body, `"error"`) {
		t.Error("/api/plugins reported an error for a valid plugin")
	}
	// The centre lists disabled plugins too (that is where you turn them back
	// on), each flagged with enabled:false and a reason. The agent's own view
	// (plugin.Load) is the one that filters them out — see the plugin package
	// tests for that side.
	if !strings.Contains(body, `"id":"disabled"`) || !strings.Contains(body, `"enabled":false`) {
		t.Error("/api/plugins should list a disabled plugin with enabled:false")
	}
	// Sorted by id: a-tools before b-skin.
	if strings.Index(body, "a-tools") > strings.Index(body, "b-skin") {
		t.Error("/api/plugins is not sorted by id")
	}
	// Tool names are sorted so the payload is deterministic.
	if strings.Index(body, "a_tool") > strings.Index(body, "z_tool") {
		t.Error("tool names are not sorted")
	}
}

// TestPluginPageServesAssets: the served page actually references the plugin
// assets, end to end over HTTP.
func TestPluginPageServesAssets(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["skin.css"]}`, map[string]string{"skin.css": "body{}"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "/plugins/skin/skin.css") {
		t.Fatal("the served page does not reference the plugin stylesheet")
	}
	// And that link actually resolves.
	resp, err = http.Get(srv.URL + "/plugins/skin/skin.css")
	if err != nil {
		t.Fatalf("GET plugin css: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("plugin css status = %d, want 200", resp.StatusCode)
	}
}

// TestDeclaredSlotHTMLIsServed: the one .html a manifest declares as a slot
// fragment IS served (the page frames it), with a locked-down CSP and nosniff —
// while an undeclared .html in the same directory stays 403.
func TestDeclaredSlotHTMLIsServed(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "bar", `{"id":"bar","ui":{"slots":[{"slot":"composer-bar","html":"bar.html"}]}}`,
		map[string]string{
			"bar.html":     `<b onclick="alert(1)">hi</b>`,
			"undeclared.html": "<script>alert(1)</script>",
		})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/plugins/bar/bar.html")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("declared slot html status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff missing: %q", got)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	resp2, err := http.Get(srv.URL + "/plugins/bar/undeclared.html")
	if err != nil {
		t.Fatalf("GET undeclared: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Errorf("undeclared html status = %d, want 403", resp2.StatusCode)
	}
}

func TestValidPluginIDAndRelPath(t *testing.T) {
	for _, s := range []string{"a", "a-b", "abc123", "0x"} {
		if !validPluginID(s) {
			t.Errorf("validPluginID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "-a", "A", "a_b", "a/b", "../a", "a b", strings.Repeat("a", 65)} {
		if validPluginID(s) {
			t.Errorf("validPluginID(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"a.css", "sub/a.css"} {
		if !pluginRelPathOK(s) {
			t.Errorf("pluginRelPathOK(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "..", "../a", "a/../b", "/a.css", "a\\b.css", "a.css?x=1", "./a.css", "a//b.css"} {
		if pluginRelPathOK(s) {
			t.Errorf("pluginRelPathOK(%q) = true, want false", s)
		}
	}
}
