package desktopwin

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/user/mini_code/desktop"
)

const (
	instanceMutex = `Local\MiniCodeDesktopSingleInstance`
	pipeName      = `\\.\pipe\mini_code_desktop`
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
	procDwmSetWindowAttr = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

const (
	mbIconError = 0x10
	swShow      = 5
	swRestore   = 9
	gclpHIcon   = -14
	gclpHIconSm = -34
	dwmwaDark20 = 20
	dwmwaDark19 = 19
	dwmwaCornerPref = 33
)

func New() desktop.Platform { return &platform{} }

type platform struct{}

func (p *platform) Fatal(msg string) {
	slog.Error("desktop: " + msg)
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
	title, _ := windows.UTF16PtrFromString("Mini Code")
	body, _ := windows.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), mbIconError)
	os.Exit(1)
}

func (p *platform) AcquireSingleInstance() (bool, func()) {
	name, _ := windows.UTF16PtrFromString(instanceMutex)
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
		return false, func() {}
	}
	if errno != 0 {
		slog.Warn("desktop: single-instance mutex failed, continuing", "err", lastErr)
	}
	h := windows.Handle(r)
	return true, func() { windows.CloseHandle(h) }
}

func (p *platform) NotifyExisting() error {
	pipe, _ := windows.UTF16PtrFromString(pipeName)
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
	return fmt.Errorf("no reachable instance: %w", lastErr)
}

func (p *platform) StartShowListener(onShow func()) {
	go p.showListenerLoop(onShow)
}

func (p *platform) showListenerLoop(onShow func()) {
	name, _ := windows.UTF16PtrFromString(pipeName)
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
		windows.DisconnectNamedPipe(pipe)
		if len(data) > 0 {
			slog.Info("desktop: instance pipe message", "data", string(data))
			if strings.Contains(string(data), "show") {
				onShow()
			}
		}
	}
}

func (p *platform) NewWindow(opts desktop.WindowOptions) (desktop.Window, error) {
	appData, _ := os.UserConfigDir()
	dataPath := filepath.Join(appData, "mini_code", "wv2data")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		slog.Warn("desktop: cannot create webview data dir, using temporary profile", "err", err)
		dataPath = ""
	}

	if args := os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"); !strings.Contains(args, "autoplay-policy") {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
			strings.TrimSpace(args+" --autoplay-policy=no-user-gesture-required"))
	}

	winW, winH := scaledWindowSize(uint(opts.Width), uint(opts.Height))
	slog.Debug("desktop: creating window", "logical", opts.Width, "w", winW, "h", winH, "dpi", systemDPI())

	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:    true,
		DataPath: dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  opts.Title,
			Width:  winW,
			Height: winH,
			IconId: 1,
			Center: true,
		},
	})
	if wv == nil {
		return nil, fmt.Errorf("WebView2 初始化失败：请确认已安装 Microsoft Edge WebView2 运行时（Win10/Win11 通常自带；https://developer.microsoft.com/microsoft-edge/webview2/）")
	}
	w := &window{wv: wv}
	applyWindowIcon(w.Handle())
	installFrameless(w)
	return w, nil
}

func (p *platform) ShowWindow(w desktop.Window) {
	hwnd := uintptr(w.Handle())
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

func applyWindowIcon(hwnd unsafe.Pointer) uintptr {
	var hinstance windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinstance)
	icon, _, _ := procLoadImageW.Call(uintptr(hinstance), 1, 1, 0, 0, 0xC0)
	if icon == 0 {
		return 0
	}
	big, small := gclpHIcon, gclpHIconSm
	procSetClassLongPtrW.Call(uintptr(hwnd), uintptr(big), icon)
	procSetClassLongPtrW.Call(uintptr(hwnd), uintptr(small), icon)
	return icon
}

func (p *platform) SetDarkTitleBar(w desktop.Window, dark bool) {
	hwnd := uintptr(w.Handle())
	if hwnd == 0 {
		return
	}
	v := uintptr(0)
	if dark {
		v = 1
	}
	if r, _, _ := procDwmSetWindowAttr.Call(hwnd, dwmwaDark20, uintptr(unsafe.Pointer(&v)), 4); r != 0 {
		_, _, _ = procDwmSetWindowAttr.Call(hwnd, dwmwaDark19, uintptr(unsafe.Pointer(&v)), 4)
	}
}

func (p *platform) PrefersDark() bool {
	k, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

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

type window struct {
	wv webview2.WebView
}

func (w *window) Run() {
	w.wv.Run()
}

func (w *window) Destroy() {
	w.wv.Destroy()
}

func (w *window) Handle() unsafe.Pointer {
	return unsafe.Pointer(w.wv.Window())
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
