package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/agent"
)

func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

func writePluginsMD(t *testing.T, content string) {
	t.Helper()
	home := withTempConfigDir(t)
	dir := filepath.Join(home, ".mini_code")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParsePluginsLine(t *testing.T) {
	tests := []struct {
		line string
		path string
		desc string
		ok   bool
	}{
		{line: "D:\\project_ai\\web_search\\web_search", path: "D:\\project_ai\\web_search\\web_search", ok: true},
		{line: "D:\\project_ai\\web_search  desc: 网络搜索工具", path: "D:\\project_ai\\web_search", desc: "网络搜索工具", ok: true},
		{line: "D:\\project_ai\\web_search description：网络搜索工具", path: "D:\\project_ai\\web_search", desc: "网络搜索工具", ok: true},
		{line: "C:\\tools\\pdf desc:", path: "C:\\tools\\pdf", ok: true},
		{line: "   ", ok: false},
		{line: "desc: no path", ok: false},
	}
	for _, tt := range tests {
		p, ok := parsePluginsLine(tt.line)
		if ok != tt.ok {
			t.Errorf("parsePluginsLine(%q): ok = %v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if p.Path != tt.path || p.Desc != tt.desc {
			t.Errorf("parsePluginsLine(%q) = {%q %q}, want {%q %q}", tt.line, p.Path, p.Desc, tt.path, tt.desc)
		}
	}
}

func TestPluginsSectionMissingFile(t *testing.T) {
	withTempConfigDir(t)
	if s := pluginsSection(); s != "" {
		t.Fatalf("pluginsSection with no plugins.md = %q, want empty", s)
	}
}

func TestPluginsSectionParses(t *testing.T) {
	writePluginsMD(t, strings.Join([]string{
		"# 每行一个插件",
		"",
		"D:\\project_ai\\web_search  desc: 网络搜索工具",
		"D:\\project_ai\\pdf_tool description：PDF 处理",
		"C:\\tools\\bare",
		"# 注释行",
		"",
	}, "\n"))

	s := pluginsSection()
	for _, want := range []string{
		"[plugins]",
		"- D:\\project_ai\\web_search — 网络搜索工具",
		"- D:\\project_ai\\pdf_tool — PDF 处理",
		"- C:\\tools\\bare",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pluginsSection missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "注释") || strings.Count(s, "\n") > 5 {
		t.Errorf("pluginsSection leaked comment/blank lines:\n%s", s)
	}
}

func TestPluginsSectionInSystemPrompt(t *testing.T) {
	pb := &PromptBuilder{agentConfig: &agent.AgentConfig{Prompt: "you are a test agent"}}

	withTempConfigDir(t)
	if got := pb.BuildSystemPrompt("C:\\work", ""); strings.Contains(got, "[plugins]") {
		t.Fatal("system prompt must not contain a plugin section when no plugins.md exists")
	}

	writePluginsMD(t, "D:\\project_ai\\web_search  desc: 网络搜索工具\n")
	got := pb.BuildSystemPrompt("C:\\work", "")
	if !strings.Contains(got, "[plugins]") || !strings.Contains(got, "D:\\project_ai\\web_search") {
		t.Fatal("system prompt must contain the [plugins] section when plugins.md exists")
	}
}
