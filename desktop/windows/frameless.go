package desktopwin

import (
	"log/slog"
	"strings"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	gwlWndproc = -4
	gwlStyle   = -16

	wmSize         = 0x0005
	wmDestroy      = 0x0002
	wmNCCalcSiz    = 0x0083
	wmNCHitTest    = 0x0084
	wmNCLButtonDown  = 0x00A1
	wmClose        = 0x0010
	wmParentNotify = 0x0091
	wmTimer        = 0x0113
	wmnCreate      = 0x0001
	wmnDestroy     = 0x0002

	htTransparent = ^uintptr(0)

	htClient  = 1
	htCaption = 2

	vkLButton = 0x01

	wsCaption = 0x00C00000

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020

	swMinimize = 6
	swMaximize = 3

	gwChild    = 5
	gwHwndNext = 2

	smCxSizeFrame = 32
	smCxPadBorder = 92

	tbLogicalH  = 40
	tbAppW      = 0
	tbControlsW = 184

	childHookTimer = 0x4D43
	childHookMs    = 300
	childHookTries = 200
)

var gwlWndprocNeg int64 = gwlWndproc
var gwlWndprocUP = uintptr(gwlWndprocNeg)
var gwlStyleNeg int64 = gwlStyle
var gwlStyleUP = uintptr(gwlStyleNeg)

var (
	procCallWindowProcW        = user32.NewProc("CallWindowProcW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procSetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procGetClientRect          = user32.NewProc("GetClientRect")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procGetWindow              = user32.NewProc("GetWindow")
	procGetClassNameW          = user32.NewProc("GetClassNameW")
	procGetCapture             = user32.NewProc("GetCapture")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")
	procIsZoomed               = user32.NewProc("IsZoomed")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procGetAsyncKeyState       = user32.NewProc("GetAsyncKeyState")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procDwmExtendFrame         = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmExtendFrameIntoClientArea")
)

type winRect struct{ l, t, r, b int32 }
type ncCalcParams struct{ rgrc [3]winRect }

type framelessCtx struct {
	hwnd   uintptr
	orig   uintptr
	wv     webview2.WebView
	zoomed bool
	cbPtr  uintptr

	children  map[uintptr]uintptr
	hookTries int
}

var framelessState *framelessCtx

func installFrameless(w *window) {
	hwnd := uintptr(w.Handle())
	if hwnd == 0 {
		return
	}
	ctx := &framelessCtx{hwnd: hwnd, wv: w.wv, zoomed: zoomed(hwnd), children: make(map[uintptr]uintptr)}
	cb := syscall.NewCallback(framelessWndproc)
	ctx.cbPtr = cb
	ctx.orig, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlWndprocUP, cb)
	framelessState = ctx

	st, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyleUP)
	if st&wsCaption != 0 {
		procSetWindowLongPtrW.Call(hwnd, gwlStyleUP, st&^uintptr(wsCaption))
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
			swpNoSize|swpNoMove|swpNoZOrder|swpFrameChanged)
	}

	m := [...]int32{-1, -1, -1, -1}
	procDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m[0])))

	pref := uint32(2)
	procDwmSetWindowAttr.Call(hwnd, dwmwaCornerPref, uintptr(unsafe.Pointer(&pref)), 4)

	hookBrowserChildren(ctx)
	if len(ctx.children) == 0 {
		procSetTimer.Call(hwnd, childHookTimer, childHookMs, 0)
	}

	_ = w.Bind("mcWinMin", func() { procShowWindow.Call(hwnd, swMinimize) })
	_ = w.Bind("mcWinMax", func() {
		cmd := uintptr(swMaximize)
		if zoomed(hwnd) {
			cmd = swRestore
		}
		procShowWindow.Call(hwnd, cmd)
	})
	_ = w.Bind("mcWinClose", func() { procPostMessageW.Call(hwnd, wmClose, 0, 0) })
	_ = w.Bind("mcWinDrag", func() {
		if ks, _, _ := procGetAsyncKeyState.Call(vkLButton); int16(uint16(ks)) >= 0 {
			return
		}
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
	})
	slog.Info("desktop: frameless title bar installed")
}

func zoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

func framelessWndproc(hwnd, msg, wp, lp uintptr) uintptr {
	ctx := framelessState
	if ctx == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	if hwnd == ctx.hwnd {
		return frameRootProc(ctx, hwnd, msg, wp, lp)
	}
	if orig, ok := ctx.children[hwnd]; ok {
		return frameChildProc(ctx, hwnd, orig, msg, wp, lp)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func frameRootProc(ctx *framelessCtx, hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmNCCalcSiz:
		if wp != 0 {
			if !zoomed(hwnd) {
				r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
				return r
			}
			lpLocal := lp
			p := *(**ncCalcParams)(unsafe.Pointer(&lpLocal))
			f := borderInset(hwnd)
			p.rgrc[0].l += f
			p.rgrc[0].r -= f
			p.rgrc[0].t += f
			p.rgrc[0].b -= f
			return 0
		}
	case wmNCHitTest:
		if h := frameHitTest(hwnd, lParamX(lp), lParamY(lp)); h != 0 {
			return h
		}
	case wmParentNotify:
		switch wp & 0xffff {
		case wmnCreate:
			hookBrowserChildren(ctx)
		case wmnDestroy:
			delete(ctx.children, uintptr(wp>>16))
		}
	case wmTimer:
		if wp == childHookTimer {
			ctx.hookTries++
			if hookBrowserChildren(ctx) > 0 {
				procKillTimer.Call(hwnd, childHookTimer)
			} else if ctx.hookTries >= childHookTries {
				procKillTimer.Call(hwnd, childHookTimer)
				if len(ctx.children) == 0 {
					slog.Warn("desktop: 始终没等到 WebView2 渲染子窗口,边缘缩放/标题栏拖动可能无效")
				}
			}
		}
	case wmSize:
		hookBrowserChildren(ctx)
		z := zoomed(hwnd)
		if z != ctx.zoomed {
			ctx.zoomed = z
			ctx.wv.Eval("window.__mcZoom&&window.__mcZoom(" + boolLit(z) + ")")
		}
	case wmDestroy:
		procKillTimer.Call(hwnd, childHookTimer)
	}
	r, _, _ := procCallWindowProcW.Call(ctx.orig, hwnd, msg, wp, lp)
	return r
}

func frameChildProc(ctx *framelessCtx, hwnd, orig, msg, wp, lp uintptr) uintptr {
	if msg == wmNCHitTest {
		r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wp, lp)
		if cap_, _, _ := procGetCapture.Call(0); cap_ != 0 && cap_ != ctx.hwnd {
			return r
		}
		if r == htClient && frameHitTest(ctx.hwnd, lParamX(lp), lParamY(lp)) != 0 {
			return htTransparent
		}
		return r
	}
	r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wp, lp)
	return r
}

