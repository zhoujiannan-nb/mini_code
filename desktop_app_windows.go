//go:build windows

package main

import (
	_ "embed"

	"github.com/user/mini_code/desktop"
	windows "github.com/user/mini_code/desktop/windows"
)

//go:embed mini_code.ico
var desktopTrayIcon []byte

func newDesktopPlatform() desktop.Platform { return windows.New() }
