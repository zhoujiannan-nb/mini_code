package tools

import (
	"context"
	"strings"
	"testing"
)

// fakeAgents is a stand-in dispatcher: it records what the tools asked for and
// replays canned answers, so the tests below pin the tool surface (validation,
// wording, name plumbing) without a session manager.
type fakeAgents struct {
	lastSpec  AgentSpec
	lastSend  string
	lastAgent string
	mode      string
	sendName  string
	sendErr   error
	run       AgentRun
	dispatchE error
	list      []JobInfo
	cancelled string
}

func (f *fakeAgents) Dispatch(_ context.Context, _ string, spec AgentSpec) (AgentRun, error) {
	f.lastSpec = spec
	if f.dispatchE != nil {
		return AgentRun{}, f.dispatchE
	}
	if f.run.Name == "" {
		return AgentRun{Name: "agent-1", ChildID: "child-1", Status: "running"}, nil
	}
	return f.run, nil
}

func (f *fakeAgents) Send(_ string, agent, message string) (string, string, error) {
	f.lastAgent, f.lastSend = agent, message
	if f.sendErr != nil {
		return "", "", f.sendErr
	}
	return f.mode, f.sendName, nil
}

func (f *fakeAgents) Resolve(_ string, agent string) (string, string, error) {
	if agent == "nope" {
		return "", "", context.DeadlineExceeded
	}
	return "child-1", agent, nil
}

func (f *fakeAgents) Output(_ context.Context, id string, _ bool, _ int) (string, error) {
	return "output of " + id, nil
}

func (f *fakeAgents) Status(string) (JobInfo, bool) { return JobInfo{}, false }

func (f *fakeAgents) Cancel(agent string) bool {
	f.cancelled = agent
	return true
}

func (f *fakeAgents) List(string) []JobInfo { return f.list }

// TestAgentToolValidation covers the required parameters and the role whitelist:
// a bad call must come back as one actionable sentence, not a panic or an
// empty result.
func TestAgentToolValidation(t *testing.T) {
	d := &fakeAgents{}
	tool := NewAgentTool()
	tool.SetSessionContext("parent", d)

	cases := []struct{ name string; params map[string]interface{}; want string }{
		{"no prompt", map[string]interface{}{"description": "x"}, "missing required parameter 'prompt'"},
		{"no description", map[string]interface{}{"prompt": "do it"}, "missing required parameter 'description'"},
		{"unknown role", map[string]interface{}{"prompt": "do it", "description": "x", "role": "no-such-role"}, "unknown role"},
	}
	for _, c := range cases {
		res, err := tool.Execute(context.Background(), c.params)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if !strings.Contains(res.Text, c.want) {
			t.Fatalf("%s: result = %q, want it to mention %q", c.name, res.Text, c.want)
		}
	}
	if d.lastSpec.Goal != "" {
		t.Fatal("a rejected call must not reach the dispatcher")
	}
}

// TestAgentToolForegroundAndBackground pins the two answer shapes: a foreground
// call returns the result, a background one returns the name plus the tools that
// reach it later.
func TestAgentToolForegroundAndBackground(t *testing.T) {
	d := &fakeAgents{run: AgentRun{Name: "reviewer", ChildID: "child-9", Status: "completed", Result: "all good"}}
	tool := NewAgentTool()
	tool.SetSessionContext("parent", d)

	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"prompt": "review the diff", "description": "代码审查",
	})
	if !strings.Contains(res.Text, `"reviewer"`) || !strings.Contains(res.Text, "all good") {
		t.Fatalf("foreground result = %q, want the name and the result", res.Text)
	}
	if d.lastSpec.Role != "work" {
		t.Fatalf("default role = %q, want work", d.lastSpec.Role)
	}
	if d.lastSpec.Description != "代码审查" {
		t.Fatalf("description not passed through: %q", d.lastSpec.Description)
	}

	d.run = AgentRun{Name: "reviewer", ChildID: "child-9", Status: "running"}
	res, _ = tool.Execute(context.Background(), map[string]interface{}{
		"prompt": "watch the build", "description": "build watch", "run_in_background": true,
		"model": "qwen-small",
	})
	if !strings.Contains(res.Text, `Sub-agent "reviewer" started`) {
		t.Fatalf("background result = %q, want a started notice", res.Text)
	}
	if !strings.Contains(res.Text, "send_message") || !strings.Contains(res.Text, "agent_output") {
		t.Fatalf("background result does not say how to reach the agent: %q", res.Text)
	}
	if !d.lastSpec.Background || d.lastSpec.Model != "qwen-small" {
		t.Fatalf("spec not passed through: %+v", d.lastSpec)
	}
}

// TestSendMessageWording: steering and waking are different events and the model
// must be able to tell them apart (one continues a run, the other starts one).
func TestSendMessageWording(t *testing.T) {
	d := &fakeAgents{mode: "steer", sendName: "reviewer"}
	tool := NewSendMessageTool("parent", d)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{"agent": "reviewer", "message": "check the tests"})
	if !strings.Contains(res.Text, "next turn") {
		t.Fatalf("steer result = %q, want it to say the message is delivered later", res.Text)
	}

	d.mode, d.sendName = "wake", "reviewer"
	res, _ = tool.Execute(context.Background(), map[string]interface{}{"agent": "reviewer", "message": "more work"})
	if !strings.Contains(res.Text, "Woke") {
		t.Fatalf("wake result = %q, want it to say the agent was woken", res.Text)
	}

	if res, _ := tool.Execute(context.Background(), map[string]interface{}{"agent": "reviewer"}); !strings.Contains(res.Text, "both 'agent' and 'message'") {
		t.Fatalf("missing message should be refused: %q", res.Text)
	}
}

// TestListAgentsRendersStatus: running and inactive are the two states; a failed
// last run is surfaced as such, and the conversation itself is always row one.
func TestListAgentsRendersStatus(t *testing.T) {
	d := &fakeAgents{list: []JobInfo{
		{ID: "c1", Name: "reviewer", Role: "work", Description: "code review", Goal: "review the diff", Status: "running"},
		{ID: "c2", Name: "builder", Role: "work", Description: "build watch", Goal: "watch the build", Status: "inactive"},
		{ID: "c3", Name: "tester", Role: "work", Status: "failed", Goal: "run the tests"},
	}}
	res, err := NewListAgentsTool("parent", d).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text
	if !strings.HasPrefix(text, "- lead") {
		t.Fatalf("the conversation must be the first row: %q", text)
	}
	for _, want := range []string{"reviewer  [running]", "builder  [inactive]", "tester  [inactive]", "last=failed", "code review"} {
		if !strings.Contains(text, want) {
			t.Fatalf("list_agents output missing %q:\n%s", want, text)
		}
	}
}

// TestUnknownAgentHintListsNames: a typo must come back with the names that do
// exist, so the fix is one retry rather than a list_agents round trip.
func TestUnknownAgentHintListsNames(t *testing.T) {
	d := &fakeAgents{
		sendErr: context.DeadlineExceeded,
		list:    []JobInfo{{Name: "reviewer"}, {Name: "builder"}},
	}
	res, _ := NewSendMessageTool("parent", d).Execute(context.Background(), map[string]interface{}{
		"agent": "reviwer", "message": "hi",
	})
	if !strings.Contains(res.Text, "Known agents: builder, reviewer") {
		t.Fatalf("unknown-agent error does not list the known names: %q", res.Text)
	}
}
