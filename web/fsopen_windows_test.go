//go:build windows

package web

import (
	"path/filepath"
	"testing"
)

func TestOpenLocalPathReportsFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here-9284.html")
	if err := openLocalPath(missing); err == nil {
		t.Fatalf("openLocalPath(%q) = nil, want an error", missing)
	}
}