func hookBrowserChildren(ctx *framelessCtx) int {
	if ctx.cbPtr == 0 {
		return 0
	}
	return hookChildTree(ctx, ctx.hwnd, 0)
}

func hookChildTree(ctx *framelessCtx, parent uintptr, depth int) int {
	if depth > 4 {
		return 0
	}
	n := 0
	child, _, _ := procGetWindow.Call(parent, gwChild)
	for child != 0 {
		if class := childClass(child); browserClass(class) {
			if _, hooked := ctx.children[child]; !hooked {
				orig, _, _ := procSetWindowLongPtrW.Call(child, gwlWndprocUP, ctx.cbPtr)
				if orig == 0 {
					orig, _, _ = procGetWindowLongPtrW.Call(child, gwlWndprocUP)
				}
				ctx.children[child] = orig
				n++
				slog.Info("desktop: hooked webview child window for hit-testing", "hwnd", child, "class", class)
			}
		}
		n += hookChildTree(ctx, child, depth+1)
		next, _, _ := procGetWindow.Call(child, gwHwndNext)
		if next == 0 || next == child {
			break
		}
		child = next
	}
	return n
}

func browserClass(class string) bool {
	switch {
	case strings.HasPrefix(class, "Chrome_WidgetWin"),
		strings.HasPrefix(class, "Chrome_Legacy_Window"),
		class == "Chrome_RenderWidgetHostHWND",
		class == "Intermediate D3D Window":
		return true
	}
	return false
}

func childClass(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func lParamX(lp uintptr) int32 { return int32(int16(uint16(lp & 0xffff))) }
func lParamY(lp uintptr) int32 { return int32(int16(uint16((lp >> 16) & 0xffff))) }

func frameHitTest(root uintptr, gx, gy int32) uintptr {
	var rc winRect
	if r, _, _ := procGetWindowRect.Call(root, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return 0
	}
	scale := windowScale(root)
	x := float64(int32(gx)-int32(rc.l)) / scale
	y := float64(int32(gy)-int32(rc.t)) / scale
	w := float64(int32(rc.r) - int32(rc.l))
	h := float64(int32(rc.b) - int32(rc.t))

	if zoomed(root) {
		inset := float64(borderInset(root)) / scale
		x -= inset
		y -= inset
		w = w/scale - 2*inset
		h = h/scale - 2*inset
		return hitTestFrame(x, y, w, h, true)
	}

	var rcC winRect
	if r, _, _ := procGetClientRect.Call(root, uintptr(unsafe.Pointer(&rcC))); r == 0 {
		return 0
	}
	x -= float64(int32(rc.r)-int32(rc.l)-int32(rcC.r)) / 2 / scale
	y -= float64(int32(rc.b)-int32(rc.t)-int32(rcC.b)) / 2 / scale
	return hitTestFrame(x, y, float64(int32(rcC.r))/scale, float64(int32(rcC.b))/scale, false)
}

func hitTestFrame(x, y, w, h float64, zoomed bool) uintptr {
	if x < 0 || y < 0 || x >= w || y >= h {
		return 0
	}
	if y < tbLogicalH && x >= tbAppW && x < w-tbControlsW {
		return htCaption
	}
	return 0
}

func windowScale(hwnd uintptr) float64 {
	if d := windowDPI(hwnd); d >= 96 {
		return float64(d) / 96.0
	}
	if d := systemDPI(); d >= 96 {
		return float64(d) / 96.0
	}
	return 1
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func borderInset(hwnd uintptr) int32 {
	d := windowDPI(hwnd)
	if d >= 96 && procGetSystemMetricsForDpi.Find() == nil {
		f, _, _ := procGetSystemMetricsForDpi.Call(smCxSizeFrame, uintptr(d))
		p, _, _ := procGetSystemMetricsForDpi.Call(smCxPadBorder, uintptr(d))
		if f+p > 0 {
			return int32(f + p)
		}
	}
	s, _, _ := procGetSystemMetrics.Call(smCxSizeFrame)
	p, _, _ := procGetSystemMetrics.Call(smCxPadBorder)
	f := int32(s) + int32(p)
	if dpi := systemDPI(); dpi > 96 {
		f = f * int32(dpi) / 96
	}
	return f
}
