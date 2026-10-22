package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/config"
)

// bootAnimTestTheme is the smallest theme the host accepts: a manifest whose id
// matches the directory plus its entry file.
const bootAnimTestTheme = `{"id":"t1","name":"测试主题","version":"1.2.3","entry":"index.html","durationMs":2000,"maxMs":4000,"description":"单测用"}`

func newBootAnimTestServer(t *testing.T, dir string, enabled bool) *httptest.Server {
	t.Helper()
	cfg := &config.AppConfig{
		BootAnim: config.BootAnimConfig{
			Enabled: enabled, Theme: "t1", Mode: config.BootAnimModeSession,
			Speed: 1, Dir: dir, Repo: "tester/plugins",
		},
	}
	srv := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBootAnimListAndAsset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "t1", "theme.json"), bootAnimTestTheme)
	must(filepath.Join(dir, "t1", "index.html"), "<!DOCTYPE html><html><body>hi</body></html>")
	must(filepath.Join(dir, "t1", "style.css"), "body{color:red}")
	// A directory that looks like a theme but whose entry is missing: it must be
	// reported as broken instead of being offered as playable.
	must(filepath.Join(dir, "broken", "theme.json"), `{"id":"broken","name":"坏主题","entry":"gone.html"}`)
	// Staging leftovers must never show up as themes.
	must(filepath.Join(dir, ".install-t1-abcd", "theme.json"), bootAnimTestTheme)
	// A file next to the themes that must not be readable through the theme path.
	must(filepath.Join(filepath.Dir(dir), "secret.txt"), "TOPSECRET")

	srv := newBootAnimTestServer(t, dir, true)

	// --- listing ---
	resp, err := http.Get(srv.URL + "/api/bootanim")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var list struct {
		Dir    string          `json:"dir"`
		Themes []BootAnimTheme `json:"themes"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list %s: %v", body, err)
	}
	if len(list.Themes) != 2 {
		t.Fatalf("want 2 themes (t1 + broken), got %d: %s", len(list.Themes), body)
	}
	var t1, broken *BootAnimTheme
	for i := range list.Themes {
		switch list.Themes[i].ID {
		case "t1":
			t1 = &list.Themes[i]
		case "broken":
			broken = &list.Themes[i]
		}
	}
	if t1 == nil || t1.Error != "" || t1.URL != "/bootanim/t1/index.html" || t1.Version != "1.2.3" {
		t.Fatalf("t1 not listed correctly: %+v", t1)
	}
	if broken == nil || broken.Error == "" {
		t.Fatalf("broken theme should carry an error: %+v", broken)
	}
	if list.Dir != dir {
		t.Fatalf("dir = %q, want %q", list.Dir, dir)
	}

	// --- asset serving ---
	resp, err = http.Get(srv.URL + "/bootanim/t1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hi") {
		t.Fatalf("entry: %d %s", resp.StatusCode, body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'none'", "frame-src 'none'", "script-src 'self' 'unsafe-inline'", "form-action 'none'"} {
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
	// A second file of the same theme (multi-file themes must work).
	if r, err := http.Get(srv.URL + "/bootanim/t1/style.css"); err != nil || r.StatusCode != 200 {
		t.Fatalf("sub-file: %v %v", err, r.StatusCode)
	}
	// missing file
	if r, _ := http.Get(srv.URL + "/bootanim/t1/nope.html"); r.StatusCode != 404 {
		t.Errorf("missing file: want 404, got %d", r.StatusCode)
	}
	// id that is not a theme dir
	if r, _ := http.Get(srv.URL + "/bootanim/nope/index.html"); r.StatusCode != 404 {
		t.Errorf("unknown theme: want 404, got %d", r.StatusCode)
	}
	// file type outside the whitelist
	if r, _ := http.Get(srv.URL + "/bootanim/t1/x.exe"); r.StatusCode != 403 {
		t.Errorf("exe: want 403, got %d", r.StatusCode)
	}
	// traversal attempts: must never return the neighbouring secret
	for _, u := range []string{
		srv.URL + "/bootanim/t1/../../secret.txt",
		srv.URL + "/bootanim/t1/%2e%2e%2f%2e%2e%2fsecret.txt",
		srv.URL + "/bootanim/t1/..%2fsecret.txt",
		srv.URL + "/bootanim/t1/../../../../../../Windows/win.ini",
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
	// The theme directory itself is not listable.
	if r, _ := http.Get(srv.URL + "/bootanim/t1/"); r.StatusCode == 200 {
		t.Error("theme directory should not be served")
	}
}

// TestBootAnimSandboxCannotReachAPI documents the intent of the CSP: a theme
// cannot call the app's own endpoints from inside its frame.
func TestBootAnimSandboxCannotReachAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "t1"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := `<script>fetch("/api/config").then(function(r){r.text()}).catch(function(){});postMessage({mcBoot:true,msg:"ready"},'*')</script>`
	os.WriteFile(filepath.Join(dir, "t1", "theme.json"), []byte(bootAnimTestTheme), 0o644)
	os.WriteFile(filepath.Join(dir, "t1", "index.html"), []byte(entry), 0o644)
	srv := newBootAnimTestServer(t, dir, true)

	resp, err := http.Get(srv.URL + "/bootanim/t1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	// connect-src 'none' is what blocks the fetch above; the iframe additionally
	// runs with sandbox="allow-scripts" (no same-origin), asserted in the browser
	// test, so it has no access to the UI's origin at all.
	if !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("theme must not be able to call the API: %s", csp)
	}
}

// fakePluginRepo serves an index.json plus the theme files it lists, so the
// installer can be tested without touching GitHub.
func fakePluginRepo(t *testing.T, files map[string]string, breakHash bool) *httptest.Server {
	t.Helper()
	repo := "tester/plugins"
	digest := func(p string) string {
		sum := sha256.Sum256([]byte(files[p]))
		return hex.EncodeToString(sum[:])
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	// deterministic order
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	fileList := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		h := digest(p)
		if breakHash && strings.HasSuffix(p, "index.html") {
			h = strings.Repeat("0", 64)
		}
		fileList = append(fileList, map[string]any{"path": p, "size": len(files[p]), "sha256": h})
	}
	index := map[string]any{
		"schema": 1,
		"repo":   "https://github.com/" + repo,
		"ref":    "main",
		"kinds": map[string]any{
			"boot-anim": []map[string]any{{
				"id": "demo", "name": "示例动画", "version": "1.0.0",
				"path": "boot-anim/demo", "entry": "index.html",
				"durationMs": 1500, "maxMs": 3000, "files": fileList,
			}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/"+repo+"/main/")
		if r.URL.Path == "/"+repo+"/main/index.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(index)
			return
		}
		if body, ok := files[p]; ok {
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBootAnimInstall(t *testing.T) {
	repoFiles := map[string]string{
		"boot-anim/demo/theme.json": `{"id":"demo","name":"示例动画","version":"1.0.0","entry":"index.html","durationMs":1500,"maxMs":3000}`,
		"boot-anim/demo/index.html": "<!DOCTYPE html><html><body>demo</body></html>",
		"boot-anim/demo/style.css":  "body{background:#000}",
	}
	repo := fakePluginRepo(t, repoFiles, false)

	dir := t.TempDir()
	srv := newBootAnimTestServer(t, dir, false)
	restore := bootAnimGitHubBase
	bootAnimGitHubBase = repo.URL
	t.Cleanup(func() { bootAnimGitHubBase = restore })
	// The remote catalogue is cached per repo for a minute; start clean.
	remoteCache.mu.Lock()
	remoteCache.key, remoteCache.at, remoteCache.idx, remoteCache.err = "", time.Time{}, nil, nil
	remoteCache.mu.Unlock()

	// remote listing
	resp, err := http.Get(srv.URL + "/api/bootanim/remote")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var remote struct {
		Themes []BootAnimTheme `json:"themes"`
	}
	if err := json.Unmarshal(body, &remote); err != nil {
		t.Fatalf("remote: %s", body)
	}
	if len(remote.Themes) != 1 || remote.Themes[0].ID != "demo" || remote.Themes[0].Installed {
		t.Fatalf("unexpected remote list: %s", body)
	}

	// install
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/bootanim/install",
		strings.NewReader(`{"id":"demo"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("install: %d %s", resp.StatusCode, body)
	}
	for _, f := range []string{"theme.json", "index.html", "style.css"} {
		if _, err := os.Stat(filepath.Join(dir, "demo", f)); err != nil {
			t.Errorf("%s not installed: %v", f, err)
		}
	}
	// no staging leftovers
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".install-") || strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("leftover staging entry %s", e.Name())
		}
	}
	// and the theme now plays
	resp, err = http.Get(srv.URL + "/bootanim/demo/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "demo") {
		t.Fatalf("installed theme: %d %s", resp.StatusCode, body)
	}
	// listing says installed + latest
	resp, _ = http.Get(srv.URL + "/api/bootanim/remote")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	remote.Themes = nil
	if err := json.Unmarshal(body, &remote); err != nil || len(remote.Themes) != 1 ||
		!remote.Themes[0].Installed || !remote.Themes[0].Latest {
		t.Fatalf("remote after install: %s", body)
	}

	// delete
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/bootanim/demo", nil)
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
		t.Error("theme dir still there after delete")
	}
}

