package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	return info.Size()
}

func TestRotatingWriterChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	w := newRotatingWriter(path, 300, 2)
	defer func() {
		w.mu.Lock()
		if w.file != nil {
			w.file.Close()
		}
		w.mu.Unlock()
	}()

	line := strings.Repeat("x", 90) + "\n"
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("%02d %s", i, line))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := sizeOf(t, path); got > 300 {
		t.Fatalf("active log = %d bytes, over the 300 cap", got)
	}
	for _, name := range []string{"agent.log.1", "agent.log.2"} {
		if sizeOf(t, filepath.Join(dir, name)) == 0 {
			t.Fatalf("%s missing/empty after 10 lines", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.log.3")); err == nil {
		t.Fatal("agent.log.3 should have been dropped from the chain")
	}
	last, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(last), "09 ") {
		t.Fatalf("active log does not start with the newest record: %q", last[:6])
	}
}

func TestRotatingWriterDemotesOversizeLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 5000)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte(strings.Repeat("z", 5000)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".3", []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	w := newRotatingWriter(path, 300, 2)
	defer func() {
		w.mu.Lock()
		if w.file != nil {
			w.file.Close()
		}
		w.mu.Unlock()
	}()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if got := sizeOf(t, path); got != 6 {
		t.Fatalf("active log = %d bytes, want a fresh 6-byte file", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.log.1")); err != nil {
		t.Fatalf("oversized base was not demoted to .1: %v", err)
	}
	if sizeOf(t, filepath.Join(dir, "agent.log.1")) != 5000 {
		t.Fatal("agent.log.1 is not the demoted base")
	}
	if sizeOf(t, filepath.Join(dir, "agent.log.2")) != 5000 {
		t.Fatal("the previous backup did not shift to .2")
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.log.3")); err == nil {
		t.Fatal("stale agent.log.3 beyond the keep count survived")
	}
}
