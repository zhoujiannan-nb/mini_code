package session

import (
	"path/filepath"
	"testing"
)

// addUsageTotal accumulates across calls and survives a store round-trip
// (JSON loads numbers back as float64, which must not break the sum). cached
// (KV-cache-served prompt tokens) accumulates next to prompt/completion.
func TestAddUsageTotal(t *testing.T) {
	s := &Session{record: &SessionRecord{Metadata: map[string]interface{}{}}}
	s.addUsageTotal(1000, 950, 40)
	s.addUsageTotal(250, 250, 10)
	tot := s.record.Metadata[usageTotalMetadataKey].(map[string]interface{})
	if tot["prompt"] != int64(1250) || tot["cached"] != int64(1200) || tot["completion"] != int64(50) {
		t.Fatalf("in-memory totals = %v, want 1250/1200/50", tot)
	}
	// Simulate a reload from the store: JSON numbers come back as float64.
	// Old rows without the cached key read as 0 rather than breaking the sum.
	s.record.Metadata[usageTotalMetadataKey] = map[string]interface{}{
		"prompt": float64(1250), "completion": float64(50),
	}
	s.addUsageTotal(7, 5, 3)
	tot = s.record.Metadata[usageTotalMetadataKey].(map[string]interface{})
	if tot["prompt"] != int64(1257) || tot["cached"] != int64(5) || tot["completion"] != int64(53) {
		t.Fatalf("post-reload totals = %v, want 1257/5/53", tot)
	}
}

// The lightweight list rows (root summaries and ListChildren) carry the
// session-wide token totals extracted from metadata, so the sub-agent panel
// can show per-task tokens without loading payloads.
func TestUsageTotalInSummaries(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	root := NewSessionRecord("root", "build", "", "")
	if err := store.Create(root); err != nil {
		t.Fatalf("create root: %v", err)
	}
	child := NewSessionRecord("child", "work", "", root.SessionID)
	child.Metadata["usage_total"] = map[string]interface{}{"prompt": float64(123456), "completion": float64(789)}
	if err := store.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	kids, err := store.ListChildren(root.SessionID)
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(kids) != 1 {
		t.Fatalf("want 1 child, got %d", len(kids))
	}
	if kids[0].TotalPrompt != 123456 || kids[0].TotalCompletion != 789 {
		t.Errorf("child totals = %d/%d, want 123456/789", kids[0].TotalPrompt, kids[0].TotalCompletion)
	}

	// The root (no counter yet) reads 0/0 rather than missing.
	roots, err := store.ListSessionSummaries(100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, s := range roots {
		if s.SessionID == root.SessionID && (s.TotalPrompt != 0 || s.TotalCompletion != 0) {
			t.Errorf("root totals = %d/%d, want 0/0", s.TotalPrompt, s.TotalCompletion)
		}
	}
}
