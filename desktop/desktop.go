// Package desktop implements the "shell" desktop mode: it runs the mini_code
// web UI + API in-process and renders it in a native window (WebView2 on
// Windows, WKWebView on macOS) instead of a browser tab.
//
// Platform-specific pieces (window creation, single instance, fatal dialogs,
// title-bar theming) are injected via the Platform interface; see the
// desktop/windows and desktop/darwin packages.
//
// Behavior:
//   - The HTTP server still listens (127.0.0.1:<web_port>), so the page in
//     the window talks to it exactly like a browser would (fetch + SSE) and
//     the web UI stays available in a regular browser as well.
//   - Closing the window hides it; the process keeps running in the system
//     tray. The window is re-opened from the tray menu or by launching the
//     binary a second time.
//   - "退出" in the tray menu (or Ctrl+C in a terminal) stops everything.
package desktop

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/user/mini_code/config"
)

// Runtime is the mini_code core started for the desktop window. It is
// implemented in package main over the internal *runtime.
type Runtime interface {
	// CloseAll releases all core resources on shutdown.
	CloseAll()
	// Handler returns the HTTP handler serving the web UI + API.
	Handler() http.Handler
	// StartHub runs the message hub until ctx is done.
	StartHub(ctx context.Context)
}

// Window is a native window rendering the local web UI.
type Window interface {
	// Run blocks until the user closes the window.
	Run()
	// Destroy closes the window programmatically (unblocks Run).
	Destroy()
	// Handle returns the native window handle (HWND / NSWindow *).
	Handle() unsafe.Pointer
	// Bind exposes a Go function as a global JavaScript function. It must
	// be called before Run.
	Bind(name string, fn interface{}) error
	// Dispatch runs fn on the UI thread.
	Dispatch(fn func())
	// Navigate loads the URL.
	Navigate(url string)
}

// WindowOptions describes the window to create.
type WindowOptions struct {
	Title  string
	Width  int
	Height int
}

// Platform provides the OS-specific shell primitives.
type Platform interface {
	// Fatal reports a startup problem to the user (native dialog) and exits.
	Fatal(msg string)
	// AcquireSingleInstance locks the single desktop instance. first is
	// false when another instance already owns the lock; release must be
	// called on exit.
	AcquireSingleInstance() (first bool, release func())
	// NotifyExisting asks the running instance to show/focus its window.
	// It returns an error when no reachable instance was found.
	NotifyExisting() error
	// StartShowListener serves "show" requests from second launches.
	StartShowListener(onShow func())
	// NewWindow creates the native window.
	NewWindow(opts WindowOptions) (Window, error)
	// ShowWindow makes the window visible and foreground (e.g. from tray).
	ShowWindow(w Window)
	// SetDarkTitleBar switches the native title bar to dark/light to match
	// the in-page theme.
	SetDarkTitleBar(w Window, dark bool)
	// PrefersDark is the initial title-bar hint before the page loads
	// (the page syncs the exact value after it renders).
	PrefersDark() bool
	// RunTray starts the system tray (icon + 打开窗口/退出 menu). onExit is
	// called when the user picks 退出; the platform owns systray.Quit.
	RunTray(icon []byte, onOpen func(), onExit func())
}

// Deps wires the desktop shell to the application core.
type Deps struct {
	Platform   Platform
	TrayIcon   []byte
	NewRuntime func(rest []string) (Runtime, error)
}

// appState owns the window lifecycle: create -> run; close hides, open
// re-creates.
type appState struct {
	platform Platform
	url      string

	mu       sync.Mutex
	current  Window
	openCh   chan struct{}
	closeCh  chan struct{}
	closed   bool
	quitOnce sync.Once
}

func (a *appState) setCurrent(w Window) {
	a.mu.Lock()
	a.current = w
	a.mu.Unlock()
}

func (a *appState) requestOpen() {
	select {
	case a.openCh <- struct{}{}:
	default:
	}
}

func (a *appState) shutdown() {
	a.quitOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		w := a.current
		a.mu.Unlock()
		if w != nil {
			w.Destroy() // -> Run returns
		}
		close(a.closeCh)
		slog.Info("desktop: shutting down")
	})
}

// isClosed reports whether the shutdown sequence has started.
func (a *appState) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

func (a *appState) handleShow() {
	a.mu.Lock()
	w := a.current
	closed := a.closed
	a.mu.Unlock()
	if w != nil {
		a.platform.ShowWindow(w)
		return
	}
	if closed {
		return
	}
	a.requestOpen()
}

// isMiniCodeServing reports whether a mini_code web API already answers at
// the given URL (used to attach the window to a running instance instead of
// starting a second runtime on the same data store).
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

