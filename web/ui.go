package web

import (
	"embed"
	"fmt"
)

//go:embed all:ui
var uiFS embed.FS

// embeddedPage returns the built-in web UI page.
func embeddedPage() (string, error) {
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		return "", fmt.Errorf("read embedded ui: %w", err)
	}
	return string(b), nil
}
