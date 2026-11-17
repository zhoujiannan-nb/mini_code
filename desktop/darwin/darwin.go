package desktopdarwin

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void mc_set_app_dark(int dark);
void mc_center_window(void *w);
void mc_show_window(void *w);
void mc_setup_main_menu(void);
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

func New() desktop.Platform { return &platform{} }

type platform struct{}

func configDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "mini_code")
}

func socketPath() string { return filepath.Join(os.TempDir(), sockFile) }

func (p *platform) Fatal(msg string) {
	slog.Error("desktop: " + msg)
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	_, _ = exec.Command("osascript", "-e", fmt.Sprintf(
		"display dialog %q with title \"Mini Code\" buttons {\"好\"} default button 1 with icon stop",
		msg,
	)).CombinedOutput()
	os.Exit(1)
}

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

func (p *platform) StartShowListener(onShow func()) {
	path := socketPath()
	_ = os.Remove(path)
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
				io.Copy(io.Discard, c)
				onShow()
			}(conn)
		}
	}()
}

func (p *platform) NewWindow(opts desktop.WindowOptions) (desktop.Window, error) {
	C.mc_setup_main_menu()
	wv := webview.NewWindow(true, nil)
	w := &window{wv: wv}
	wv.SetTitle(opts.Title)
	wv.SetSize(opts.Width, opts.Height, webview.HintNone)
	C.mc_center_window(wv.Window())
	return w, nil
}

func (p *platform) ShowWindow(w desktop.Window) {
	if h := w.Handle(); h != nil {
		w.Dispatch(func() { C.mc_show_window(h) })
	}
}

func (p *platform) SetDarkTitleBar(w desktop.Window, dark bool) {
	v := 0
	if dark {
		v = 1
	}
	C.mc_set_app_dark(C.int(v))
}

func (p *platform) PrefersDark() bool { return false }

func (p *platform) RunTray(icon []byte, onOpen func(), onExit func()) {
	systray.Register(func() {
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
	}, onExit)
}

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
