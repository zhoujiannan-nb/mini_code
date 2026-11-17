//go:build !windows && (!darwin || !desktop_darwin)


package main

import "github.com/user/mini_code/desktop"

var desktopTrayIcon []byte

func newDesktopPlatform() desktop.Platform { return nil }
