package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCategoryExplicit(t *testing.T) {
	cases := map[string]string{
		"process":    CategoryProcess,
		"program":    CategoryProcess,
		"service":    CategoryProcess,
		"tool":       CategoryTool,
		"tools":      CategoryTool,
		"mcp":        CategoryTool,
		"skill":      CategoryTool,
		"appearance": CategoryAppearance,
		"skin":       CategoryAppearance,
		"":           "",
	}
	for kind, want := range cases {
		p := Plugin{Manifest: Manifest{Kind: kind}}
		if want == "" {
			continue
		}
		if got := p.Category(); got != want {
			t.Errorf("Category(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestCategoryInferred(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"process only", Manifest{Process: &ProcessSpec{Command: "x"}}, CategoryProcess},
		{"process+tools", Manifest{Process: &ProcessSpec{Command: "x"},
			Tools: []ToolSpec{{Name: "t", Command: "y"}}}, CategoryProcess},
		{"tools only", Manifest{Tools: []ToolSpec{{Name: "t", Command: "y"}}}, CategoryTool},
		{"styles only", Manifest{Styles: []string{"a.css"}}, CategoryAppearance},
		{"empty", Manifest{}, CategoryAppearance},
	}
	for _, c := range cases {
		p := Plugin{Manifest: c.m}
		if got := p.Category(); got != c.want {
			t.Errorf("%s: Category() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProcessManifestLoads(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "svc", `{
		"id":"svc","name":"Helper",
		"process":{"command":"python","args":["server.py"],"autostart":true,
			"restart":"on-failure","ready_timeout_seconds":15,
			"health_check":{"type":"http","url":"http://127.0.0.1:9/healthz"},
			"expose":{"tool":true,"endpoint":"http://127.0.0.1:9"}},
		"description":"a helper"
	}`, nil)
	got := Load()
	if len(got) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(got))
	}
	p := got[0]
	if p.Process == nil {
		t.Fatal("process spec not parsed")
	}
	if p.Process.Command != "python" || len(p.Process.Args) != 1 {
		t.Fatalf("process = %+v", p.Process)
	}
	if !p.Process.Autostart {
		t.Error("autostart not parsed")
	}
	if p.Process.Expose == nil || !p.Process.Expose.Tool {
		t.Error("expose.tool not parsed")
	}
	if p.Category() != CategoryProcess {
		t.Errorf("Category() = %q", p.Category())
	}
	if k := strings.Join(p.Kinds(), ","); k != "process" {
		t.Errorf("Kinds() = %q", k)
	}
}

func TestProcessRejectsBadRestart(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "bad", `{"id":"bad","process":{"command":"x","restart":"sometimes"}}`, nil)
	if got := Load(); len(got) != 0 {
		t.Fatalf("restart must be always/on-failure/never, got %+v", got)
	}
}

func TestProcessRejectsEmptyCommand(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "bad", `{"id":"bad","process":{"command":"  "}}`, nil)
	if got := Load(); len(got) != 0 {
		t.Fatalf("empty process.command must be rejected, got %+v", got)
	}
}

func TestProcessRejectsHTTPCheckWithoutURL(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "bad", `{"id":"bad","process":{"command":"x","health_check":{"type":"http"}}}`, nil)
	if got := Load(); len(got) != 0 {
		t.Fatalf("http health check without url must be rejected, got %+v", got)
	}
}

func TestProcessClampsReadyTimeout(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "slow", `{"id":"slow","process":{"command":"x","ready_timeout_seconds":9999}}`, nil)
	got := Load()
	if len(got) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(got))
	}
	if got[0].Process.ReadyTimeoutSeconds != 300 {
		t.Errorf("ready timeout = %d, want it clamped to 300", got[0].Process.ReadyTimeoutSeconds)
	}
}

func TestProcToolReportsState(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "svc-a", `{"id":"svc-a","name":"Svc A",
		"process":{"command":"python","args":["a.py"],
		"expose":{"tool":true,"endpoint":"http://127.0.0.1:1234"},"description":"helper A"}}`, nil)
	plugins := Load()
	if len(plugins) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(plugins))
	}
	m := NewProcManager(func() []Plugin { return plugins })
	ts := ProcTools(plugins, m)
	if len(ts) != 1 {
		t.Fatalf("want 1 exposed tool, got %d", len(ts))
	}
	if ts[0].Name() != "plugin_proc__svc_a" {
		t.Errorf("tool name = %q", ts[0].Name())
	}
	if ts[0].IsHidden() {
		t.Error("the control tool must be visible to the model")
	}
	res, err := ts[0].Execute(nil, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("output is not JSON: %q (%v)", res.Text, err)
	}
	if out["state"] != procStateStopped {
		t.Errorf("state = %v, want stopped", out["state"])
	}
	if out["endpoint"] != "http://127.0.0.1:1234" {
		t.Errorf("endpoint = %v", out["endpoint"])
	}
	if out["plugin"] != "svc-a" {
		t.Errorf("plugin = %v", out["plugin"])
	}
}

func TestProcToolsRespectsExpose(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "quiet", `{"id":"quiet","process":{"command":"python","args":["q.py"]}}`, nil)
	plugins := Load()
	m := NewProcManager(func() []Plugin { return plugins })
	if ts := ProcTools(plugins, m); len(ts) != 0 {
		t.Fatalf("a plugin without expose.tool must contribute no tool, got %d", len(ts))
	}
	states := m.States()
	if len(states) != 1 || states[0].State != procStateStopped {
		t.Fatalf("States() = %+v", states)
	}
	if states[0].Command != "python q.py" {
		t.Errorf("Command = %q", states[0].Command)
	}
}

func TestProcManagerStartUnknownPlugin(t *testing.T) {
	home := setHome(t)
	_ = home
	m := NewProcManager(func() []Plugin { return nil })
	if err := m.Start("nope"); err == nil {
		t.Fatal("Start of an unknown plugin must fail")
	}
	if err := m.Stop("nope"); err == nil {
		t.Fatal("Stop of an unknown plugin must fail")
	}
}

func TestProcRestartNever(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "once", `{"id":"once","process":{"command":"cmd","args":["/C","exit 1"],"restart":"never"}}`, nil)
	plugins := Load()
	if len(plugins) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(plugins))
	}
	m := NewProcManager(func() []Plugin { return plugins })
	if err := m.Start("once"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st := m.States()
		if len(st) == 1 && st[0].State == procStateFailed {
			if st[0].Restarts != 0 {
				t.Fatalf("restarts = %d with restart=never", st[0].Restarts)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("plugin never settled to failed: %+v", m.States())
}
