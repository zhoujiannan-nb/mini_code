// frameless.go implements the "dsh-style" borderless window: the WebView
// covers the whole window (WM_NCCALCSIZE returns zero non-client area), while
// DWM keeps rounded corners, the drop shadow and Aero Snap. The top strip of
// the page acts as the title bar: WM_NCHITTEST marks it HTCAPTION so it can
// be dragged, and min/max/close are JS-callable binds (mcWinMin / mcWinMax /
// mcWinClose). The page must detect frameless mode by checking for those
// globals before rendering its own title bar (see js/lib/titlebar.js).
//
// 「拖边缘没反应」两轮实测(诊断工具枚举运行中的窗口树)给出的最终方案:
//
//  1. WebView2 的窗口是四层套娃:root → Chrome_WidgetWin_0 → Chrome_WidgetWin_1
//     → Chrome_RenderWidgetHostHWND + Intermediate D3D Window,每层都按父窗口
//     client 铺满。鼠标命中永远落在最深那层 —— 只挂 Chrome_WidgetWin* 的旧方案
//     让位链走不到 root,拖边和拖顶都不生效。
//     现在:① 子类化覆盖上面【全部】浏览器类名,HTTRANSPARENT 逐层向上穿透;
//     ② 更根本的,四边缩放不再依赖穿透链 —— 见 2。
//
//  2. root 样式摘掉 WS_CAPTION(保留 WS_THICKFRAME):非最大化时 WM_NCCALCSIZE
//     直接回落 DefWindowProc,四周留下一圈【真】的 NC 缩放边框 —— 光标、角、
//     size loop 全是系统原生逻辑,浏览器子窗口(按 GetClientRect 铺)天然盖不到
//     这条边框。WM_NCHITTEST 从此只负责一件事:client 顶部 40px 拖拽带返回
//     HTCAPTION(左侧让出三点窗控带)。最大化时维持原方案:整窗皆 client,
//     NCCALCSIZE 内缩掉屏幕外的隐形缩放边框。
//
//  3. 坐标/缩放:WM_NCHITTEST 的 lParam 是【物理】屏幕坐标,而 40/282 这些
//     判定常量和页面 CSS px 一样是【逻辑】像素,统一用窗口自己的 DPI
//     (GetDpiForWindow)换算;非最大化时还要换算到 client 坐标系(client 比窗口
//     矩形小一圈缩放边框)。tbControlsW 必须等于 layers.css 的 --tb-ctl-w,
//     见下面常量处的说明。
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
	wmNCLButtonDown  = 0x00A1 // mcWinDrag 用它把一次按下伪装成"点在了标题栏上"
	wmClose        = 0x0010
	wmParentNotify = 0x0091
	wmTimer        = 0x0113
	wmnCreate      = 0x0001 // WM_PARENTNOTIFY 的 LOWORD(wParam):子窗口刚创建
	wmnDestroy     = 0x0002 // LOWORD=2:子窗口销毁,HIWORD 是它的句柄

	// HTTRANSPARENT(-1):此点交给 Z 序下面更低的窗口继续做命中测试。
	htTransparent = ^uintptr(0)

	htClient  = 1
	htCaption = 2

	// 摘标题栏样式用:保留 WS_THICKFRAME 的缩放边框,只去掉 WS_CAPTION,
	// 四边缩放交给系统原生 NC 处理(见文件头 2)。
	wsCaption = 0x00C00000

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020

	swMinimize = 6
	swMaximize = 3

	gwChild    = 5 // GetWindow 的第一个孩子
	gwHwndNext = 2 // GetWindow 的下一个兄弟

	smCxSizeFrame = 32 // SM_CXSIZEFRAME
	smCxPadBorder = 92 // SM_CXPADDEDBORDER

	// 逻辑像素,与页面 CSS 严格同步(layers.css 标题条段):
	//   tbLogicalH = --tb-h        拖拽条高 40px(自 client 顶起算)
	//   tbAppW     = --tb-app-w    左侧窗控排除区 —— v3.3 起 macOS 三点下线,
	//                              窗口钮改到右上,左侧不再有排除区,宽度 0
	//   tbControlsW = --tb-ctl-w   右侧窗口按钮组(v3.8 起 4 颗:主题/min/max/
	//                              close,4×46=184),与 CSS --tb-ctl-w 一致
	// 顶部 40px 里【左侧排除区以右、右侧钮组以左】才判 HTCAPTION,那里的页面
	// DOM 收不到鼠标事件:排除区算宽了会把钮判进拖拽带(点了只拖窗),算窄了
	// 钮和空档都正常。按钮文字/数量/宽度一改,这里和 layers.css 要一起改。
	// (四边缩放是真 NC 边框,系统处理;主拖动路径另有 JS 的 mousedown→
	// mcWinDrag,见 titlebar.js,这条命中带只是兜底。)
	tbLogicalH  = 40
	tbAppW      = 0   // v3.3: 三点带下线,左侧整段并入拖拽带
	tbControlsW = 184 // v3.8: 右上钮组 4×46(主题/min/max/close),与 layers.css --tb-ctl-w 同步

	// WebView2 的 controller 是异步初始化的,子窗口要等一会儿才出现,
	// 所以先用定时器兜底扫,挂上就停表(之后靠 WM_PARENTNOTIFY / WM_SIZE 复查)。
	childHookTimer = 0x4D43
	childHookMs    = 300
	childHookTries = 200 // ≈60s,慢启动也够了
)

