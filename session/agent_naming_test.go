package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// gateOf turns a blocking channel into a PromptFunc: it waits until release is
// closed, then returns result. Used to hold an agent in "running".
func blockingPrompt(started chan<- string, release <-chan struct{}, result string) func(context.Context, *Session, string) (string, string, error) {
	return func(ctx context.Context, s *Session, goal string) (string, string, error) {
		if started != nil {
			select {
			case started <- goal:
			default:
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		return result, "", nil
	}
}

// TestAgentNameValidation pins the naming rule copied from DSH: lower-case
// letters/digits/dashes, 1-32 chars, starting with a letter, "lead" reserved.
func TestAgentNameValidation(t *testing.T) {
	valid := []string{"a", "reviewer", "agent-1", "qwen38-flash", "x1234567890123456789012345678901"} // 32 chars
	for _, n := range valid {
		if err := validAgentName(n); err != nil {
			t.Fatalf("%q should be a valid name: %v", n, err)
		}
	}
	invalid := []string{"", "Lead", "LEAD", "lead", "-a", "1a", "a b", "a/b", "a_b", "a.b", strings.Repeat("a", 33)}
	for _, n := range invalid {
		if err := validAgentName(n); err == nil {
			t.Fatalf("%q should be rejected", n)
		}
	}
	// "lead" specifically must say why.
	if err := validAgentName("lead"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved name error = %v, want a 'reserved' explanation", err)
	}
}

// TestAgentNamesAreSequentialAndUnique: omitting the name yields agent-1,
// agent-2, ...; a clash is an error, never a silent rename.
func TestAgentNamesAreSequentialAndUnique(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(context.Context, *Session, string) (string, string, error) {
		return "done", "", nil
	}

	first, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Goal: "one", Description: "first"})
	if err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	second, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Goal: "two", Description: "second"})
	if err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if first.Name != "agent-1" || second.Name != "agent-2" {
		t.Fatalf("default names = %q, %q; want agent-1, agent-2", first.Name, second.Name)
	}

	// Explicit name works once, then clashes.
	third, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "reviewer", Goal: "three", Description: "review"})
	if err != nil {
		t.Fatalf("named dispatch: %v", err)
	}
	if third.Name != "reviewer" {
		t.Fatalf("name = %q, want reviewer", third.Name)
	}
	if _, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "reviewer", Goal: "dup", Description: "dup"}); err == nil {
		t.Fatal("duplicate name must be refused")
	}
	if _, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "lead", Goal: "x", Description: "x"}); err == nil {
		t.Fatal("the reserved name must be refused")
	}
}

// TestAgentNamesSurviveRestart: names live in the child session rows, so a new
// service instance (a new process) still lists them and can address them.
func TestAgentNamesSurviveRestart(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(context.Context, *Session, string) (string, string, error) {
		return "first run", "", nil
	}
	run, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "reviewer", Goal: "review the diff", Description: "code review"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// A fresh service == a new process: no jobs in memory.
	fresh := NewSubagentService(mgr)
	rows := fresh.List(parent.ID())
	if len(rows) != 1 {
		t.Fatalf("after restart List() = %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Name != "reviewer" || rows[0].Description != "code review" {
		t.Fatalf("identity lost across restart: %+v", rows[0])
	}
	if rows[0].Status != "inactive" {
		// The stub PromptFunc never runs Session.Prompt, so the row keeps its
		// creation status; either way a row with no live job must not read as
		// running. "inactive" is the honest answer after a restart.
		t.Fatalf("status after restart = %q, want inactive", rows[0].Status)
	}

	// And it is still addressable by name.
	id, name, err := fresh.Resolve(parent.ID(), "reviewer")
	if err != nil || id != run.ChildID || name != "reviewer" {
		t.Fatalf("Resolve after restart = (%q,%q,%v), want (%q,reviewer,nil)", id, name, err, run.ChildID)
	}
	// A second instance must not hand out the same default name.
	fresh.PromptFunc = func(context.Context, *Session, string) (string, string, error) { return "again", "", nil }
	next, err := fresh.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Goal: "next", Description: "next"})
	if err != nil {
		t.Fatalf("dispatch after restart: %v", err)
	}
	if next.Name != "agent-1" {
		t.Fatalf("default name after restart = %q, want agent-1 (no collision with reviewer)", next.Name)
	}
}

