package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHome points the process at a throwaway home directory and returns it.
func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// config.GetConfigDir uses os.UserHomeDir, which reads USERPROFILE on
	// Windows and HOME elsewhere.
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".mini_code", "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// writePlugin creates <home>/.mini_code/plugins/<dir>/plugin.json.
func writePlugin(t *testing.T, home, dir, manifest string, files map[string]string) {
	t.Helper()
	base := filepath.Join(home, ".mini_code", "plugins", dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", base, err)
	}
	if err := os.WriteFile(filepath.Join(base, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for name, body := range files {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestLoadMissingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	if got := Load(); len(got) != 0 {
		t.Fatalf("Load() = %d plugins, want 0", len(got))
	}
}

func TestLoadSortedAndFiltered(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "zeta", `{"id":"zeta","tools":[{"name":"z_tool","command":"x"}]}`, nil)
	writePlugin(t, home, "alpha", `{"id":"alpha"}`, nil)
	writePlugin(t, home, "off", `{"id":"off","enabled":false}`, nil)
	writePlugin(t, home, "broken", `{not json`, nil)

	got := Load()
	if len(got) != 2 {
		t.Fatalf("Load() = %d plugins, want 2 (disabled and broken skipped): %+v", len(got), got)
	}
	if got[0].ID != "alpha" || got[1].ID != "zeta" {
		t.Fatalf("plugins not sorted by id: %q, %q", got[0].ID, got[1].ID)
	}
	if got[1].Name != "zeta" { // name defaults to id
		t.Errorf("Name = %q, want the id as fallback", got[1].Name)
	}
}

func TestLoadRejectsBadToolName(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "bad", `{"id":"bad","tools":[{"name":"Has-Dash","command":"x"}]}`, nil)
	if got := Load(); len(got) != 0 {
		t.Fatalf("a tool name with a dash must be rejected, got %+v", got)
	}
}

func TestLoadRejectsEscapingPath(t *testing.T) {
	home := setHome(t)
	for _, rel := range []string{"../outside.css", "/etc/passwd", `C:\Windows\win.ini`} {
		writePlugin(t, home, "esc", `{"id":"esc","styles":["`+strings.ReplaceAll(rel, `\`, `\\`)+`"]}`, nil)
		if got := Load(); len(got) != 0 {
			t.Fatalf("path %q must be rejected, got %+v", rel, got)
		}
	}
}

func TestPluginToolsAppendedAfterCore(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "p1", `{"id":"p1","tools":[{"name":"aaa_plugin","command":"x"}]}`, nil)
	plugins := Load()
	if len(plugins) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(plugins))
	}
	tools := PluginTools(plugins)
	if len(tools) != 1 || tools[0].Name() != "aaa_plugin" {
		t.Fatalf("PluginTools = %v", tools)
	}
	if got := tools[0].Parameters(); got["type"] != "object" {
		t.Errorf("Parameters() = %v, want an object schema", got)
	}
	if tools[0].IsHidden() {
		t.Error("plugin tools must be visible to the model")
	}
}

// TestToolExecuteFeedsParamsOnStdin runs a real child process: the plugin
// command is a tiny shell snippet that copies stdin to stdout, which proves the
// parameter object reaches the program unchanged.
func TestToolExecuteFeedsParamsOnStdin(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "echo", `{"id":"echo","tools":[{"name":"echo_tool",
		"description":"echo params",
		"command":"cmd","args":["/C","more"],"timeout_seconds":30}]}`, nil)
	plugins := Load()
	if len(plugins) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(plugins))
	}
	ts := PluginTools(plugins)
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{"query": "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Text)), &back); err != nil {
		t.Fatalf("tool output is not the JSON we sent: %q (%v)", res.Text, err)
	}
	if back["query"] != "hello" {
		t.Fatalf("round-tripped params = %v, want query=hello", back)
	}
}

// TestToolExecuteReportsFailure checks that a non-zero exit becomes a tool
// result the model can read, not a Go error (the loop treats the latter as a
// crash).
func TestToolExecuteReportsFailure(t *testing.T) {
	home := setHome(t)
	writePlugin(t, home, "fail", `{"id":"fail","tools":[{"name":"fail_tool",
		"command":"cmd","args":["/C","exit 3"]}]}`, nil)
	ts := PluginTools(Load())
	if len(ts) != 1 {
		t.Fatalf("want 1 tool, got %d", len(ts))
	}
	res, err := ts[0].Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute must not return a Go error for a failed command: %v", err)
	}
	if !strings.Contains(res.Text, "Error") {
		t.Fatalf("failed command result = %q, want an Error text", res.Text)
	}
}

func TestKinds(t *testing.T) {
	p := Plugin{Manifest: Manifest{
		Tools:     []ToolSpec{{Name: "t", Command: "x"}},
		Styles:    []string{"a.css"},
		Wallpaper: "w.webp",
	}}
	got := strings.Join(p.Kinds(), ",")
	if got != "tools,skin,wallpaper" {
		t.Fatalf("Kinds() = %q", got)
	}
}
