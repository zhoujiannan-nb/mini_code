//go:build windows

// Desktop mode (Windows only): run the same web UI + API in-process and
// render it in a native WebView2 window instead of a browser tab.
//
// - The HTTP server still listens (127.0.0.1:<web_port>), so the UI in the
//   window talks to it exactly like a browser would (fetch + SSE). The web
//   page therefore remains fully available at http://127.0.0.1:<port>.
// - Closing the window hides it; the process keeps running in the system
//   tray (the DingTalk channel, if configured, keeps working). The window
//   is re-opened from the tray menu or by launching the exe a second time.
// - "退出" in the tray menu (or Ctrl+C in a console build) stops everything.

package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/web"
)

//go:embed mini_code.ico
var desktopIconICO []byte

const (
	desktopInstanceMutex = `Local\MiniCodeDesktopSingleInstance`
	desktopPipeName      = `\\.\pipe\mini_code_desktop`
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForegroundWin = user32.NewProc("SetForegroundWindow")
	procIsIconic         = user32.NewProc("IsIconic")
	procCreateMutexW     = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procSetClassLongPtrW = user32.NewProc("SetClassLongPtrW")
)

const (
	mbIconError  = 0x10
	swShow       = 5
	swRestore    = 9
	gclpHIcon    = -14
	gclpHIconSm  = -34
)

// desktopFatal reports a startup problem to the user. Console builds also
// print it; -H windowsgui builds have no console, so a message box is used.
func desktopFatal(msg string) {
	slog.Error("desktop: " + msg)
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	title, _ := windows.UTF16PtrFromString("Mini Code")
	body, _ := windows.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), mbIconError)
	os.Exit(1)
}

// acquireSingleInstance returns the mutex if this process is the first
// desktop instance, or a zero handle when another instance owns it.
//
// NOTE: x/sys Proc.Call returns Errno(0) (non-nil) on success, so the result
// must be checked numerically, not with a nil test.
func acquireSingleInstance() (windows.Handle, bool) {
	name, _ := windows.UTF16PtrFromString(desktopInstanceMutex)
	r, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	var errno syscall.Errno
	if e, ok := lastErr.(syscall.Errno); ok {
		errno = e
	}
	slog.Debug("desktop: mutex acquired", "handle", r, "errno", uint32(errno))
	if errno == windows.ERROR_ALREADY_EXISTS {
		if r != 0 {
			windows.CloseHandle(windows.Handle(r))
		}
		return 0, false
	}
	if errno != 0 {
		slog.Warn("desktop: single-instance mutex failed, continuing", "err", lastErr)
	}
	return windows.Handle(r), true
}

