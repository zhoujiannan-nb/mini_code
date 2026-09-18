//go:build desktop_default

// This file is compiled only into the desktop build (see build.bat,
// -tags desktop_default). It makes the no-argument default mode "desktop"
// so that double-clicking mini_code.exe opens the window instead of
// silently starting a web server. Pass "web" explicitly for the plain
// web server mode.
package main

const desktopDefault = true