// TestSendSteersRunningAgent: while an agent runs, a message is queued for its
// next turn instead of starting a second loop on the same conversation.
func TestSendSteersRunningAgent(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	release := make(chan struct{})
	svc.PromptFunc = blockingPrompt(nil, release, "finished")

	run, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{
		Name: "reviewer", Goal: "long task", Description: "review", Background: true,
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	mode, name, err := svc.Send(parent.ID(), "reviewer", "also check the tests")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if mode != "steer" || name != "reviewer" {
		t.Fatalf("Send mode = %q name = %q, want steer/reviewer", mode, name)
	}
	child, err := mgr.Get(run.ChildID)
	if err != nil || child == nil {
		t.Fatalf("child session: %v", err)
	}
	queued := child.DrainUserMessages()
	if len(queued) != 1 || !strings.Contains(queued[0], "also check the tests") {
		t.Fatalf("message was not queued on the running agent: %q", queued)
	}
	// The relay text must attribute the message and deny it is an approval.
	if !strings.Contains(queued[0], `Agent "lead" sent you a message`) {
		t.Fatalf("relay text does not name the sender: %q", queued[0])
	}
	if !strings.Contains(queued[0], "not an approval") {
		t.Fatalf("relay text does not deny approval: %q", queued[0])
	}
	close(release)
}

// TestSendWakesIdleAgent: after an agent finishes, a message starts a new run on
// the same conversation and the parent gets a settlement notice for it.
func TestSendWakesIdleAgent(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	var mu sync.Mutex
	var goals []string
	svc.PromptFunc = func(_ context.Context, _ *Session, goal string) (string, string, error) {
		mu.Lock()
		goals = append(goals, goal)
		n := len(goals)
		mu.Unlock()
		if n == 1 {
			return "first result", "", nil
		}
		return "second result", "", nil
	}
	run, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "reviewer", Goal: "first task", Description: "review"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	mode, name, err := svc.Send(parent.ID(), "reviewer", "second task")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if mode != "wake" || name != "reviewer" {
		t.Fatalf("Send mode = %q name = %q, want wake/reviewer", mode, name)
	}
	// The wake runs asynchronously; wait for its notice.
	deadline := time.Now().Add(3 * time.Second)
	for {
		hist := parent.Record().Messages
		found := false
		for _, m := range hist {
			if m.Role == "user" && strings.Contains(m.GetText(), "<agent ") && strings.Contains(m.GetText(), "second result") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no settlement notice for the woken agent; history: %+v", hist)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(goals) != 2 || !strings.Contains(goals[1], "second task") {
		t.Fatalf("woken agent did not receive the message: %q", goals)
	}
	// Same conversation, so the second run kept the first run's history.
	if id, _, _ := svc.Resolve(parent.ID(), "reviewer"); id != run.ChildID {
		t.Fatalf("wake created a new session %q, want the existing %q", id, run.ChildID)
	}
}

// TestAgentOutputReadsStoredConversation: with no live job (pruned, or another
// process), agent_output still answers from the child's own conversation.
func TestAgentOutputReadsStoredConversation(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	svc.PromptFunc = func(_ context.Context, s *Session, _ string) (string, string, error) {
		// What a real Prompt does at the end of a turn: commit the reply to the
		// session's stored conversation.
		s.mu.Lock()
		s.record.Messages = append(s.record.Messages, provider.Message{Role: "assistant", Content: "the answer is 42"})
		err := s.store.Update(s.record)
		s.mu.Unlock()
		if err != nil {
			return "", "", err
		}
		return "the answer is 42", "", nil
	}
	run, err := svc.Dispatch(context.Background(), parent.ID(), tools.AgentSpec{Name: "reviewer", Goal: "answer", Description: "review"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	fresh := NewSubagentService(mgr) // no jobs in memory
	out, err := fresh.Output(context.Background(), run.ChildID, false, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !strings.Contains(out, "the answer is 42") {
		t.Fatalf("Output = %q, want the stored assistant reply", out)
	}
}

// TestResolveRejectsUnknownAndRoot covers the error paths the tools rely on.
func TestResolveRejectsUnknownAndRoot(t *testing.T) {
	mgr := newTestManager(t)
	parent := mustSession(t, mgr, "parent")
	svc := NewSubagentService(mgr)
	if _, _, err := svc.Resolve(parent.ID(), "lead"); err == nil {
		t.Fatal("resolving 'lead' must fail: it is this conversation")
	}
	if _, _, err := svc.Resolve(parent.ID(), "nobody"); err == nil {
		t.Fatal("resolving an unknown name must fail")
	}
}
