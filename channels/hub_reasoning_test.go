package channels

import (
	"context"
	"testing"
	"time"
)

// TestHubNewSessionReasoningOverride verifies that a new session created from a
// message carrying thinking / reasoning_effort snapshots the per-session
// override (the composer path for a brand-new conversation).
func TestHubNewSessionReasoningOverride(t *testing.T) {
	env := newTestEnv(t, stepTwoTurns)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msg := NewUserMessage("test", "hello")
	msg.Thinking = "enabled"
	msg.ReasoningEffort = "low"
	_, sessionID, err := env.hub.RunOnce(ctx, msg)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	sess, err := env.mgr.Get(sessionID)
	if err != nil || sess == nil {
		t.Fatalf("get session: %v", err)
	}
	thinking, effort := sess.ReasoningOverride()
	if thinking != "enabled" || effort != "low" {
		t.Fatalf("override = (%q,%q), want (enabled,low)", thinking, effort)
	}
}