// GWLP_WNDPROC as a uintptr (negative index wraps around). The int64
// variable forces a runtime conversion: a plain uintptr(-4) constant would
// be a compile error.
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
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi") // Win10 1607+
	procIsZoomed               = user32.NewProc("IsZoomed")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procDwmExtendFrame         = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmExtendFrameIntoClientArea")
)

type winRect struct{ l, t, r, b int32 }
type ncCalcParams struct{ rgrc [3]winRect }

type framelessCtx struct {
	hwnd   uintptr // go-webview2 的根窗口
	orig   uintptr // 根窗口的原始 wndproc(go-webview2 的)
	wv     webview2.WebView
	zoomed bool
	cbPtr  uintptr // keep the wndproc callback alive for the GC

	// WebView2 的渲染子窗口(Chrome_WidgetWin_*)→ 它们各自的原始 wndproc。
	// 见文件头说明:子窗口必须一起子类化,否则 root 收不到 WM_NCHITTEST。
	// 只在 UI 线程(消息循环)里读写,不需要加锁。
	children  map[uintptr]uintptr // 子窗口 hwnd → 它自己的原始 wndproc
	hookTries int
}

// single window per process (the desktop build holds a single-instance mutex)
var framelessState *framelessCtx

// installFrameless removes the native title bar and wires the JS binds used
// by the page-drawn title bar. Call right after the window is created.
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

	// 摘掉 WS_CAPTION:非最大化时 DefWindowProc 的 NCCALCSIZE 不再给标题栏留
	// 高度,但 WS_THICKFRAME 的缩放边框保留 —— 那是真 NC 区,四边/角缩放全是
	// 系统原生逻辑(见文件头 2)。FRAMECHANGED 让窗口按新样式重算非客户区。
	st, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyleUP)
	if st&wsCaption != 0 {
		procSetWindowLongPtrW.Call(hwnd, gwlStyleUP, st&^uintptr(wsCaption))
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
			swpNoSize|swpNoMove|swpNoZOrder|swpFrameChanged)
	}

	// Extend the DWM glass over the whole window: keeps Win11 rounded
	// corners and the shadow even though the client covers everything.
	// (v3.7:「透视桌面/Mica」实验已回退 —— 用户定稿两框不透明风,不再动背景。)
	m := [...]int32{-1, -1, -1, -1}
	procDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&m[0])))

	// Win11: 显式要求窗口圆角(DWMWCP_ROUND)。摘了 WS_CAPTION 的窗口默认策略
	// 可能给方角;页面侧(layers.css ⑩)的 body 18px 圆角壳靠这道 OS 裁剪才
	// 能四角干净。Win10 无此属性,失败就失败 —— 圆弧仍由页面侧负责,不会更糟。
	pref := uint32(2) // DWMWCP_ROUND
	procDwmSetWindowAttr.Call(hwnd, dwmwaCornerPref, uintptr(unsafe.Pointer(&pref)), 4)

	// 挂钩渲染子窗口。此时 controller 多半还没建好子窗口,挂不上就靠定时器。
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
	// 拖拽带的主拖动路径:页面 mousedown 时调进来。此刻鼠标捕获在 webview 子
	// 窗口手里(它是命中窗口的 client),ReleaseCapture 放掉,再给 root 补一发
	// WM_NCLBUTTONDOWN(HTCAPTION) —— DefWindowProc 从此接管,进系统自带的
	// SC_MOVE 模态循环。Electron/Tauri/Wails 的 frameless 拖动同款。
	_ = w.Bind("mcWinDrag", func() {
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
	})
	slog.Info("desktop: frameless title bar installed")
}

func zoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

// framelessWndproc 是根窗口和 WebView2 子窗口共用的子类化 wndproc:同一个回调
// 地址挂在所有相关窗口上,按 hwnd 分派,各自把不关心的消息交回自己的原始 wndproc。
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

