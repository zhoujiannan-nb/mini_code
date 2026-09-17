//go:build !windows

// Desktop mode exists only on Windows (WebView2). On other platforms the
// web UI is served as before and consumed in a browser.

package main

import "fmt"

func runDesktop(args []string) {
	fatal(fmt.Errorf("desktop 模式仅支持 Windows（WebView2）。其他平台请使用网页端：启动后在浏览器打开 http://127.0.0.1:<web_port>"))
}
