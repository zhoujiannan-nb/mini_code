//go:build darwin && desktop_darwin

// This file is compiled only into the macOS desktop build (see
// build_darwin.sh, -tags desktop_darwin). It selects the WKWebView shell.

package main

import (
	_ "embed"

	"github.com/user/mini_code/desktop"
	darwin "github.com/user/mini_code/desktop/darwin"
)

//go:embed mini_code.png
var desktopTrayIcon []byte

func newDesktopPlatform() desktop.Platform { return darwin.New() }