// notifyExistingInstance asks the running instance to show/focus its window.
// It returns an error when no instance (or no ready pipe) was found.
func notifyExistingInstance() error {
	pipe, _ := windows.UTF16PtrFromString(desktopPipeName)
	deadline := time.Now().Add(5 * time.Second)
	logged := false
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := windows.CreateFile(pipe, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			werr := windows.WriteFile(conn, []byte("show\n"), nil, nil)
			windows.CloseHandle(conn)
			if werr == nil {
				return nil
			}
			lastErr = werr
		} else {
			lastErr = err
			if !logged {
				slog.Info("desktop: notify pipe connect failed", "err", err)
				logged = true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("no reachable instance: %w", lastErr)
	}
	return fmt.Errorf("no reachable instance")
}

// startInstancePipe listens for "show" commands from second launches.
func startInstancePipe(onShow func()) {
	name, _ := windows.UTF16PtrFromString(desktopPipeName)
	pipe, err := windows.CreateNamedPipe(
		name, windows.PIPE_ACCESS_INBOUND, 0, 1, 4096, 4096, 0, nil)
	if err != nil {
		slog.Warn("desktop: instance pipe not started", "err", err)
		return
	}
	slog.Info("desktop: single-instance pipe ready")
	connectWarns := 0
	for {
		err := windows.ConnectNamedPipe(pipe, nil)
		if err != nil {
			if connectWarns < 3 {
				slog.Warn("desktop: pipe connect waiting", "err", err)
				connectWarns++
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// Read the whole short command. The client writes and closes
		// immediately, so read until EOF or the keyword is seen.
		var data []byte
		buf := make([]byte, 64)
		for {
			var got uint32
			rerr := windows.ReadFile(pipe, buf, &got, nil)
			if got > 0 {
				data = append(data, buf[:got]...)
				if strings.Contains(string(data), "show") {
					break
				}
			}
			if got == 0 || rerr != nil {
				break
			}
		}
		// Reset the pipe so the next ConnectNamedPipe can listen again.
		windows.DisconnectNamedPipe(pipe)
		if len(data) > 0 {
			slog.Info("desktop: instance pipe message", "data", string(data))
			if strings.Contains(string(data), "show") {
				onShow()
			}
		}
	}
}

// showOrFocus makes the (already open) window visible and foreground.
// It is called from the pipe-reader goroutine, hence plain ShowWindow /
// SetForegroundWindow cross-thread calls.
func showOrFocus(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	cmd := uintptr(swShow)
	if isIconic(hwnd) {
		cmd = swRestore
	}
	_, _, _ = procShowWindow.Call(hwnd, cmd)
	_, _, _ = procSetForegroundWin.Call(hwnd)
}

func isIconic(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}

// applyWindowIcon loads the app icon (resource ID 1, from rsrc resources)
// and sets it on the window class. go-webview2 tries the same internally,
// but has been observed to end up with a default icon, so we enforce it.
func applyWindowIcon(hwnd uintptr) uintptr {
	var hinstance windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinstance)
	icon, _, _ := procLoadImageW.Call(uintptr(hinstance), 1, 1, 0, 0, 0xC0) // MAKEINTRESOURCE(1), IMAGE_ICON, LR_DEFAULTSIZE|LR_SHARED
	if icon == 0 {
		return 0
	}
	big, small := gclpHIcon, gclpHIconSm
	procSetClassLongPtrW.Call(hwnd, uintptr(big), icon)
	procSetClassLongPtrW.Call(hwnd, uintptr(small), icon)
	return icon
}

// desktopApp owns the desktop-mode window lifecycle.
type desktopApp struct {
	rt *runtime

	url  string
	port int

	mu       sync.Mutex
	current  webview2.WebView // open window, nil when hidden
	openCh   chan struct{}
	closeCh  chan struct{}
	closed   bool
	quitOnce sync.Once
}

func (a *desktopApp) setCurrent(wv webview2.WebView) {
	a.mu.Lock()
	a.current = wv
	a.mu.Unlock()
}

func (a *desktopApp) currentWnd() uintptr {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return 0
	}
	return wvPtr(a.current)
}

func (a *desktopApp) requestOpen() {
	select {
	case a.openCh <- struct{}{}:
	default:
	}
}

func (a *desktopApp) shutdown() {
	a.quitOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		wv := a.current
		a.mu.Unlock()
		if wv != nil {
			wv.Destroy() // WM_CLOSE -> Run() returns
		}
		close(a.closeCh)
		systray.Quit()
		slog.Info("desktop: shutting down")
	})
}

func (a *desktopApp) handleShow() {
	if wnd := a.currentWnd(); wnd != 0 {
		showOrFocus(wnd)
		return
	}
	if a.closed {
		return
	}
	a.requestOpen()
}

