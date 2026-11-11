package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultSessionStoreHasSpillDir guards the production wiring: the
// default-path constructor (used by main.go) must set spillDir like
// NewSessionStore does — it once shipped empty, so pruned results in the
// real app never spilled their full text.
func TestDefaultSessionStoreHasSpillDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	s, err := NewDefaultSessionStore()
	if err != nil {
		t.Fatalf("NewDefaultSessionStore: %v", err)
	}
	defer s.Close()
	if s.SpillDir() == "" {
		t.Fatal("default store has empty spillDir")
	}
	if filepath.Base(s.SpillDir()) != "spill" {
		t.Fatalf("unexpected spillDir: %s", s.SpillDir())
	}
	// The dir is created lazily by the spill write itself; verify a write works.
	p := spillToolText(s.SpillDir(), "sD", "cD", "body")
	if p == "" {
		t.Fatal("spill to default dir failed")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("spill file missing: %v", err)
	}
}
