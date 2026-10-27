package web

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doJSON issues a request and decodes the JSON body.
func doJSON(t *testing.T, method, url string, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestPluginCentreListsDisabledPlugins: the centre must show the switched-off
// ones (that is where you turn them back on) while the agent's Load keeps hiding
// them. This is the whole reason Discover exists next to Load.
func TestPluginCentreListsDisabledPlugins(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "on-skin", `{"id":"on-skin","name":"On Skin","styles":["s.css"]}`,
		map[string]string{"s.css": "body{}"})
	installPlugin(t, home, "off-skin", `{"id":"off-skin","name":"Off Skin","styles":["s.css"]}`,
		map[string]string{"s.css": "body{}"})
	installPlugin(t, home, "manifest-off", `{"id":"manifest-off","name":"Manifest Off","enabled":false}`, nil)

	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	// Turn one off through the API (the state file, not the manifest).
	code, body := doJSON(t, "POST", srv.URL+"/api/plugins/off-skin/toggle", `{"enabled":false}`)
	if code != 200 {
		t.Fatalf("toggle status = %d (%s)", code, body)
	}

	code, body = doJSON(t, "GET", srv.URL+"/api/plugins", "")
	if code != 200 {
		t.Fatalf("list status = %d", code)
	}
	for _, want := range []string{"on-skin", "off-skin", "manifest-off"} {
		if !strings.Contains(body, want) {
			t.Errorf("centre list is missing %q\n%s", want, body)
		}
	}
	var payload struct {
		Plugins []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		} `json:"plugins"`
		Dir   string   `json:"dir"`
		Slots []string `json:"slots"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	byID := map[string]bool{}
	reason := map[string]string{}
	for _, p := range payload.Plugins {
		byID[p.ID] = p.Enabled
		reason[p.ID] = p.Reason
	}
	if !byID["on-skin"] {
		t.Error("on-skin should be enabled")
	}
	if byID["off-skin"] {
		t.Error("off-skin was toggled off but still reports enabled")
	}
	if reason["off-skin"] == "" {
		t.Error("a disabled plugin must explain why")
	}
	if byID["manifest-off"] {
		t.Error("a manifest with enabled:false must stay off")
	}
	if payload.Dir == "" {
		t.Error("the list must report the plugin directory")
	}
	if len(payload.Slots) == 0 {
		t.Error("the list must report the slots the page provides")
	}
}

// TestPluginToggleIsIdempotentAndReversible checks the state file round-trip:
// off twice, on again, and the plugin comes back.
func TestPluginToggleIsIdempotentAndReversible(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["s.css"]}`, map[string]string{"s.css": "body{}"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		if code, body := doJSON(t, "POST", srv.URL+"/api/plugins/skin/toggle", `{"enabled":false}`); code != 200 {
			t.Fatalf("toggle off #%d: %d %s", i, code, body)
		}
	}
	// The state file must list the id exactly once.
	statePath := filepath.Join(home, ".mini_code", "plugins.state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if n := strings.Count(string(raw), `"skin"`); n != 1 {
		t.Fatalf("state file has %d entries for skin:\n%s", n, raw)
	}
	if code, body := doJSON(t, "POST", srv.URL+"/api/plugins/skin/toggle", `{"enabled":true}`); code != 200 {
		t.Fatalf("toggle on: %d %s", code, body)
	}
	raw, _ = os.ReadFile(statePath)
	if strings.Contains(string(raw), `"skin"`) {
		t.Fatalf("enabling did not clear the entry:\n%s", raw)
	}
}

// TestPluginToggleRejectsMissingBody: an absent "enabled" must be an error, not
// a silent "false" (which would switch a plugin off on a malformed click).
func TestPluginToggleRejectsMissingBody(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin"}`, nil)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	for _, body := range []string{`{}`, `{"enabled":null}`, `not json`} {
		if code, _ := doJSON(t, "POST", srv.URL+"/api/plugins/skin/toggle", body); code != 400 {
			t.Errorf("body %q: status = %d, want 400", body, code)
		}
	}
}