// Run implements "mini_code desktop". args are the raw mode arguments
// (may contain -p / -p=<port>); the rest is forwarded to the runtime.
func Run(args []string, d Deps) {
	platform := d.Platform

	// --- port: -p flag wins, else config web_port ---
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
	if port == 0 {
		if cfg, err := config.LoadConfig(); err == nil && cfg.WebPort > 0 {
			port = cfg.WebPort
		} else {
			port = 7500
		}
	}

	// --- single instance ---
	first, release := platform.AcquireSingleInstance()
	if !first {
		if err := platform.NotifyExisting(); err != nil {
			// The other instance is still starting up; retry briefly, then
			// fall through to a normal startup (its port will be taken, and
			// the user gets a clear error instead of a silent no-op).
			time.Sleep(1500 * time.Millisecond)
			if err2 := platform.NotifyExisting(); err2 != nil {
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
	defer release()

	// --- local HTTP server (the window is just another client of it).
	// Windows allows a 127.0.0.1 bind next to a 0.0.0.0 owner, so this must
	// be checked BEFORE listening, not as a fallback.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var (
		rt         Runtime
		httpServer *http.Server
		ctx        context.Context
		cancel     context.CancelFunc
	)
	healthURL := fmt.Sprintf("http://%s/api/healthz", addr)
	if isMiniCodeServing(healthURL) {
		slog.Info("desktop: attaching to running web server", "addr", addr)
	} else if ln, err := net.Listen("tcp4", addr); err != nil {
		platform.Fatal(fmt.Sprintf("端口 %d 被占用，无法启动桌面窗口。请先关闭占用该端口的程序，或用 -p 指定其他端口（例: mini_code desktop -p %d）。", port, port+1))
	} else {
		var err error
		rt, err = d.NewRuntime(rest)
		if err != nil {
			platform.Fatal(fmt.Sprintf("初始化失败: %v", err))
		}
		defer rt.CloseAll()

		httpServer = &http.Server{Handler: rt.Handler()}
		go httpServer.Serve(ln)

		// --- signal ctx (Ctrl+C from a terminal stops everything) ---
		ctx, cancel = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		rt.StartHub(ctx)
	}

	app := &appState{
		platform: platform,
		url:      fmt.Sprintf("http://127.0.0.1:%d", port),
		openCh:   make(chan struct{}, 1),
		closeCh:  make(chan struct{}),
	}

	// --- show listener (second launch focuses this one) ---
	platform.StartShowListener(app.handleShow)
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
		platform.Fatal("本地服务未就绪，桌面窗口启动失败")
	}

	slog.Info("starting desktop channel", "port", port, "url", app.url)

	// --- tray (survives window close) ---
	platform.RunTray(d.TrayIcon, app.handleShow, app.shutdown)

	// --- window loop: create -> run; close hides, open re-creates ---
	for {
		// 退出优先:退出流程一旦开始,迟到的"打开窗口"请求不再复活窗口。
		if app.isClosed() {
			break
		}
		w, err := platform.NewWindow(WindowOptions{Title: "Mini Code", Width: 1280, Height: 800})
		if err != nil {
			platform.Fatal(err.Error())
		}
		app.setCurrent(w)
		slog.Info("desktop: window opened", "handle", w.Handle())
		// 标题栏初始颜色按系统给,页面加载后 JS 会按实际主题再校正
		platform.SetDarkTitleBar(w, platform.PrefersDark())
		// 页面切换主题(浅色/深色/随系统)时回调,同步原生标题栏
		_ = w.Bind("mcSetDarkTitleBar", func(dark bool) {
			w.Dispatch(func() { platform.SetDarkTitleBar(w, dark) })
		})
		w.Navigate(app.url)
		w.Run() // returns when the user closes the window
		app.setCurrent(nil)
		// 退出优先:shutdown 会 Destroy 当前窗口使 Run 返回,此处必须结束循环。
		if app.isClosed() {
			break
		}
		slog.Info("desktop: window closed, continuing in tray")
		select {
		case <-app.openCh:
			// 打开窗口:回循环顶部重建窗口(顶部有退出检查)
			continue
		case <-app.closeCh:
			// 退出:回循环顶部由 isClosed 检查结束循环。
			// (Go 的 break 只跳出 select 不跳出 for——直接 break 会掉回
			//  NewWindow 把窗口再开一次,表现为"点退出反而打开了窗口")
		}
	}

	// --- teardown ---
	if httpServer != nil {
		httpServer.Close()
	}
	slog.Info("desktop: stopped")
}
