//go:build !desktop_default

// Default build (mini_code.exe): no argument means web mode, exactly as
// before. The desktop build flips this via -tags desktop_default.
package main

const desktopDefault = false