// TestPluginToggleUnknown404.
func TestPluginToggleUnknown404(t *testing.T) {
	pluginHome(t)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()
	if code, _ := doJSON(t, "POST", srv.URL+"/api/plugins/nope/toggle", `{"enabled":true}`); code != 404 {
		t.Errorf("status = %d, want 404", code)
	}
}

// TestPluginUninstall: the directory is removed, and a directory without a
// plugin.json is refused (we must never delete something we cannot identify).
func TestPluginUninstall(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["s.css"]}`, map[string]string{"s.css": "body{}"})
	// A stray directory that is not a plugin.
	junk := filepath.Join(home, ".mini_code", "plugins", "junk")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	if code, body := doJSON(t, "DELETE", srv.URL+"/api/plugins/skin", ""); code != 200 {
		t.Fatalf("uninstall: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, ".mini_code", "plugins", "skin")); !os.IsNotExist(err) {
		t.Error("the plugin directory was not removed")
	}
	// Without a manifest there is nothing identifying it: refuse.
	if code, _ := doJSON(t, "DELETE", srv.URL+"/api/plugins/junk", ""); code != 400 {
		t.Errorf("junk uninstall status = %d, want 400", code)
	}
	if _, err := os.Stat(junk); err != nil {
		t.Error("a non-plugin directory must not be deleted")
	}
}

// TestPluginProcessAPINotAProcessPlugin: asking a skin to start must be a 400,
// not a 500 — the message has to point at the real mistake.
func TestPluginProcessAPINotAProcessPlugin(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "skin", `{"id":"skin","styles":["s.css"]}`, map[string]string{"s.css": "body{}"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	code, body := doJSON(t, "POST", srv.URL+"/api/plugins/skin/process", `{"action":"start"}`)
	if code != 400 || !strings.Contains(body, "程序类") {
		t.Fatalf("status=%d body=%s, want 400 explaining it is not a process plugin", code, body)
	}
	if code, _ := doJSON(t, "POST", srv.URL+"/api/plugins/skin/process", `{"action":"explode"}`); code != 400 {
		t.Errorf("unknown action status = %d, want 400", code)
	}
}

// TestPluginProcessListShowsStopped: a process plugin that was never started is
// listed as stopped, with the exact command it would run.
func TestPluginProcessListShowsStopped(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "svc", `{"id":"svc","name":"Svc",
		"process":{"command":"python","args":["-V"],"expose":{"tool":true,
		"endpoint":"http://127.0.0.1:9"},"description":"d"}}`, nil)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	code, body := doJSON(t, "GET", srv.URL+"/api/plugins/processes", "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `"stopped"`) || !strings.Contains(body, "python -V") {
		t.Fatalf("process list = %s", body)
	}
	// The centre row carries the same state inline.
	_, listBody := doJSON(t, "GET", srv.URL+"/api/plugins", "")
	var payload struct {
		Plugins []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Process  *struct {
				State string `json:"state"`
			} `json:"process"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(listBody), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Plugins) != 1 {
		t.Fatalf("plugins = %d", len(payload.Plugins))
	}
	p := payload.Plugins[0]
	if p.Category != "process" {
		t.Errorf("category = %q, want process", p.Category)
	}
	if p.Process == nil || p.Process.State != "stopped" {
		t.Errorf("inline process state = %+v", p.Process)
	}
}

// TestPluginProcessLogsEmpty: no logs yet is an empty array, not null (the UI
// iterates it without a guard).
func TestPluginProcessLogsEmpty(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "svc", `{"id":"svc","process":{"command":"x"}}`, nil)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()
	code, body := doJSON(t, "GET", srv.URL+"/api/plugins/svc/logs", "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `"lines":[]`) {
		t.Errorf("lines is not an empty array: %s", body)
	}
}

