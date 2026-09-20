// Package desktopdarwin implements the desktop shell on macOS: a native
// WKWebView window (webview/webview_go), a single-instance lock file + unix
// domain socket, osascript fatal dialogs and AppKit dark title bars.
package desktopdarwin

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void mc_set_app_dark(int dark);
void mc_center_window(void *w);
void mc_show_window(void *w);
*/
import "C"

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/webview/webview_go"
	"golang.org/x/sys/unix"

	"github.com/user/mini_code/desktop"
)

const (
	lockFile = "desktop.lock"
	sockFile = "mini_code_desktop.sock"
)

// New returns the macOS desktop shell.
func New() desktop.Platform { return &platform{} }

type platform struct{}

func configDir() string {
	base, err := os.UserConfigDir() // ~/Library/Application Support
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "mini_code")
}

func socketPath() string { return filepath.Join(os.TempDir(), sockFile) }

// Fatal reports a startup problem to the user. Terminal builds print it;
// double-click launches (no console) get a native alert via osascript.
func (p *platform) Fatal(msg string) {
	slog.Error("desktop: " + msg)
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	_, _ = exec.Command("osascript", "-e", fmt.Sprintf(
		"display dialog %q with title \"Mini Code\" buttons {\"好\"} default button 1 with icon stop",
		msg,
	)).CombinedOutput()
	os.Exit(1)
}

// AcquireSingleInstance holds an exclusive flock on
// ~/Library/Application Support/mini_code/desktop.lock.
func (p *platform) AcquireSingleInstance() (bool, func()) {
	path := filepath.Join(configDir(), lockFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("desktop: cannot create lock dir, continuing", "err", err)
		return true, func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		slog.Warn("desktop: lock file failed, continuing", "err", err)
		return true, func() {}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return false, func() {}
	}
	return true, func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}
}

// NotifyExisting asks the running instance to show/focus its window via a
// unix domain socket (the client writes "show\n" and closes).
func (p *platform) NotifyExisting() error {
	path := socketPath()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_, werr := conn.Write([]byte("show\n"))
			conn.Close()
			if werr == nil {
				return nil
			}
			lastErr = werr
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no reachable instance: %w", lastErr)
}

// StartShowListener serves "show" requests from second launches.
func (p *platform) StartShowListener(onShow func()) {
	path := socketPath()
	_ = os.Remove(path) // stale socket from a crashed run
	l, err := net.Listen("unix", path)
	if err != nil {
		slog.Warn("desktop: show listener not started", "err", err)
		return
	}
	slog.Info("desktop: single-instance listener ready", "path", path)
	go func() {
		defer os.Remove(path)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(io.Discard, c) // client writes then closes
				onShow()
			}(conn)
		}
	}()
}

// NewWindow creates the WKWebView window. WKWebView is a system framework,
// so no runtime installation is needed.
func (p *platform) NewWindow(opts desktop.WindowOptions) (desktop.Window, error) {
	// debug: F12 devtools, same as the Windows build
	wv := webview.NewWindow(true, nil)
	w := &window{wv: wv}
	wv.SetTitle(opts.Title)
	wv.SetSize(opts.Width, opts.Height, webview.HintNone)
	C.mc_center_window(wv.Window())
	return w, nil
}

// ShowWindow makes the (already open) window key and frontmost.
func (p *platform) ShowWindow(w desktop.Window) {
	if h := w.Handle(); h != nil {
		C.mc_show_window(h)
	}
}

// SetDarkTitleBar switches the app appearance to dark/light so the native
// title bar matches the in-page theme.
func (p *platform) SetDarkTitleBar(w desktop.Window, dark bool) {
	v := 0
	if dark {
		v = 1
	}
	C.mc_set_app_dark(C.int(v))
}

// PrefersDark: the native title bar follows the system by default, and the
// page syncs the exact value after it renders, so there is no extra initial
// hint on macOS.
func (p *platform) PrefersDark() bool { return false }

// RunTray starts the menu-bar tray item: 打开窗口 / 退出.
//
// NOTE: on macOS systray's event loop is [NSApp run], the same NSApp the
// webview uses — verified pattern (getlantern/systray ships a webview
// example), but real-machine testing is required before shipping.
func (p *platform) RunTray(icon []byte, onOpen func(), onExit func()) {
	go systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTooltip("Mini Code — 窗口已隐藏，任务继续运行")
		openItem := systray.AddMenuItem("打开窗口", "显示 Mini Code 窗口")
		exitItem := systray.AddMenuItem("退出", "完全退出 Mini Code")
		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					onOpen()
				case <-exitItem.ClickedCh:
					onExit()
					systray.Quit()
					return
				}
			}
		}()
	}, nil)
}

// window adapts a webview_go WebView to desktop.Window.
type window struct {
	wv webview.WebView
}

func (w *window) Run() {
	w.wv.Run()
}

func (w *window) Destroy() {
	w.wv.Terminate()
	w.wv.Destroy()
}

func (w *window) Handle() unsafe.Pointer {
	return w.wv.Window()
}

func (w *window) Bind(name string, fn interface{}) error {
	return w.wv.Bind(name, fn)
}

func (w *window) Dispatch(fn func()) {
	w.wv.Dispatch(fn)
}

func (w *window) Navigate(url string) {
	w.wv.Navigate(url)
}
