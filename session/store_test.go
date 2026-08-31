package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/config"
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

// TestSetTitle verifies that a rename is persisted and visible through both
// the full-row read and the lightweight list query, and that it leaves
// updated_at (and therefore the list order) untouched.
func TestSetTitle(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rec := NewSessionRecord("old name", "build", "", "")
	if err := store.Create(rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	updatedBefore := rec.UpdatedAt
	if err := store.SetTitle(rec.SessionID, "new name"); err != nil {
		t.Fatalf("set title: %v", err)
	}

	got, err := store.Get(rec.SessionID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "new name" {
		t.Errorf("title = %q, want %q", got.Title, "new name")
	}
	if got.UpdatedAt != updatedBefore {
		t.Errorf("updated_at changed on rename: %q -> %q", updatedBefore, got.UpdatedAt)
	}
	sums, err := store.ListSessionSummaries(10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sums) != 1 || sums[0].Title != "new name" {
		t.Errorf("list title = %+v, want new name", sums)
	}
}

// TestRenameSession verifies manager-level rename: validation (blank,
// over-long, unknown session), trimming, and in-memory sync so the cached
// record (and any later whole-row persistence) carries the new title.
func TestRenameSession(t *testing.T) {
	mgr := newTestManager(t)
	sess, err := mgr.CreateSessionRecord("old", "build", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := sess.ID()

	if err := mgr.RenameSession(id, "   "); err == nil {
		t.Fatal("expected error for blank title")
	}
	if err := mgr.RenameSession(id, strings.Repeat("长", MaxTitleLen+1)); err == nil {
		t.Fatal("expected error for over-long title")
	}
	if err := mgr.RenameSession("nope", "x"); err == nil {
		t.Fatal("expected error for unknown session")
	}
	if err := mgr.RenameSession(id, "  new title  "); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := sess.Title(); got != "new title" {
		t.Errorf("in-memory title = %q, want %q (trimmed)", got, "new title")
	}
	rec, err := mgr.GetStore().Get(id)
	if err != nil || rec == nil {
		t.Fatalf("get from store: %v", err)
	}
	if rec.Title != "new title" {
		t.Errorf("persisted title = %q, want %q", rec.Title, "new title")
	}
}

// TestSetModelKey verifies that the per-session model key is persisted and
// visible through both the full-row read and the lightweight list query, and
// that switching back to the default model clears it.
func TestSetModelKey(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	rec := NewSessionRecord("model-test", "build", "", "")
	if err := store.Create(rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetModelKey(rec.SessionID, "gpt-4o"); err != nil {
		t.Fatalf("set model key: %v", err)
	}

	got, err := store.Get(rec.SessionID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.ModelKey != "gpt-4o" {
		t.Errorf("model_key = %q, want %q", got.ModelKey, "gpt-4o")
	}
	sums, err := store.ListSessionSummaries(10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sums) != 1 || sums[0].ModelKey != "gpt-4o" {
		t.Errorf("list model_key = %+v, want gpt-4o", sums)
	}

	// Switching back to the default model clears the key.
	if err := store.SetModelKey(rec.SessionID, ""); err != nil {
		t.Fatalf("clear model key: %v", err)
	}
	got, err = store.Get(rec.SessionID)
	if err != nil || got == nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.ModelKey != "" {
		t.Errorf("model_key after clear = %q, want empty", got.ModelKey)
	}
}

// TestSetModel verifies manager-level model switching: unknown model names
// are rejected, a valid switch is persisted and synced to the in-memory
// record, and an empty name resets to the default model.
func TestSetModel(t *testing.T) {
	mgr := newTestManager(t)
	// Install a two-model registry (stateless clients; no HTTP is made).
	mk := func(name string) *provider.ModelClient {
		c, err := provider.NewModelClient(config.ModelConfig{
			Provider: "vllm", BaseURL: "http://127.0.0.1:1", APIKey: "k",
			ModelName: name, MaxTokens: 100, ContextWindow: 4096,
		})
		if err != nil {
			t.Fatalf("NewModelClient(%s): %v", name, err)
		}
		return c
	}
	mgr.SetModels(map[string]*provider.ModelClient{"a": mk("a"), "b": mk("b")}, "a")

	sess, err := mgr.CreateSessionRecord("model-test", "build", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := sess.ID()

	if err := mgr.SetModel(id, "nope"); err == nil {
		t.Fatal("expected error for unknown model")
	}
	if err := mgr.SetModel("nope", "a"); err == nil {
		t.Fatal("expected error for unknown session")
	}
	if err := mgr.SetModel(id, "b"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	if got := sess.Record().ModelKey; got != "b" {
		t.Errorf("in-memory model_key = %q, want %q", got, "b")
	}
	rec, err := mgr.GetStore().Get(id)
	if err != nil || rec == nil {
		t.Fatalf("get from store: %v", err)
	}
	if rec.ModelKey != "b" {
		t.Errorf("persisted model_key = %q, want %q", rec.ModelKey, "b")
	}
	if got := mgr.ContextWindowFor("b"); got != 4096 {
		t.Errorf("ContextWindowFor(b) = %d, want 4096", got)
	}

	// Empty name resets to the default model.
	if err := mgr.SetModel(id, ""); err != nil {
		t.Fatalf("reset model: %v", err)
	}
	rec, err = mgr.GetStore().Get(id)
	if err != nil || rec == nil {
		t.Fatalf("get after reset: %v", err)
	}
	if rec.ModelKey != "" {
		t.Errorf("model_key after reset = %q, want empty", rec.ModelKey)
	}
}
