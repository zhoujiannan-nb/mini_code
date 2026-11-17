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

type Runtime interface {
	CloseAll()
	Handler() http.Handler
	StartHub(ctx context.Context)
}

type Window interface {
	Run()
	Destroy()
	Handle() unsafe.Pointer
	Bind(name string, fn interface{}) error
	Dispatch(fn func())
	Navigate(url string)
}

type WindowOptions struct {
	Title  string
	Width  int
	Height int
}

type Platform interface {
	Fatal(msg string)
	AcquireSingleInstance() (first bool, release func())
	NotifyExisting() error
	StartShowListener(onShow func())
	NewWindow(opts WindowOptions) (Window, error)
	ShowWindow(w Window)
	SetDarkTitleBar(w Window, dark bool)
	PrefersDark() bool
	RunTray(icon []byte, onOpen func(), onExit func())
}

type Deps struct {
	Platform   Platform
	TrayIcon   []byte
	NewRuntime func(rest []string) (Runtime, error)
}

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
			w.Destroy()
		}
		close(a.closeCh)
		slog.Info("desktop: shutting down")
	})
}

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

func Run(args []string, d Deps) {
	platform := d.Platform

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

	first, release := platform.AcquireSingleInstance()
	if !first {
		if err := platform.NotifyExisting(); err != nil {
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

	platform.StartShowListener(app.handleShow)
	if ctx != nil {
		go func() { <-ctx.Done(); app.shutdown() }()
	}

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

	platform.RunTray(d.TrayIcon, app.handleShow, app.shutdown)

	for {
		if app.isClosed() {
			break
		}
		w, err := platform.NewWindow(WindowOptions{Title: "Mini Code", Width: 1280, Height: 800})
		if err != nil {
			platform.Fatal(err.Error())
		}
		app.setCurrent(w)
		slog.Info("desktop: window opened", "handle", w.Handle())
		platform.SetDarkTitleBar(w, platform.PrefersDark())
		_ = w.Bind("mcSetDarkTitleBar", func(dark bool) {
			w.Dispatch(func() { platform.SetDarkTitleBar(w, dark) })
		})
		w.Navigate(app.url)
		w.Run()
		app.setCurrent(nil)
		if app.isClosed() {
			break
		}
		slog.Info("desktop: window closed, continuing in tray")
		select {
		case <-app.openCh:
			continue
		case <-app.closeCh:
		}
	}

	if httpServer != nil {
		httpServer.Close()
	}
	slog.Info("desktop: stopped")
}
