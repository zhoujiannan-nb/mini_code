package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/user/mini_code/config"
)

// bootPlayState reads the live play settings through the API the UI uses.
func bootPlayState(t *testing.T, url string) map[string]any {
	t.Helper()
	code, body := doJSON(t, "GET", url+"/api/bootanim", "")
	if code != 200 {
		t.Fatalf("GET /api/bootanim = %d\n%s", code, body)
	}
	var payload struct {
		Play map[string]any `json:"play"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload: %v\n%s", err, body)
	}
	return payload.Play
}

// savingBackend is fakeBackend that actually remembers what it was handed, so
// tests can tell "the patch was applied" from "the patch was parsed".
type savingBackend struct {
	fakeBackend
	mu  sync.Mutex
	cur *config.AppConfig
}

func (s *savingBackend) GetConfig() *config.AppConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		s.cur = DefaultBootAnimConfigForTest()
	}
	return s.cur
}

func (s *savingBackend) SaveConfig(cfg *config.AppConfig) (*SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = cfg
	return &SaveResult{}, nil
}

// DefaultBootAnimConfigForTest is the live config a test starts from: boot
// animation on, nothing pinned, defaults for the rest.
func DefaultBootAnimConfigForTest() *config.AppConfig {
	cfg := &config.AppConfig{
		Model: config.ModelConfig{Provider: "vllm", ModelName: "test"},
	}
	cfg.BootAnim = config.DefaultBootAnimConfig()
	return cfg
}

// TestBootAnimPatchUpdatesOnlyTheBootBlock: the play switches are edited from a
// boot plugin's detail pane, so they need their own endpoint. Posting the whole
// config from there would mean shipping the model list — and its API keys —
// just to turn a checkbox, and the model list would have to be valid.
//
// A config with no models at all still has to accept the patch.
func TestBootAnimPatchUpdatesOnlyTheBootBlock(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-a",
		`{"id":"boot-a","name":"A","boot":{"entry":"index.html","durationMs":2000,"maxMs":4000}}`,
		map[string]string{"index.html": bootEntryHTML})

	srv := httptest.NewServer(NewServer(&savingBackend{}))
	defer srv.Close()

	// Start from a known state.
	if code, body := doJSON(t, "PUT", srv.URL+"/api/bootanim",
		`{"theme":"boot-a","enabled":true,"mode":"daily","mute":true}`); code != 200 {
		t.Fatalf("first patch = %d\n%s", code, body)
	}
	play := bootPlayState(t, srv.URL)
	if play["theme"] != "boot-a" || play["enabled"] != true ||
		play["mode"] != "daily" || play["mute"] != true {
		t.Fatalf("the patch did not land: %+v", play)
	}

	// Omitting a field must keep it (a pointer-per-field merge, not a replace).
	if code, body := doJSON(t, "PUT", srv.URL+"/api/bootanim", `{"mute":false}`); code != 200 {
		t.Fatalf("partial patch = %d\n%s", code, body)
	}
	play = bootPlayState(t, srv.URL)
	if play["mute"] != false {
		t.Errorf("mute was not cleared: %+v", play)
	}
	if play["theme"] != "boot-a" || play["mode"] != "daily" || play["enabled"] != true {
		t.Errorf("an omitted field was wiped: %+v", play)
	}

	// The speed multiplier is gone; sending it must not resurrect it.
	if code, body := doJSON(t, "PUT", srv.URL+"/api/bootanim", `{"speed":2.5}`); code != 200 {
		t.Fatalf("a legacy speed field should be ignored, got %d\n%s", code, body)
	}
	if _, ok := play["speed"]; ok {
		t.Error("play settings still advertise a speed")
	}
}

// TestBootAnimPatchRejectsUnknownClip: selecting a clip that is not installed
// would leave the player with nothing to draw, so it is a 400 with a readable
// reason instead of a silently blank startup.
func TestBootAnimPatchRejectsUnknownClip(t *testing.T) {
	home := pluginHome(t)
	installBootPlugin(t, home, "boot-a",
		`{"id":"boot-a","name":"A","boot":{"entry":"index.html"}}`,
		map[string]string{"index.html": bootEntryHTML})

	srv := httptest.NewServer(NewServer(newTestBackend(t)))
	defer srv.Close()

	code, body := doJSON(t, "PUT", srv.URL+"/api/bootanim", `{"theme":"not-installed"}`)
	if code != 400 {
		t.Fatalf("unknown clip = %d, want 400\n%s", code, body)
	}
	if !strings.Contains(body, "not-installed") {
		t.Errorf("the error does not name the clip: %s", body)
	}
	// And the previous selection must survive a rejected patch.
	play := bootPlayState(t, srv.URL)
	if play["theme"] == "not-installed" {
		t.Errorf("a rejected patch still changed the selection: %+v", play)
	}
}

// TestBootAnimPatchRejectsBadMode: the mode is an enum; a typo must be refused
// rather than normalized into something the user did not ask for.
func TestBootAnimPatchRejectsBadMode(t *testing.T) {
	pluginHome(t)
	srv := httptest.NewServer(NewServer(&savingBackend{}))
	defer srv.Close()
	code, _ := doJSON(t, "PUT", srv.URL+"/api/bootanim", `{"mode":"weekly"}`)
	if code != 400 {
		t.Errorf("bad mode = %d, want 400", code)
	}
	if code, _ := doJSON(t, "PUT", srv.URL+"/api/bootanim", `not json`); code != 400 {
		t.Errorf("bad body = %d, want 400", code)
	}
}

// TestBootConfigHasNoSpeedField: the multiplier is gone from the model, so a
// config.json written by an older build still loads (unknown fields are
// ignored) and Normalize leaves no speed behind.
func TestBootConfigHasNoSpeedField(t *testing.T) {
	var b config.BootAnimConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"theme":"boot-a","mode":"always","speed":2.5}`), &b); err != nil {
		t.Fatalf("an older config must still unmarshal: %v", err)
	}
	b.Normalize()
	if err := b.Validate(); err != nil {
		t.Fatalf("an older config must still validate: %v", err)
	}
	if b.Mode != config.BootAnimModeAlways || b.Theme != "boot-a" {
		t.Errorf("the known fields were lost: %+v", b)
	}
	out, _ := json.Marshal(b)
	if strings.Contains(string(out), "speed") {
		t.Errorf("a speed field was written back out: %s", out)
	}
}