func TestBootAnimInstallRejectsBadHash(t *testing.T) {
	repoFiles := map[string]string{
		"boot-anim/demo/theme.json": `{"id":"demo","name":"示例动画","version":"1.0.0","entry":"index.html","durationMs":1500,"maxMs":3000}`,
		"boot-anim/demo/index.html": "<!DOCTYPE html><html><body>demo</body></html>",
	}
	repo := fakePluginRepo(t, repoFiles, true) // index.html hash is wrong

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	const previous = "PREVIOUS VERSION"
	os.WriteFile(filepath.Join(dir, "demo", "theme.json"),
		[]byte(`{"id":"demo","name":"旧版","version":"0.9.0","entry":"old.html"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "demo", "old.html"), []byte(previous), 0o644)

	srv := newBootAnimTestServer(t, dir, true)
	restore := bootAnimGitHubBase
	bootAnimGitHubBase = repo.URL
	t.Cleanup(func() { bootAnimGitHubBase = restore })
	remoteCache.mu.Lock()
	remoteCache.key, remoteCache.at, remoteCache.idx, remoteCache.err = "", time.Time{}, nil, nil
	remoteCache.mu.Unlock()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/bootanim/install",
		strings.NewReader(`{"id":"demo"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("install should fail on a hash mismatch: %s", body)
	}
	if !strings.Contains(string(body), "校验") {
		t.Logf("error was: %s", body)
	}
	// The previously installed theme must be untouched (no half install).
	got, err := os.ReadFile(filepath.Join(dir, "demo", "old.html"))
	if err != nil || string(got) != previous {
		t.Errorf("old theme was clobbered by the failed install: %v %q", err, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo", "index.html")); err == nil {
		t.Error("partially installed file survived")
	}
}

func TestBootAnimIndexInjection(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "t1"), 0o755)
	os.WriteFile(filepath.Join(dir, "t1", "theme.json"), []byte(bootAnimTestTheme), 0o644)
	os.WriteFile(filepath.Join(dir, "t1", "index.html"), []byte("<html></html>"), 0o644)

	// enabled → the player config is inlined, so the overlay is decided before
	// the first paint instead of after an extra API round-trip
	srv := newBootAnimTestServer(t, dir, true)
	body, err := fetchBody(t, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, bootAnimPlaceholder) {
		t.Fatal("placeholder was not replaced — the marker in the UI and the server must match")
	}
	for _, want := range []string{`"entry":"/bootanim/t1/index.html"`, `"maxMs":4000`, `"mode":"session"`, `"enabled":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("injected boot config missing %s", want)
		}
	}

	// disabled → stays "null", the player must not touch the page at all
	srvOff := newBootAnimTestServer(t, dir, false)
	body, err = fetchBody(t, srvOff.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `<script id="mc-boot-cfg" type="application/json">null</script>`) {
		t.Error("disabled config should leave the player switched off")
	}

	// theme not installed → nothing to play even though it is enabled
	srv2 := newBootAnimTestServer(t, t.TempDir(), true)
	body, err = fetchBody(t, srv2.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"mc-boot-cfg" type="application/json">null<`) {
		t.Errorf("missing theme should not produce a player config: %s", body)
	}
}

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
		{"./x", true},  // path.Clean 归一化后仍在主题目录内
		{"a//b", true}, // 同上:多余斜杠不构成越界
	} {
		if got := bootAnimRelPathOK(tc.p); got != tc.want {
			t.Errorf("bootAnimRelPathOK(%q) = %v, want %v", tc.p, got, tc.want)
		}
	}
	// theme ids
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"mc-minimal", true}, {"a1", true}, {"", false}, {"A1", false},
		{"a..b", false}, {"../x", false}, {"a/b", false}, {"-a", false},
	} {
		if got := config.ValidBootAnimID(tc.id); got != tc.want {
			t.Errorf("ValidBootAnimID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
	// default dir + speed clamping
	var b config.BootAnimConfig
	b.Normalize()
	if b.Speed != config.DefaultBootAnimSpeed || b.Mode != config.BootAnimModeSession || b.Repo != config.DefaultBootAnimRepo {
		t.Errorf("Normalize left defaults: %+v", b)
	}
	if p, err := b.DirPath(); err != nil || filepath.Base(p) != "boot-anim" {
		t.Errorf("DirPath default = %q, %v", p, err)
	}
	home, _ := os.UserHomeDir()
	b.Dir = `~\my-themes`
	if p, err := b.DirPath(); err != nil || p != filepath.Join(home, "my-themes") {
		t.Errorf("DirPath(~) = %q, %v", p, err)
	}
	b2 := config.BootAnimConfig{Mode: "weekly", Speed: 99}
	b2.Normalize()
	if b2.Mode != config.BootAnimModeSession || b2.Speed != config.MaxBootAnimSpeed {
		t.Errorf("Normalize did not clamp: %+v", b2)
	}
}

// TestBootAnimBundleRelPath: a catalogue entry may only place files inside its
// own theme directory, with an allowed extension.
func TestBootAnimBundleRelPath(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want string
		ok   bool
	}{
		{"boot-anim/demo/index.html", "index.html", true},
		{"boot-anim/demo/assets/a.css", "assets/a.css", true},
		{"boot-anim/demo/./x.png", "x.png", true},
		{"boot-anim/other/index.html", "", false},
		{"boot-anim/demo/../../x.txt", "", false},
		{"index.html", "", false},
		{"/etc/passwd", "", false},
		{"boot-anim/demo/x.exe", "", false},
		{"boot-anim/demo/../demo/x.png", "x.png", true},
	} {
		got, err := bootAnimBundleRelPath("boot-anim/demo", tc.p)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("bootAnimBundleRelPath(%q) = %q, %v; want %q", tc.p, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("bootAnimBundleRelPath(%q) = %q, want rejection", tc.p, got)
		}
	}
}

// TestBootAnimInstallFromRealRepo is the only test that touches the network: it
// installs mc-minimal from the real plugin repo, which also proves index.json's
// files[] (paths + sha256) and the raw URL layout match the host. Run with
// MC_LIVE=1 go test -run RealRepo ./web/.
func TestBootAnimInstallFromRealRepo(t *testing.T) {
	if os.Getenv("MC_LIVE") == "" {
		t.Skip("set MC_LIVE=1 to test against github.com")
	}
	dir := t.TempDir()
	cfg := &config.AppConfig{BootAnim: config.BootAnimConfig{
		Enabled: true, Theme: "mc-minimal", Mode: config.BootAnimModeSession,
		Speed: 1, Dir: dir, Repo: "zhoujiannan-nb/mini_code-plugins",
	}}
	ts := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	t.Cleanup(ts.Close)
	srv := ts
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/bootanim/install",
		strings.NewReader(`{"id":"mc-minimal"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("install from github: %d %s", resp.StatusCode, body)
	}
	for _, f := range []string{"theme.json", "index.html"} {
		if _, err := os.Stat(filepath.Join(dir, "mc-minimal", f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	r, err := http.Get(srv.URL + "/bootanim/mc-minimal/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Security-Policy"), "connect-src") {
		t.Fatalf("served installed theme: %d", r.StatusCode)
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

// TestBootAnimBootOptionsMuteAndAudio: theme.json 的 options 会被收敛后注入播放器
// (主题不能覆盖宿主自己的 speed/name/interactive/mute),Mute 一并注入,
// 并且主题里的语音文件取得到、CSP 放行 media-src。
func TestBootAnimBootOptionsMuteAndAudio(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "t1"), 0o755); err != nil {
		t.Fatal(err)
	}
	theme := `{"id":"t1","name":"测试主题","version":"1.2.3","entry":"index.html","durationMs":2000,"maxMs":4000,` +
		`"options":{"brand":"Mini Code","sub":"欢迎来到人类联盟","speed":9,"Bad Key":"x","n":true,` +
		`"long":"` + strings.Repeat("y", 80) + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "t1", "theme.json"), []byte(theme), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t1", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t1", "voice-hello.wav"), []byte("RIFF....WAVEfmt "), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.AppConfig{BootAnim: config.BootAnimConfig{
		Enabled: true, Theme: "t1", Mode: config.BootAnimModeSession, Speed: 1, Mute: true, Dir: dir,
	}}
	srv := httptest.NewServer(NewServer(&fakeBackend{cfg: cfg}))
	defer srv.Close()

	body, err := fetchBody(t, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	// map 的键按字母序序列化:只有 brand / n / sub 活下来(空格键名、过长的值、
	// 以及宿主保留的 speed 都被丢掉)。
	if !strings.Contains(body, `"options":{"brand":"Mini Code","n":1,"sub":"欢迎来到人类联盟"}`) {
		t.Errorf("options 没有被正确收敛/注入: %s", body)
	}
	if !strings.Contains(body, `"mute":true`) {
		t.Errorf("静音开关没有注入: %s", body)
	}

	resp, err := http.Get(srv.URL + "/bootanim/t1/voice-hello.wav")
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
