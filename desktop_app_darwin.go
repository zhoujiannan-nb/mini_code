//go:build darwin && desktop_darwin


package main

import (
	_ "embed"

	"github.com/user/mini_code/desktop"
	darwin "github.com/user/mini_code/desktop/darwin"
)

//go:embed mini_code.png
var desktopTrayIcon []byte

func newDesktopPlatform() desktop.Platform { return darwin.New() }
