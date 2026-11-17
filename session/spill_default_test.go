package session

import (
	"os"
	"path/filepath"
	"testing"
)

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
	p := spillToolText(s.SpillDir(), "sD", "cD", "body")
	if p == "" {
		t.Fatal("spill to default dir failed")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("spill file missing: %v", err)
	}
}
