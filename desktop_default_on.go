//go:build desktop_default

// This file is compiled only into the desktop build (see build.bat,
// -tags desktop_default). It makes the no-argument default mode "desktop"
// so that double-clicking mini_code_desktop.exe opens the window instead
// of silently starting a web server.
package main

const desktopDefault = true