// TestPluginMakePrompt: every category yields a prompt naming the real directory
// and the real contract, and an unknown kind is rejected.
func TestPluginMakePrompt(t *testing.T) {
	pluginHome(t)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	for _, kind := range []string{"process", "tool", "appearance"} {
		code, body := doJSON(t, "GET", srv.URL+"/api/plugins/prompt?kind="+kind, "")
		if code != 200 {
			t.Fatalf("%s: status = %d", kind, code)
		}
		var payload struct {
			Kind   string `json:"kind"`
			Prompt string `json:"prompt"`
			Dir    string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("%s: payload: %v", kind, err)
		}
		if payload.Kind != kind {
			t.Errorf("kind = %q, want %q", payload.Kind, kind)
		}
		if !strings.Contains(payload.Prompt, "plugin.json") {
			t.Errorf("%s prompt does not mention plugin.json", kind)
		}
		if !strings.Contains(payload.Prompt, payload.Dir) {
			t.Errorf("%s prompt does not name the plugin directory", kind)
		}
	}
	// The tool prompt must say the parameters arrive on stdin: that is the one
	// mistake every first-time plugin author makes.
	code, body := doJSON(t, "GET", srv.URL+"/api/plugins/prompt?kind=tool", "")
	if code != 200 || !strings.Contains(body, "stdin") {
		t.Errorf("tool prompt does not mention stdin")
	}
	// The appearance prompt must list the real slots and forbid .js.
	_, body = doJSON(t, "GET", srv.URL+"/api/plugins/prompt?kind=appearance", "")
	for _, want := range []string{"composer-bar", "overlay", ".js"} {
		if !strings.Contains(body, want) {
			t.Errorf("appearance prompt is missing %q", want)
		}
	}
	if code, _ := doJSON(t, "GET", srv.URL+"/api/plugins/prompt?kind=nonsense", ""); code != 400 {
		t.Errorf("unknown kind status = %d, want 400", code)
	}
}

// TestSlotFragmentsAreSandboxed is the security assertion for the third kind:
// a slot fragment is rendered in an iframe with a sandbox that grants neither
// scripts nor same-origin, so downloaded HTML cannot call the app's API.
func TestSlotFragmentsAreSandboxed(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "pet-skin", `{"id":"pet-skin","ui":{
		"slots":[{"slot":"composer-bar","html":"bar.html","height":72}],
		"accent":"#ff7500"}}`, map[string]string{
		"bar.html": "<b onclick=\"alert(1)\">hi</b>",
	})
	page, err := embeddedPage()
	if err != nil {
		t.Fatalf("embeddedPage: %v", err)
	}
	got := injectPluginAssets(page)
	for _, want := range []string{
		`data-slot="composer-bar"`,
		`src="/plugins/pet-skin/bar.html"`,
		`sandbox`,
		`height="72"`,
		`--accent:#ff7500`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("injected page is missing %q", want)
		}
	}
	// The fragment must NOT be inlined into the app's own document: if it were,
	// its onclick handler would run in the app's origin.
	if strings.Contains(got, "alert(1)") {
		t.Fatal("slot HTML was inlined into the page instead of framed")
	}
	// And the sandbox must not grant script or same-origin. Check the slot
	// iframe itself (the embedded page has other sandbox usages, e.g. the boot
	// animation player, which legitimately needs allow-scripts).
	i := strings.Index(got, `data-slot="composer-bar"`)
	if i < 0 {
		t.Fatal("slot container not found")
	}
	slot := got[i:]
	if j := strings.Index(slot, "</div>"); j >= 0 {
		slot = slot[:j]
	}
	if !strings.Contains(slot, `sandbox`) {
		t.Fatal("the slot iframe has no sandbox attribute")
	}
	for _, bad := range []string{"allow-scripts", "allow-same-origin"} {
		if strings.Contains(slot, bad) {
			t.Fatalf("the slot iframe sandbox grants %s:\n%s", bad, slot)
		}
	}
}

