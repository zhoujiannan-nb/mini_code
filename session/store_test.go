package session

import (
	"path/filepath"
	"testing"

	"github.com/user/mini_code/provider"
)

// TestListSessionSummaries verifies the lightweight list query: root
// sessions only (sub-agent sessions with parent_id set stay out), message
// counts taken from the stored payloads, and no payload in the rows.
func TestListSessionSummaries(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rootA := NewSessionRecord("root A", "build", "", "")
	rootA.Messages = []provider.Message{{Role: "user", Content: "a1"}, {Role: "assistant", Content: "a2"}, {Role: "user", Content: "a3"}}
	if err := store.Create(rootA); err != nil {
		t.Fatalf("create A: %v", err)
	}
	rootB := NewSessionRecord("root B", "build", "", "")
	if err := store.Create(rootB); err != nil {
		t.Fatalf("create B: %v", err)
	}
	sub := NewSessionRecord("sub of A", "explore", "", rootA.SessionID)
	sub.Messages = []provider.Message{{Role: "user", Content: "s1"}, {Role: "assistant", Content: "s2"}}
	if err := store.Create(sub); err != nil {
		t.Fatalf("create sub: %v", err)
	}

	summaries, err := store.ListSessionSummaries(100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 root sessions, got %d: %+v", len(summaries), summaries)
	}
	counts := map[string]int{}
	for _, s := range summaries {
		if s.SessionID == sub.SessionID {
			t.Fatalf("sub-agent session leaked into the root list: %+v", s)
		}
		counts[s.SessionID] = s.MessageCount
	}
	if counts[rootA.SessionID] != 3 {
		t.Errorf("root A message_count = %d, want 3", counts[rootA.SessionID])
	}
	if counts[rootB.SessionID] != 0 {
		t.Errorf("root B message_count = %d, want 0", counts[rootB.SessionID])
	}

	// A deleted session (and its children) disappears from the list.
	if err := store.Delete(rootA.SessionID); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	summaries, err = store.ListSessionSummaries(100, 0)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(summaries) != 1 || summaries[0].SessionID != rootB.SessionID {
		t.Fatalf("expected only root B after delete, got %+v", summaries)
	}
}
