package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/config"
)

// bootAnimTestServer builds a server whose boot config is fixed. The clips
// themselves come from the plugin directory (pluginHome), because a startup
// animation IS an appearance plugin now.
func bootAnimTestServer(t *testing.T, enabled bool, theme string) *httptest.Server {
	t.Helper()
	cfg := &config.AppConfig{
		BootAnim: config.BootAnimConfig{
			Enabled: enabled, Theme: theme, Mode: config.BootAnimModeSession,
			Repo: "tester/plugins",
		},
	}
	srv := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	t.Cleanup(srv.Close)
	return srv
}

// installBootPlugin writes a boot plugin directory under the test home.
func installBootPlugin(t *testing.T, home, id, manifest string, files map[string]string) {
	t.Helper()
	installPlugin(t, home, id, manifest, files)
}

const bootEntryHTML = "<!DOCTYPE html><html><body>clip</body></html>"

// TestBootClipIsAPlugin: the StartupClip of a boot plugin is its entry document,
// served from the plugin's own directory under the theme CSP.
func TestBootClipIsAPlugin(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"示例动画","version":"1.2.3","kind":"appearance",
		   "description":"单测用","boot":{"entry":"index.html","durationMs":2000,"maxMs":4000}}`,
		map[string]string{
			"index.html": bootEntryHTML,
			"style.css":  "body{color:red}",
			"clip.js":    "postMessage({mc:'boot-anim',type:'ready'},'*')",
		})
	srv := bootAnimTestServer(t, true, "")

	// --- listing ---
	resp, err := http.Get(srv.URL + "/api/bootanim")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var list struct {
		Dir       string          `json:"dir"`
		LegacyDir string          `json:"legacy_dir"`
		Themes    []BootAnimTheme `json:"themes"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list %s: %v", body, err)
	}
	if len(list.Themes) != 1 {
		t.Fatalf("want 1 clip, got %d: %s", len(list.Themes), body)
	}
	c := list.Themes[0]
	if c.ID != "boot-demo" || c.Error != "" || c.URL != "/bootanim/boot-demo/index.html" ||
		c.Version != "1.2.3" || c.DurationMs != 2000 || c.MaxMs != 4000 {
		t.Fatalf("clip not described correctly: %+v", c)
	}
	if c.Source != "plugin" || c.Plugin != "boot-demo" {
		t.Errorf("clip must say it came from a plugin: %+v", c)
	}
	// dir points at the plugin directory: that is where clips live now.
	if list.Dir == "" || !strings.Contains(list.Dir, "plugins") {
		t.Errorf("dir should be the plugin directory, got %q", list.Dir)
	}

	// --- asset serving ---
	resp, err = http.Get(srv.URL + "/bootanim/boot-demo/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "clip") {
		t.Fatalf("entry: %d %s", resp.StatusCode, body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'none'", "frame-src 'none'",
		"script-src 'self' 'unsafe-inline'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	// A clip MAY ship .js (unlike a skin): its code runs in the sandboxed frame.
	if r, err := http.Get(srv.URL + "/bootanim/boot-demo/clip.js"); err != nil || r.StatusCode != 200 {
		t.Fatalf("clip script should be served: %v %v", err, r)
	}
	// Multi-file clips work.
	if r, _ := http.Get(srv.URL + "/bootanim/boot-demo/style.css"); r.StatusCode != 200 {
		t.Errorf("sub-file: %d", r.StatusCode)
	}
	// Missing file / unknown clip / disallowed type.
	if r, _ := http.Get(srv.URL + "/bootanim/boot-demo/nope.html"); r.StatusCode != 404 {
		t.Errorf("missing file: want 404, got %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/bootanim/nope/index.html"); r.StatusCode != 404 {
		t.Errorf("unknown clip: want 404, got %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/bootanim/boot-demo/x.exe"); r.StatusCode != 403 {
		t.Errorf("exe: want 403, got %d", r.StatusCode)
	}
}

// TestBootClipCannotEscapeOrReadNeighbours: the same traversal guard the old
// theme route had, now applied to a plugin directory (which also holds skins,
// plugin.json, memory.md…).
func TestBootClipCannotEscapeOrReadNeighbours(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"demo","boot":{"entry":"index.html"}}`,
		map[string]string{"index.html": bootEntryHTML})
	// A file NEXT TO the plugin directory: the clip route must never reach it.
	secret := filepath.Join(home, ".mini_code", "plugins", "secret.txt")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := bootAnimTestServer(t, true, "")

	for _, u := range []string{
		srv.URL + "/bootanim/boot-demo/../../secret.txt",
		srv.URL + "/bootanim/boot-demo/%2e%2e%2fsecret.txt",
		srv.URL + "/bootanim/boot-demo/..%2f..%2fsecret.txt",
		srv.URL + "/bootanim/boot-demo/../../../../../../Windows/win.ini",
	} {
		r, err := http.Get(u)
		if err != nil {
			continue // a client-side refusal is fine too
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(b), "TOPSECRET") || strings.Contains(string(b), "[extensions]") {
			t.Errorf("traversal leaked %s", u)
		}
	}
	// The plugin directory itself is not listable.
	if r, _ := http.Get(srv.URL + "/bootanim/boot-demo/"); r.StatusCode == 200 {
		t.Error("clip directory should not be served")
	}
}

// TestBootClipSandboxCannotReachAPI documents the intent of the CSP: a clip
// cannot call the app's own endpoints from inside its frame.
func TestBootClipSandboxCannotReachAPI(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"demo","boot":{"entry":"index.html"}}`,
		map[string]string{"index.html": `<script>fetch("/api/config")</script>`})
	srv := bootAnimTestServer(t, true, "")

	resp, err := http.Get(srv.URL + "/bootanim/boot-demo/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// connect-src 'none' is what blocks the fetch above; the iframe additionally
	// runs with sandbox="allow-scripts" (no same-origin), asserted in the browser
	// test, so it has no access to the UI's origin at all.
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("clip must not be able to call the API: %s", csp)
	}
}

// TestBootClipDisabledPluginDisappears: the plugin toggle IS the clip toggle.
// Turning the plugin off removes it from the list and stops it playing — one
// switch, one concept (this is the whole point of the merge).
func TestBootClipDisabledPluginDisappears(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"demo","boot":{"entry":"index.html"}}`,
		map[string]string{"index.html": bootEntryHTML})
	srv := bootAnimTestServer(t, true, "")

	page, err := fetchBody(t, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, `"entry":"/bootanim/boot-demo/index.html"`) {
		t.Fatalf("the clip should be injected into the player config: %s", page)
	}

	if code, body := doJSON(t, "POST", srv.URL+"/api/plugins/boot-demo/toggle", `{"enabled":false}`); code != 200 {
		t.Fatalf("toggle: %d %s", code, body)
	}
	_, listBody := doJSON(t, "GET", srv.URL+"/api/bootanim", "")
	if strings.Contains(listBody, "boot-demo") {
		t.Errorf("a disabled boot plugin must not be listed: %s", listBody)
	}
	page, _ = fetchBody(t, srv.URL+"/")
	if !strings.Contains(page, `"mc-boot-cfg" type="application/json">null<`) {
		t.Errorf("a disabled clip must not produce a player config: %s", page)
	}
	// And its files stop being served at all.
	if r, _ := http.Get(srv.URL + "/bootanim/boot-demo/index.html"); r.StatusCode == 404 {
		// expected
	} else {
		t.Errorf("a disabled clip should not be served, got %d", r.StatusCode)
	}
}

// TestBootPluginValidationRejectsMissingEntry: a manifest that claims a clip
// whose entry file is absent fails the load, loudly, with the file named.
func TestBootPluginValidationRejectsMissingEntry(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-broken",
		`{"id":"boot-broken","name":"broken","boot":{"entry":"gone.html"}}`, nil)
	srv := bootAnimTestServer(t, true, "")

	_, body := doJSON(t, "GET", srv.URL+"/api/plugins", "")
	if !strings.Contains(body, "boot-broken") || !strings.Contains(body, "gone.html") {
		t.Fatalf("a boot plugin with a missing entry must be reported: %s", body)
	}
	// Nothing to play.
	page, _ := fetchBody(t, srv.URL+"/")
	if !strings.Contains(page, `"mc-boot-cfg" type="application/json">null<`) {
		t.Error("a broken clip must not become the active one")
	}
}

// TestBootThemeJsonIsInherited: a ported theme keeps its theme.json (the plugin
// repo's clips already have one). The numbers the boot block omits come from it,
// so nothing has to be retyped.
func TestBootThemeJsonIsInherited(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"demo","kind":"appearance","boot":{"entry":"index.html"}}`,
		map[string]string{
			"index.html": bootEntryHTML,
			"theme.json": `{"id":"boot-demo","name":"demo","version":"9.9.9","description":"来自 theme.json",
				"durationMs":3200,"maxMs":6400,"interactive":true,"minViewport":480,
				"options":{"brand":"Mini Code","speed":9,"Bad Key":"x","n":true}}`,
		})
	srv := bootAnimTestServer(t, true, "")

	_, body := doJSON(t, "GET", srv.URL+"/api/bootanim", "")
	var list struct {
		Themes []BootAnimTheme `json:"themes"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Themes) != 1 {
		t.Fatalf("list: %s", body)
	}
	c := list.Themes[0]
	if c.DurationMs != 3200 || c.MaxMs != 6400 || !c.Interactive || c.MinViewport != 480 {
		t.Errorf("theme.json timings were not inherited: %+v", c)
	}
	if c.Description != "来自 theme.json" || c.Version != "9.9.9" {
		t.Errorf("theme.json metadata was not inherited: %+v", c)
	}
	// options are sanitized: a key with a space or a value longer than 64 chars
	// is dropped, and the keys the player owns (speed/name/mute/mode) never
	// reach the clip.
	page, _ := fetchBody(t, srv.URL+"/")
	if !strings.Contains(page, `"options":{"brand":"Mini Code","n":1}`) {
		t.Errorf("options were not sanitized into the player config: %s", page)
	}
}

// TestBootClipSelection: the config may pin one clip; otherwise the first by id
// plays (a boot screen is global, there is no way to show two).
func TestBootClipSelection(t *testing.T) {
	home := pluginHome(t)
	for _, id := range []string{"boot-b", "boot-a"} {
		installBootPlugin(t, home, id,
			fmt.Sprintf(`{"id":%q,"name":%q,"boot":{"entry":"index.html"}}`, id, id),
			map[string]string{"index.html": bootEntryHTML})
	}
	// No pin: boot-a wins (sorted by id, deterministic).
	srv := bootAnimTestServer(t, true, "")
	page, _ := fetchBody(t, srv.URL+"/")
	if !strings.Contains(page, `"entry":"/bootanim/boot-a/index.html"`) {
		t.Errorf("without a pin the first clip by id should play: %s", page)
	}
	// Pinned: boot-b.
	srvB := bootAnimTestServer(t, true, "boot-b")
	page, _ = fetchBody(t, srvB.URL+"/")
	if !strings.Contains(page, `"entry":"/bootanim/boot-b/index.html"`) {
		t.Errorf("a pinned clip must win: %s", page)
	}
	// Pinned to something that is not installed: nothing plays (no surprise).
	srvC := bootAnimTestServer(t, true, "boot-missing")
	page, _ = fetchBody(t, srvC.URL+"/")
	if !strings.Contains(page, `"mc-boot-cfg" type="application/json">null<`) {
		t.Errorf("a missing pinned clip should play nothing: %s", page)
	}
	// Switched off globally: nothing plays even though a clip is installed.
	srvOff := bootAnimTestServer(t, false, "")
	page, _ = fetchBody(t, srvOff.URL+"/")
	if !strings.Contains(page, `<script id="mc-boot-cfg" type="application/json">null</script>`) {
		t.Error("a disabled boot config should leave the player switched off")
	}
}

// TestLegacyBootThemeStillPlays: clips installed before the plugin layer existed
// keep working, and are labelled as legacy so the page can suggest installing the
// plugin version.
func TestLegacyBootThemeStillPlays(t *testing.T) {
	pluginHome(t) // the plugin directory stays empty
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "old-clip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-clip", "theme.json"),
		[]byte(`{"id":"old-clip","name":"旧主题","version":"1.0.0","entry":"index.html","durationMs":1500,"maxMs":3000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-clip", "index.html"), []byte(bootEntryHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.AppConfig{BootAnim: config.BootAnimConfig{
		Enabled: true, Theme: "old-clip", Mode: config.BootAnimModeSession,
		Dir: dir, Repo: "tester/plugins",
	}}
	srv := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	t.Cleanup(srv.Close)

	_, body := doJSON(t, "GET", srv.URL+"/api/bootanim", "")
	var list struct {
		Themes []BootAnimTheme `json:"themes"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Themes) != 1 {
		t.Fatalf("legacy list: %s", body)
	}
	if list.Themes[0].Source != "legacy" {
		t.Errorf("a theme.json directory must be labelled legacy: %+v", list.Themes[0])
	}
	resp, err := http.Get(srv.URL + "/bootanim/old-clip/index.html")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("a legacy clip must still be served, got %d", resp.StatusCode)
	}
	page, _ := fetchBody(t, srv.URL+"/")
	if !strings.Contains(page, `"entry":"/bootanim/old-clip/index.html"`) {
		t.Errorf("a legacy clip must still play: %s", page)
	}
}

// TestBootPlaceholderMatchesUI: the marker the server replaces must exist
// verbatim in the embedded page, or the overlay silently stops working.
func TestBootPlaceholderMatchesUI(t *testing.T) {
	page, err := embeddedPage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, bootAnimPlaceholder) {
		t.Fatalf("index.html lost the player placeholder %q", bootAnimPlaceholder)
	}
}

// TestBootAnimRelPathAndConfig keeps the path/id/config guards honest.
func TestBootAnimRelPathAndConfig(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want bool
	}{
		{"index.html", true},
		{"assets/a.css", true},
		{"a b.svg", true},
		{"", false},
		{"../evil", false},
		{"a/../../evil", false},
		{"/etc/passwd", false},
		{"..\\evil", false},
		{"c:/windows/win.ini", false},
		{"./x", true},  // path.Clean 归一化后仍在目录内
		{"a//b", true}, // 同上:多余斜杠不构成越界
	} {
		if got := bootAnimRelPathOK(tc.p); got != tc.want {
			t.Errorf("bootAnimRelPathOK(%q) = %v, want %v", tc.p, got, tc.want)
		}
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"mc-access", true}, {"a1", true}, {"", false}, {"A1", false},
		{"a..b", false}, {"../x", false}, {"a/b", false}, {"-a", false},
	} {
		if got := config.ValidBootAnimID(tc.id); got != tc.want {
			t.Errorf("ValidBootAnimID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
	// default mode/repo + clamping
	var b config.BootAnimConfig
	b.Normalize()
	if b.Mode != config.BootAnimModeSession || b.Repo != config.DefaultBootAnimRepo {
		t.Errorf("Normalize left defaults: %+v", b)
	}
	b2 := config.BootAnimConfig{Mode: "weekly"}
	b2.Normalize()
	if b2.Mode != config.BootAnimModeSession {
		t.Errorf("Normalize did not fix an unknown mode: %+v", b2)
	}
}

// TestBootClipOptionsMuteAndAudio: the clip's options are sanitized into the
// player config, Mute is injected, and the clip's own audio files are served
// with an audio content type and a CSP that allows media.
func TestBootClipOptionsMuteAndAudio(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-demo",
		`{"id":"boot-demo","name":"demo","boot":{"entry":"index.html","durationMs":2000,"maxMs":4000}}`,
		map[string]string{
			"index.html":      "<html></html>",
			"voice-hello.wav": "RIFF....WAVEfmt ",
			"theme.json": `{"id":"boot-demo","options":{"brand":"Mini Code","sub":"欢迎来到人类联盟",
				"speed":9,"Bad Key":"x","n":true,"long":"` + strings.Repeat("y", 80) + `"}}`,
		})
	cfg := &config.AppConfig{BootAnim: config.BootAnimConfig{
		Enabled: true, Mode: config.BootAnimModeSession, Mute: true,
	}}
	srv := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	t.Cleanup(srv.Close)

	body, err := fetchBody(t, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	// map keys serialize alphabetically; only brand / n / sub survive (the
	// space-containing key, the over-long value and the reserved speed are gone).
	if !strings.Contains(body, `"options":{"brand":"Mini Code","n":1,"sub":"欢迎来到人类联盟"}`) {
		t.Errorf("options 没有被正确收敛/注入: %s", body)
	}
	if !strings.Contains(body, `"mute":true`) {
		t.Errorf("静音开关没有注入: %s", body)
	}
	resp, err := http.Get(srv.URL + "/bootanim/boot-demo/voice-hello.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("语音文件应当可取,得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("语音文件 Content-Type = %q", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "media-src") {
		t.Errorf("CSP 必须放行 media-src,主题才放得出声音: %s", csp)
	}
}

func fetchBody(t *testing.T, url string) (string, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %d", url, resp.StatusCode)
	}
	return string(b), err
}
