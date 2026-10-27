package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
)

// TestPluginToolReachesTheModel is the bug this test was written for: the
// compiled role permission table only lists core tools, so a naive filter
// dropped every plugin tool from the schema list and the model answered "I don't
// have web_search".
func TestPluginToolReachesTheModel(t *testing.T) {
	home := pluginHome(t)
	cfg := agent.BuildAgentConfig() // the real, compiled role table

	before := NewPromptBuilder(cfg, "C:\\work")
	core := names(before.GetFilteredDefinitions(cfg))
	if contains(core, "web_search") {
		t.Fatal("web_search must not exist before the plugin is installed")
	}

	writeTestPlugin(t, home, "web", `{"id":"web","tools":[
		{"name":"web_search","description":"search","command":"x"}]}`, nil)

	after := NewPromptBuilder(cfg, "C:\\work")
	got := names(after.GetFilteredDefinitions(cfg))

	if !contains(got, "web_search") {
		t.Fatalf("web_search was filtered out of the schema list; got %v", got)
	}
	// The core prefix is unchanged, and the plugin tool is last.
	if len(got) != len(core)+1 {
		t.Fatalf("got %d definitions, want core(%d)+1: %v", len(got), len(core), got)
	}
	for i := range core {
		if got[i] != core[i] {
			t.Fatalf("core tool %d moved: %q -> %q", i, core[i], got[i])
		}
	}
	if got[len(got)-1] != "web_search" {
		t.Fatalf("plugin tool is not last: %v", got)
	}
}

// TestExplicitDenyStillBlocksPluginTool: a role that names the plugin tool with
// "deny" must win over the "absent means allow" rule.
func TestExplicitDenyStillBlocksPluginTool(t *testing.T) {
	home := pluginHome(t)
	writeTestPlugin(t, home, "web", `{"id":"web","tools":[
		{"name":"web_search","description":"search","command":"x"}]}`, nil)

	cfg := agent.BuildAgentConfig()
	cfg.Permissions.Tools["web_search"] = agent.NewDeny()
	pb := NewPromptBuilder(cfg, "C:\\work")
	if contains(names(pb.GetFilteredDefinitions(cfg)), "web_search") {
		t.Fatal("an explicit deny must still block a plugin tool")
	}
}

// TestFilteredDefinitionsStillDropsUnpermittedCoreTool keeps the pre-existing
// guarantee for core tools: the role filter is not loosened for them.
func TestFilteredDefinitionsStillDropsUnpermittedCoreTool(t *testing.T) {
	pluginHome(t)
	cfg := agent.BuildAgentConfig()
	delete(cfg.Permissions.Tools, "exec")
	pb := NewPromptBuilder(cfg, "C:\\work")
	if contains(names(pb.GetFilteredDefinitions(cfg)), "exec") {
		t.Fatal("exec must stay filtered out once the role drops it")
	}
}

// TestGateDeniesUnpermittedCoreTool: the runtime gate still denies a core tool
// the role does not allow.
func TestGateDeniesUnpermittedCoreTool(t *testing.T) {
	pluginHome(t)
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("gate", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	cfg := agent.BuildAgentConfig()
	delete(cfg.Permissions.Tools, "exec")
	gate := s.gateFor(cfg)
	allow, msg := gate(context.Background(), "exec", map[string]interface{}{"command": "echo hi"})
	if allow {
		t.Fatal("exec must be denied once the role drops it")
	}
	if !strings.Contains(msg, "not permitted") {
		t.Fatalf("deny message = %q", msg)
	}
}

// TestGateAllowsPluginToolInFullMode: in full mode a plugin tool runs (the mode
// is the only thing that can ask for approval, and full mode asks nothing).
func TestGateAllowsPluginToolInFullMode(t *testing.T) {
	home := pluginHome(t)
	writeTestPlugin(t, home, "web", `{"id":"web","tools":[
		{"name":"web_search","description":"search","command":"x"}]}`, nil)
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("gate", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	gate := s.gateFor(agent.BuildAgentConfig())
	allow, msg := gate(context.Background(), "web_search", map[string]interface{}{"query": "x"})
	if !allow || msg != "" {
		t.Fatalf("plugin tool denied in full mode: allow=%v msg=%q", allow, msg)
	}
}

// TestGateAsksForPluginToolInCautiousMode: a plugin runs an external program the
// host knows nothing about, so cautious mode must ask the user first — the
// plugin layer only ever makes the gate stricter.
func TestGateAsksForPluginToolInCautiousMode(t *testing.T) {
	home := pluginHome(t)
	writeTestPlugin(t, home, "web", `{"id":"web","tools":[
		{"name":"web_search","description":"search","command":"x"}]}`, nil)
	mgr := newTestManager(t)
	s, err := mgr.CreateSessionRecord("gate", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("CreateSessionRecord: %v", err)
	}
	if err := mgr.SetMode(s.ID(), ModeCautious); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	ap := &recordingApprover{approved: true, reason: "user approved"}
	mgr.SetApprover(ap)

	gate := s.gateFor(agent.BuildAgentConfig())
	allow, msg := gate(context.Background(), "web_search", map[string]interface{}{"query": "x"})
	if ap.called != 1 {
		t.Fatalf("cautious mode did not ask the user (called=%d)", ap.called)
	}
	if !allow || msg != "" {
		t.Fatalf("an approved plugin call must run: allow=%v msg=%q", allow, msg)
	}
	if ap.last.ToolName != "web_search" {
		t.Fatalf("approval carried tool %q", ap.last.ToolName)
	}

	// Denied once the approver says no.
	ap.approved = false
	ap.reason = "user denied"
	allow, msg = gate(context.Background(), "web_search", map[string]interface{}{"query": "x"})
	if allow {
		t.Fatal("a denied plugin call still ran")
	}
	if !strings.Contains(msg, "denied") {
		t.Fatalf("deny message = %q", msg)
	}
}

// TestIsPluginTool verifies the helper the gate uses.
func TestIsPluginTool(t *testing.T) {
	home := pluginHome(t)
	writeTestPlugin(t, home, "web", `{"id":"web","tools":[
		{"name":"web_search","description":"search","command":"x"}]}`, nil)
	if !isPluginTool("web_search") {
		t.Fatal("isPluginTool(web_search) = false")
	}
	if isPluginTool("exec") {
		t.Fatal("isPluginTool(exec) = true; a core tool must never be treated as a plugin")
	}
	// A disabled plugin's tool is not installed.
	dir := filepath.Join(home, ".mini_code", "plugins", "off")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"),
		[]byte(`{"id":"off","enabled":false,"tools":[{"name":"off_tool","command":"x"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if isPluginTool("off_tool") {
		t.Fatal("a disabled plugin's tool must not count as installed")
	}
}

func names(defs []provider.ToolSchema) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