// runDesktop implements "mini_code desktop" on Windows.
func runDesktop(args []string) {
	var port int
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-p":
			if i+1 < len(args) {
				i++
				port, _ = strconv.Atoi(args[i])
			}
		case strings.HasPrefix(args[i], "-p="):
			port, _ = strconv.Atoi(strings.TrimPrefix(args[i], "-p="))
		default:
			rest = append(rest, args[i])
		}
	}

	// --- single instance ---
	mtx, first := acquireSingleInstance()
	if !first {
		if err := notifyExistingInstance(); err != nil {
			// The other instance is still starting up; retry briefly, then
			// fall through to a normal startup (its port will be taken, and
			// the user gets a clear error instead of a silent no-op).
			time.Sleep(1500 * time.Millisecond)
			if err2 := notifyExistingInstance(); err2 != nil {
				slog.Info("desktop: no reachable instance, starting a new one anyway", "err", err2)
			} else {
				slog.Info("desktop: existing instance focused, exiting")
				return
			}
		} else {
			slog.Info("desktop: existing instance focused, exiting")
			return
		}
	}
	defer windows.CloseHandle(mtx)

	// --- port: -p flag wins, else config web_port ---
	if port == 0 {
		if cfg, err := config.LoadConfig(); err == nil && cfg.WebPort > 0 {
			port = cfg.WebPort
		} else {
			port = 7500
		}
	}

	// --- local HTTP server (the window is just another client of it).
	// If a mini_code web server already serves this port (e.g. a running
	// "mini_code web"), the window attaches to that instance instead of
	// starting a second runtime on the same data store. (Windows allows a
	// 127.0.0.1 bind next to a 0.0.0.0 owner, so this must be checked
	// BEFORE listening, not as a fallback.)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var (
		rt         *runtime
		httpServer *http.Server
		ctx        context.Context
		cancel     context.CancelFunc
	)
	healthURL := fmt.Sprintf("http://%s/api/healthz", addr)
	if isMiniCodeServing(healthURL) {
		slog.Info("desktop: attaching to running web server", "addr", addr)
	} else if ln, err := net.Listen("tcp4", addr); err != nil {
		desktopFatal(fmt.Sprintf("端口 %d 被占用，无法启动桌面窗口。请先关闭占用该端口的程序，或用 -p 指定其他端口（例: mini_code desktop -p %d）。", port, port+1))
	} else {
		var err error
		rt, err = setup(rest, 0)
		if err != nil {
			desktopFatal(fmt.Sprintf("初始化失败: %v", err))
		}
		defer rt.closeClients()
		defer rt.mgr.Close()
		defer rt.groups.Close()

		httpServer = &http.Server{Handler: web.NewServer(rt)}
		go httpServer.Serve(ln)

		// --- signal ctx (matters for console builds) ---
		ctx, cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		go rt.hub.Run(ctx)
		rt.dt.apply(rt.liveConfig().DingTalk)
		defer rt.dt.stop()
	}

	// --- instance pipe (second launch focuses this one) ---
	app := &desktopApp{rt: rt, port: port, url: fmt.Sprintf("http://127.0.0.1:%d", port), openCh: make(chan struct{}, 1), closeCh: make(chan struct{})}
	go startInstancePipe(app.handleShow)
	if ctx != nil {
		go func() { <-ctx.Done(); app.shutdown() }()
	}

	// --- wait until the server answers, so the window never sees a 502 ---
	ok := false
	for i := 0; i < 50; i++ {
		resp, err := http.Get(healthURL)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ok = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		desktopFatal("本地服务未就绪，桌面窗口启动失败")
	}

	// --- WebView2 user data (persistent localStorage) ---
	appData, _ := os.UserConfigDir() // %APPDATA% on Windows
	dataPath := filepath.Join(appData, "mini_code", "wv2data")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		slog.Warn("desktop: cannot create webview data dir, using temporary profile", "err", err)
		dataPath = ""
	}

	slog.Info("starting desktop channel", "port", port, "url", app.url)

	// --- tray (survives window close; DingTalk keeps running in background) ---
	go systray.Run(func() {
		systray.SetIcon(desktopIconICO)
		systray.SetTooltip("Mini Code — 窗口已隐藏，任务继续运行")
		openItem := systray.AddMenuItem("打开窗口", "显示 Mini Code 窗口")
		exitItem := systray.AddMenuItem("退出", "完全退出 Mini Code")
		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					app.handleShow()
				case <-exitItem.ClickedCh:
					app.shutdown()
				case <-app.closeCh:
					return
				}
			}
		}()
	}, nil)
	go func() { <-ctx.Done(); app.shutdown() }()

	// --- window loop: create -> run; close hides, open re-creates ---
	for {
		wv := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:    true, // F12 devtools + right-click context menu
			DataPath: dataPath,
			WindowOptions: webview2.WindowOptions{
				Title:  "Mini Code",
				Width:  1280,
				Height: 800,
				IconId: 1, // app icon from rsrc resources, 0 = default
				Center: true,
			},
		})
		if wv == nil {
			desktopFatal("WebView2 初始化失败：请确认已安装 Microsoft Edge WebView2 运行时（Win10/Win11 通常自带；https://developer.microsoft.com/microsoft-edge/webview2/）。")
		}
		app.setCurrent(wv)
		hwnd := wvPtr(wv)
		slog.Info("desktop: window opened", "hwnd", hwnd, "class_icon", applyWindowIcon(hwnd))
		wv.Navigate(app.url)
		wv.Run() // returns when the user closes the window
		app.setCurrent(nil)
		if app.closed {
			break
		}
		slog.Info("desktop: window closed, continuing in tray")
		select {
		case <-app.openCh:
			continue
		case <-app.closeCh:
			break
		}
	}

	// --- teardown ---
	if httpServer != nil {
		httpServer.Close()
	}
	slog.Info("desktop: stopped")
}

// isMiniCodeServing reports whether a mini_code web API already answers at
// the given URL (used to attach the window to a running instance).
func isMiniCodeServing(healthURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// wvPtr extracts the Win32 window handle from a webview2.WebView.
func wvPtr(wv webview2.WebView) uintptr {
	return uintptr(wv.Window())
}