// TestMotionDeclarationInjected: motion bindings travel as JSON in a
// non-executable script tag, escaped so they cannot close the tag early.
func TestMotionDeclarationInjected(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "shake", `{"id":"shake","ui":{
		"motion":[{"event":"send","class":"mc-shake"},{"event":"error","class":"flash"}]}}`, nil)
	page, _ := embeddedPage()
	got := injectPluginAssets(page)

	if !strings.Contains(got, `id="mc-plugin-ui"`) || !strings.Contains(got, `type="application/json"`) {
		t.Fatal("the motion declaration was not injected as JSON")
	}
	for _, want := range []string{
		`"event":"send"`,
		`"class":"mc-shake"`,
		`"event":"error"`,
		`"class":"flash"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("motion JSON is missing %q", want)
		}
	}
	// The payload must not be able to end the script element.
	if strings.Contains(got, "</script><script") {
		t.Error("the JSON payload is not escaped for </ sequences")
	}
}

// TestAccentActuallyDrivesTheTheme is the regression guard for a real bug:
// ui.accent used to be written as --mc-plugin-accent, a variable NOTHING in
// base.css reads, so a plugin's accent looked accepted and did nothing (it only
// appeared to work in the browser test because the example skin also set
// --accent in its own CSS). The accent must override the variables the shell
// actually uses, in BOTH themes, and the derived text/button shades must stay
// legible instead of handing the theme white-on-white.
func TestAccentActuallyDrivesTheTheme(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "accent", `{"id":"accent","ui":{"accent":"#ff7500"}}`, nil)
	page, _ := embeddedPage()
	got := injectPluginAssets(page)

	for _, want := range []string{
		`id="mc-plugin-accent"`,
		`--accent:#ff7500`,
		// Both themes are overridden: one :root rule alone would lose to
		// base.css's html[data-theme="dark"] block.
		`html[data-theme="dark"]`,
		`--accent-t:`,
		`--accent-btn:`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("accent injection is missing %q", want)
		}
	}
	if strings.Contains(got, "--mc-plugin-accent") {
		t.Error("wrote --mc-plugin-accent, a variable no stylesheet reads")
	}

	// The derived shades must be readable. Text on the light surface (#ffffff)
	// and on the dark surface (#171716) needs a sane ratio; the button fill
	// sits behind WHITE text, so it must not be light.
	shades, ok := accentShades("#ff7500")
	if !ok {
		t.Fatal("accentShades rejected a valid #rrggbb")
	}
	if r := contrast(shades.light.text, "#ffffff"); r < 4.5 {
		t.Errorf("light --accent-t contrast on surface = %.2f, want >=4.5", r)
	}
	if r := contrast(shades.dark.text, "#171716"); r < 4.5 {
		t.Errorf("dark --accent-t contrast on surface = %.2f, want >=4.5", r)
	}
	if r := contrast("#ffffff", shades.light.btn); r < 4.5 {
		t.Errorf("white text on --accent-btn = %.2f, want >=4.5", r)
	}
	if r := contrast("#ffffff", shades.dark.btn); r < 4.5 {
		t.Errorf("white text on dark --accent-btn = %.2f, want >=4.5", r)
	}
	// The accent itself is used verbatim: it is the plugin's stated intent.
	if shades.light.accent != "#ff7500" || shades.dark.accent != "#ff7500" {
		t.Errorf("the accent colour was altered: %s / %s", shades.light.accent, shades.dark.accent)
	}
}

// contrast is the WCAG ratio between two #rrggbb colours.
func contrast(a, b string) float64 {
	la, lb := luma(a), luma(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luma(hex string) float64 {
	r, g, b, ok := parseHex6(hex)
	if !ok {
		return 0
	}
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}

func pow(x, y float64) float64 { return math.Pow(x, y) }

// TestAccentRejectsGarbage: validateUI is the gate, but accentShades must not
// emit a broken stylesheet if a value slips through.
func TestAccentRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "#fff", "ff7500", "#gggggg", "red", "#ff75000"} {
		if _, ok := accentShades(bad); ok {
			t.Errorf("accentShades accepted %q", bad)
		}
	}
}

