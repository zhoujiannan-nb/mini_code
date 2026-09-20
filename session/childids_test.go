package session

import (
	"path/filepath"
	"testing"
)

func TestChildIDsByParent(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rootA := NewSessionRecord("root A", "build", "", "")
	if err := store.Create(rootA); err != nil {
		t.Fatalf("create A: %v", err)
	}
	rootB := NewSessionRecord("root B", "build", "", "")
	if err := store.Create(rootB); err != nil {
		t.Fatalf("create B: %v", err)
	}
	subA1 := NewSessionRecord("sub A1", "explore", "", rootA.SessionID)
	if err := store.Create(subA1); err != nil {
		t.Fatalf("create subA1: %v", err)
	}
	subA2 := NewSessionRecord("sub A2", "explore", "", rootA.SessionID)
	if err := store.Create(subA2); err != nil {
		t.Fatalf("create subA2: %v", err)
	}
	subB := NewSessionRecord("sub B", "explore", "", rootB.SessionID)
	if err := store.Create(subB); err != nil {
		t.Fatalf("create subB: %v", err)
	}

	got, err := store.ChildIDsByParent()
	if err != nil {
		t.Fatalf("ChildIDsByParent: %v", err)
	}
	if len(got[rootA.SessionID]) != 2 {
		t.Errorf("root A children = %v, want 2 ids", got[rootA.SessionID])
	}
	if len(got[rootB.SessionID]) != 1 || got[rootB.SessionID][0] != subB.SessionID {
		t.Errorf("root B children = %v, want [subB]", got[rootB.SessionID])
	}
	if _, ok := got[subA1.SessionID]; ok {
		t.Errorf("child session %s wrongly present as a parent", subA1.SessionID)
	}
}
