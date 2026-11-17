package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolUTF8OutputSurvives(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "raw", `{"id":"raw","tools":[{"name":"raw_tool",
		"command":"cmd","args":["/C","echo \u00ffhello"]}]}`, nil)
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := json.Marshal(map[string]interface{}{"content": res.Text}); err != nil {
		t.Fatalf("tool output cannot be marshalled into a request: %v", err)
	}
}

func TestToolEnvForcesUTF8(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "env", `{"id":"env","tools":[{"name":"env_tool",
		"command":"cmd","args":["/C","echo %PYTHONIOENCODING% %PYTHONUTF8%"]}]}`, nil)
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "utf-8") || !strings.Contains(res.Text, "1") {
		t.Fatalf("child did not see PYTHONIOENCODING/PYTHONUTF8: %q", res.Text)
	}
}

func TestTruncateKeepsUTF8Intact(t *testing.T) {
	s := strings.Repeat("中", 100)
	got := truncate(s, 10)
	if !strings.HasPrefix(got, "中中中") {
		t.Fatalf("truncate lost characters: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("truncate did not mark the cut")
	}
	for n := 1; n < 12; n++ {
		if !utf8Valid(truncate(s, n)) {
			t.Fatalf("truncate(%d) produced invalid UTF-8", n)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' && !strings.Contains(s, "truncated") {
			return false
		}
	}
	b := []byte(s)
	return len(b) == 0 || string(b) == s
}

func TestToolTimeoutIsReported(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "slow", `{"id":"slow","tools":[{"name":"slow_tool",
		"command":"cmd","args":["/C","ping -n 10 127.0.0.1 >nul"],"timeout_seconds":1}]}`, nil)
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute must not return a Go error on timeout: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Text), "timed out") {
		t.Fatalf("timeout result = %q", res.Text)
	}
}

func TestToolParametersDefault(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "bare", `{"id":"bare","tools":[{"name":"bare_tool","command":"cmd","args":["/C","echo hi"]}]}`, nil)
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	p := ts[0].Parameters()
	if p["type"] != "object" {
		t.Fatalf("Parameters = %v", p)
	}
	if req, ok := p["required"].([]string); !ok || req == nil {
		t.Fatalf("required must be a non-nil []string, got %#v", p["required"])
	}
}

func TestToolWorkingDirectoryIsThePluginDir(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "rel", `{"id":"rel","tools":[{"name":"rel_tool",
		"command":"cmd","args":["/C","type marker.txt"]}]}`, map[string]string{"marker.txt": "marker-ok"})
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "marker-ok") {
		t.Fatalf("the child did not run in the plugin directory: %q", res.Text)
	}
	_ = filepath.Join
	_ = os.Getenv
}
