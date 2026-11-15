package session

import (
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/agent"
)

// TestBuildSystemPromptIsStatic verifies the system prompt no longer embeds
// a timestamp: it is the head of the request prefix the provider's prefix
// cache matches, so it must be identical across tasks of a session.
func TestBuildSystemPromptIsStatic(t *testing.T) {
	pb := &PromptBuilder{agentConfig: &agent.AgentConfig{Prompt: "you are a test agent"}}
	a := pb.BuildSystemPrompt("C:\\work", "")
	// More than one wall-clock second: a per-second timestamp in the prompt
	// would make the two renders differ.
	time.Sleep(1100 * time.Millisecond)
	b := pb.BuildSystemPrompt("C:\\work", "")
	if a != b {
		t.Fatal("system prompt must be stable across renders (a per-second timestamp would defeat the prefix cache)")
	}
}

// TestBuildSystemPromptIsTheJoinOfItsParts pins the invariant the prefix cache
// depends on: exposing the prompt as sections (BuildSystemPromptParts, for the
// 「上下文」drawer) must not change one byte of the prompt a turn sends.
//
// The expected text is spelled out here in the order the sections have always
// been emitted, independently of BuildSystemPromptParts — so reordering,
// re-spacing, or dropping a section fails this test instead of quietly costing
// every session a full prefix-cache miss.
func TestBuildSystemPromptIsTheJoinOfItsParts(t *testing.T) {
	// 老 BuildSystemPrompt 的拼接顺序,手抄一份当作真相。
	join := func(persona, workDir, extra string) string {
		var want []string
		if persona != "" {
			// modelName 未设置时 {{model}} 渲染成 "default"。
			want = append(want, strings.ReplaceAll(persona, "{{model}}", "default"))
		}
		want = append(want, workConstraints)
		if workDir != "" {
			want = append(want, "\n# Working Directory\n\nThe current working directory is: "+
				workDir+"\nAll file operations must be confined to this directory.\n")
		}
		if ins := agentsInstructionsSection(workDir); ins != "" {
			want = append(want, ins)
		}
		if ps := pluginsSection(); ps != "" {
			want = append(want, ps)
		}
		if mem := pluginMemorySection(); mem != "" {
			want = append(want, mem)
		}
		want = append(want, "\n"+environmentSection()+"\n")
		if extra != "" {
			want = append(want, "\n# Additional Context\n\n"+extra+"\n")
		}
		return strings.Join(want, "\n")
	}

	// 四种组合都要验:有人设/没人设、有工作目录/没有、有附加上下文/没有。
	cases := []struct{ persona, workDir, extra string }{
		{"you are a coding agent powered by the {{model}} model", `C:\work`, ""},
		{"", `C:\work`, ""},
		{"plain persona", "", ""},
		{"persona", `C:\work`, "额外注入的一段"},
	}
	for _, c := range cases {
		pb := &PromptBuilder{agentConfig: &agent.AgentConfig{Prompt: c.persona}}
		got := pb.BuildSystemPrompt(c.workDir, c.extra)
		if want := join(c.persona, c.workDir, c.extra); got != want {
			t.Fatalf("prompt bytes changed (workDir=%q extra=%q):\ngot len %d\nwant len %d",
				c.workDir, c.extra, len(got), len(want))
		}
		parts := pb.BuildSystemPromptParts(c.workDir, c.extra)
		if len(parts) == 0 {
			t.Fatalf("no prompt parts for workDir=%q", c.workDir)
		}
		texts := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.Label == "" {
				t.Fatalf("part with empty label: %+v", p)
			}
			texts = append(texts, p.Text)
		}
		if joined := strings.Join(texts, "\n"); joined != got {
			t.Fatalf("parts joined != BuildSystemPrompt (workDir=%q): %d vs %d bytes",
				c.workDir, len(joined), len(got))
		}
		// 人设/工作约束是基础段,工作目录起都是注入段。
		if c.persona != "" && (parts[0].Label != "人设" || parts[0].Injected) {
			t.Fatalf("first part = %+v, want 非注入的「人设」", parts[0])
		}
		injected := 0
		for _, p := range parts {
			if p.Injected {
				injected++
			}
		}
		if c.workDir != "" && injected == 0 {
			t.Fatalf("workDir=%q 应当至少有一段 injected=true(工作目录)", c.workDir)
		}
		if c.extra != "" {
			last := parts[len(parts)-1]
			if last.Label != "附加上下文" || !last.Injected || !strings.Contains(last.Text, c.extra) {
				t.Fatalf("last part = %+v, want 注入的「附加上下文」且含额外内容", last)
			}
		}
	}
}