// frameRootProc handles the messages the borderless frame needs on the root.
func frameRootProc(ctx *framelessCtx, hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmNCCalcSiz:
		if wp != 0 {
			if !zoomed(hwnd) {
				// 非最大化:回落 DefWindowProc。WS_CAPTION 已摘,默认算法
				// 只留四周 WS_THICKFRAME 缩放边框做真 NC —— 系统原生 resize。
				r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
				return r
			}
			// 最大化:整窗皆 client,只内缩掉屏幕外那条隐形缩放边框,
			// 内容才不被裁。 (lp holds a *NCCALCSIZE_PARAMS as a
			// uintptr; recover it without tripping go vet.)
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
		// 缩放边/角优先,其次顶部拖拽条;都不命中才回落原始判定。
		if h := frameHitTest(hwnd, lParamX(lp), lParamY(lp)); h != 0 {
			return h
		}
	case wmParentNotify:
		switch wp & 0xffff {
		case wmnCreate:
			// WebView2 的渲染子窗口是异步创建的,创建通知一到就补挂。
			hookBrowserChildren(ctx)
		case wmnDestroy:
			// 子窗口销毁:从表里清掉,句柄即使被系统回收给新窗口也不怕漏挂。
			delete(ctx.children, uintptr(wp>>16))
		}
	case wmTimer:
		if wp == childHookTimer {
			ctx.hookTries++
			if hookBrowserChildren(ctx) > 0 {
				procKillTimer.Call(hwnd, childHookTimer)
			} else if ctx.hookTries >= childHookTries {
				// 等够了还是什么都没挂上:停表(之后 WM_PARENTNOTIFY / WM_SIZE 仍会复查)。
				procKillTimer.Call(hwnd, childHookTimer)
				if len(ctx.children) == 0 {
					slog.Warn("desktop: 始终没等到 WebView2 渲染子窗口,边缘缩放/标题栏拖动可能无效")
				}
			}
		}
	case wmSize:
		hookBrowserChildren(ctx) // 子窗口可能被重建,顺手复查(幂等且很轻)
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

// frameChildProc 处理 WebView2 渲染子窗口:只需要在 WM_NCHITTEST 里把命中到
// 边缘/拖拽条的点用 HTTRANSPARENT 让给父窗口,其余消息原样转发。
func frameChildProc(ctx *framelessCtx, hwnd, orig, msg, wp, lp uintptr) uintptr {
	if msg == wmNCHitTest {
		r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wp, lp)
		// 页面正在拖东西(选文字、拉滑块、拖自绘控件)时子窗口握着鼠标捕获,
		// 这时别把命中抢走,否则拖动会被边缘判定打断。
		if cap_, _, _ := procGetCapture.Call(0); cap_ != 0 && cap_ != ctx.hwnd {
			return r
		}
		if r == htClient && frameHitTest(ctx.hwnd, lParamX(lp), lParamY(lp)) != 0 {
			// 子窗口自己不认这些码(那样 size/move 循环会跑到子窗口身上),
			// 交给下面的 root 去返回 HTLEFT/HTCAPTION。
			return htTransparent
		}
		return r
	}
	r, _, _ := procCallWindowProcW.Call(orig, hwnd, msg, wp, lp)
	return r
}

// hookBrowserChildren 把 wndproc 挂到 WebView2 的渲染子窗口上,返回本次新挂的个数。
// 用 GetWindow(GW_CHILD/GW_HWNDNEXT)递归下钻而不用 EnumChildWindows,是为了不再
// 造一个 Go 回调(syscall.NewCallback 只能在主线程创建,而这里也会在定时器里被调)。
// 幂等:挂过的窗口跳过。
func hookBrowserChildren(ctx *framelessCtx) int {
	if ctx.cbPtr == 0 {
		return 0
	}
	return hookChildTree(ctx, ctx.hwnd, 0)
}

func hookChildTree(ctx *framelessCtx, parent uintptr, depth int) int {
	if depth > 4 { // WebView2 正常只有一层,防环兜底
		return 0
	}
	n := 0
	child, _, _ := procGetWindow.Call(parent, gwChild)
	for child != 0 {
		if class := childClass(child); browserClass(class) {
			// 幂等只看表,【不能】拿 GetWindowLongPtr(GWL_WNDPROC) 判"挂没挂":
			// 对 legacy 子类化它按文档返回【原始】窗口过程,拿它和 cb 比会永远
			// 不等 → 反复 SetWindowLongPtr,orig 被写成本回调,消息一进来就
			// CallWindowProc(cb) 无限递归爆栈。销毁清理靠 WM_PARENTNOTIFY
			// (WM_DESTROY),WebView2 重建窗口后自然会被重新扫到。
			if _, hooked := ctx.children[child]; !hooked {
				orig, _, _ := procSetWindowLongPtrW.Call(child, gwlWndprocUP, ctx.cbPtr)
				if orig == 0 {
					// 设置失败:留个可用的转发目标,别把消息吞了。
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

// browserClass:WebView2/Chromium 的窗口族(实测运行中的进程,鼠标命中的是
// 最深的 RenderWidgetHostHWND —— 让位链要通,每一层都得挂)。
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

// lParamX / lParamY 解 WM_NCHITTEST 的坐标,等价于 GET_X_LPARAM / GET_Y_LPARAM:
// 两个有符号 16 位 short,【物理】屏幕坐标。多屏时坐标可为负,绝不能按无符号取。
func lParamX(lp uintptr) int32 { return int32(int16(uint16(lp & 0xffff))) }
func lParamY(lp uintptr) int32 { return int32(int16(uint16((lp >> 16) & 0xffff))) }

// frameHitTest 判断屏幕点 (gx,gy) 是否落在 client 顶部的拖拽带上(HTCAPTION),
// 返回 0 表示仍是 client。四边缩放不经过这里 —— 那是系统 NC 边框的原生活。
//
// 关键点:gx/gy 是物理屏幕坐标,先按【窗口自己的 DPI】换算,再换算到【client】
// 坐标系(client 比窗口矩形小一圈缩放边框;最大化时是屏幕外的隐形边框 inset,
// 见 wmNCCalcSiz),才能和 40/282 这些常量(=页面 CSS px)比。
// root 和它的子窗口都调用本函数、都以 root 的窗口矩形为基准,两边判定必然一致。
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
		// 最大化:窗口矩形四周多出屏幕外的隐形缩放边框,client 按同样的量
		// 内缩(见 wmNCCalcSiz),这里同步扣掉才对得上看得见的窗口。
		inset := float64(borderInset(root)) / scale
		x -= inset
		y -= inset
		w = w/scale - 2*inset
		h = h/scale - 2*inset
		return hitTestFrame(x, y, w, h, true)
	}

	// 非最大化:四周是【真】NC 缩放边框(DefWindowProc 的 NCCALCSIZE 留的),
	// client 用 GetClientRect 拿,换算成 client 坐标后只有顶部拖拽带需要判。
	var rcC winRect
	if r, _, _ := procGetClientRect.Call(root, uintptr(unsafe.Pointer(&rcC))); r == 0 {
		return 0
	}
	// 无 caption 时左右上下边框各占一半差值(顶部没有标题栏高度,四边近似等宽)。
	x -= float64(int32(rc.r)-int32(rc.l)-int32(rcC.r)) / 2 / scale
	y -= float64(int32(rc.b)-int32(rc.t)-int32(rcC.b)) / 2 / scale
	return hitTestFrame(x, y, float64(int32(rcC.r))/scale, float64(int32(rcC.b))/scale, false)
}

// hitTestFrame 是换算之后的纯计算:x/y 是【client】左上角为原点的逻辑像素,
// w/h 是 client 逻辑宽高。拆成纯函数是为了脱离真实窗口跑测试。
// 非最大化与最大化的差别只剩注释意义上的 —— 边框扣除已经在 frameHitTest 里
// 按两种形态各自做完了,这里只认 client 坐标。
func hitTestFrame(x, y, w, h float64, zoomed bool) uintptr {
	if x < 0 || y < 0 || x >= w || y >= h {
		return 0 // 点不在 client 里(缩放边框/窗外),交回原始判定
	}
	// 拖拽带:client 顶部 tbLogicalH 高,左侧整段可用(tbAppW=0),
	// 右侧让出 Windows 式钮组(min/max/close)—— 钮要能点。
	if y < tbLogicalH && x >= tbAppW && x < w-tbControlsW {
		return htCaption
	}
	return 0
}

// windowScale 是该窗口的缩放倍数。Per-Monitor V2 下必须用 GetDpiForWindow:
// systemDPI() 只有系统/主屏的值,窗口放在缩放不同的副屏上就会整体算错。
func windowScale(hwnd uintptr) float64 {
	if d := windowDPI(hwnd); d >= 96 {
		return float64(d) / 96.0
	}
	if d := systemDPI(); d >= 96 {
		return float64(d) / 96.0
	}
	return 1
}

// boolLit 把 bool 写成 JS 字面量,供 window.__mcZoom 用。
func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// borderInset 返回隐形缩放边框的宽度(物理像素):最大化时窗口矩形比工作区四周
// 各多出这么多,WM_NCCALCSIZE 要按同样的量内缩,命中判定也要按它对齐。
// 用 GetSystemMetricsForDpi 直接按【窗口 DPI】取(Win10 1607+):这样既不会漏缩放,
// 也不会像"GetSystemMetrics × dpi/96"那样在 DPI-aware 进程里可能重复缩放。
// 老系统取不到就退回 GetSystemMetrics × 系统缩放(近似)。
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
