package session

import (
	"strings"
	"testing"
	"time"

	"github.com/user/mini_code/agent"
)

func TestBuildSystemPromptIsStatic(t *testing.T) {
	pb := &PromptBuilder{agentConfig: &agent.AgentConfig{Prompt: "you are a test agent"}}
	a := pb.BuildSystemPrompt("C:\\work", "")
	time.Sleep(1100 * time.Millisecond)
	b := pb.BuildSystemPrompt("C:\\work", "")
	if a != b {
		t.Fatal("system prompt must be stable across renders (a per-second timestamp would defeat the prefix cache)")
	}
}

func TestBuildSystemPromptIsTheJoinOfItsParts(t *testing.T) {
	join := func(persona, workDir, extra string) string {
		var want []string
		if persona != "" {
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
		want = append(want, "\n"+environmentSection()+"\n")
		if extra != "" {
			want = append(want, "\n# Additional Context\n\n"+extra+"\n")
		}
		return strings.Join(want, "\n")
	}

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
