//go:build windows

package web

import (
	"path/filepath"
	"testing"
)

// TestOpenLocalPathReportsFailure exercises the real ShellExecuteW call for the
// one case that must not open anything: a path that does not exist. A success
// case is deliberately absent — it would launch Explorer on the test machine.
func TestOpenLocalPathReportsFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here-9284.html")
	if err := openLocalPath(missing); err == nil {
		t.Fatalf("openLocalPath(%q) = nil, want an error", missing)
	}
}