// TestPetIsNotPartOfTheContract: a desktop pet was removed from the third-kind
// contract on purpose — a web page cannot draw outside its own window, and
// faking it inside the window is a toy. The field must therefore be INERT: it is
// ignored (unknown keys are tolerated for forward compatibility) and nothing
// about it reaches the page. When a pet returns it will be built on top of the
// kind-1 (process) seam, not here.
func TestPetIsNotPartOfTheContract(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "cat", `{"id":"cat","name":"Cat","ui":{
		"pet":{"name":"小黑","frames":["f1.svg","f2.svg"],"frameMs":200,
			"reactions":{"thinking":"spin"}},
		"motion":[{"event":"send","class":"mc-shake"}]}}`, map[string]string{
		"f1.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"f2.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`,
	})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	// The plugin still loads (an unknown key must never break a manifest)…
	_, body := doJSON(t, "GET", srv.URL+"/api/plugins", "")
	if !strings.Contains(body, `"id":"cat"`) {
		t.Fatalf("a manifest with an unknown ui.pet key must still load: %s", body)
	}
	// …but it is advertised nowhere: no pet in kinds, none in the UI summary.
	if strings.Contains(body, `"pet"`) || strings.Contains(body, `"frames"`) {
		t.Errorf("ui.pet leaked into the plugin centre payload: %s", body)
	}
	page, _ := embeddedPage()
	got := injectPluginAssets(page)
	for _, unwanted := range []string{"f1.svg", "f2.svg", "小黑", "mc-pet"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("ui.pet still reaches the page (%q)", unwanted)
		}
	}
	// The motion binding next to it still works, which proves the field was
	// ignored rather than the whole ui block being dropped.
	if !strings.Contains(got, `"class":"mc-shake"`) {
		t.Error("dropping ui.pet must not disable ui.motion")
	}
}

// TestUIValidationRejectsUnknownSlot: a typo in a slot name must fail the load,
// visibly, instead of silently doing nothing.
func TestUIValidationRejectsUnknownSlot(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "typo", `{"id":"typo","ui":{"slots":[{"slot":"composer-barr","html":"x.html"}]}}`,
		map[string]string{"x.html": "<b>x</b>"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	_, body := doJSON(t, "GET", srv.URL+"/api/plugins", "")
	// Discovered but broken: listed with an error so the page can explain it.
	if !strings.Contains(body, "typo") || !strings.Contains(body, `"error"`) {
		t.Fatalf("a broken plugin must be listed with its error: %s", body)
	}
	if !strings.Contains(body, "composer-barr") {
		t.Error("the error does not name the offending slot")
	}
	// And it contributes nothing to the page.
	page, _ := embeddedPage()
	if strings.Contains(injectPluginAssets(page), "typo") {
		t.Error("a plugin that failed validation was still injected")
	}
}

// TestUIValidationRejectsScriptSlot: a fragment must be .html/.svg — a .js file
// cannot even be named, which keeps the "no plugin script" rule honest at the
// manifest level as well as at the asset route.
func TestUIValidationRejectsScriptSlot(t *testing.T) {
	home := pluginHome(t)
	installPlugin(t, home, "sneaky", `{"id":"sneaky","ui":{"slots":[{"slot":"composer-bar","html":"x.js"}]}}`,
		map[string]string{"x.js": "alert(1)"})
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()
	_, body := doJSON(t, "GET", srv.URL+"/api/plugins", "")
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("a .js slot fragment must be rejected: %s", body)
	}
}

// TestCatalogueHandlesUnreachableRepo: an unreachable catalogue is a clean 502
// with a readable message (the UI shows it in the panel), never a hang or a 500.
func TestCatalogueHandlesUnreachableRepo(t *testing.T) {
	pluginHome(t)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()
	code, body := doJSON(t, "GET", srv.URL+"/api/plugins/catalog", "")
	if code != 502 && code != 200 {
		t.Fatalf("status = %d, want 502 (offline) or 200 (online)\n%s", code, body)
	}
	if code == 502 && !strings.Contains(body, "error") {
		t.Errorf("the failure is not explained: %s", body)
	}
}

// TestCatalogueInstallRejectsBadID guards the install entry point: the id comes
// straight from a click, so it must be validated before any path is built.
func TestCatalogueInstallRejectsBadID(t *testing.T) {
	pluginHome(t)
	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()
	for _, body := range []string{`{"id":"../etc"}`, `{"id":""}`, `{"id":"A-B"}`, `not json`} {
		if code, _ := doJSON(t, "POST", srv.URL+"/api/plugins/install", body); code != 400 {
			t.Errorf("body %q: status = %d, want 400", body, code)
		}
	}
}
