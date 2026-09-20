//go:build !windows && (!darwin || !desktop_darwin)

// Desktop shell is not available here (Linux, or macOS without the
// -tags desktop_darwin build); "mini_code desktop" reports an error and the
// web UI is consumed in a browser as before.

package main

import "github.com/user/mini_code/desktop"

var desktopTrayIcon []byte

func newDesktopPlatform() desktop.Platform { return nil }
