package desktopwin

// DPI 感知:高分屏(2K/4K 笔记本 + 150%/200% 缩放)清晰度的关键。
//
// 为什么必须我们自己做:exe 里没有嵌 manifest(rsrc_windows.syso 只装了图标),
// vendored 的 jchv/go-webview2 也从不调 SetProcessDpiAwareness*,于是进程默认是
// DPI-unaware —— Windows 先按 96dpi 画好整个窗口,再让 DWM 把它当位图放大到面板
// 物理像素。200% 缩放的 2K 屏上就是硬拉 2 倍,字和图标全糊;而外接 1080P 屏是
// 100% 缩放,拉伸系数 1、1:1 直出,反而锐利。这就是"笔记本糊、外接屏清楚"的全部原因。
//
// 声明成 Per-Monitor V2 之后,窗口拿到真实 DPI,WebView2 按物理像素渲染,两种屏都锐利,
// 而且窗口在多屏之间拖动时 DPI 会跟着变(不像 System-aware 那样锁定在主屏 DPI)。
//
// 注意调用时机:必须在**本进程创建第一个窗口之前**,晚调直接返回失败并静默无效。
// 所以放在包级 init() —— Go 保证包初始化早于 main(),那时还没有任何 HWND。

import (
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPI_AWARENESS_CONTEXT 是几个负数哨兵值(不是句柄),转成 uintptr 只能按位取反写。
const (
	dpiCtxSystemAware    = ^uintptr(1) // -2
	dpiCtxPerMonitor     = ^uintptr(2) // -3
	dpiCtxPerMonitorV2   = ^uintptr(3) // -4,Win10 1703+
	processPerMonitorDPI = 2           // shcore 的 PROCESS_PER_MONITOR_DPI_AWARE

	spiGetWorkArea = 0x0030
	logPixelsX     = 88 // GetDeviceCaps 的 LOGPIXELSX
)

var (
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext") // Win10 1703+
	procSetProcessDpiAwareness    = windows.NewLazySystemDLL("shcore.dll").NewProc("SetProcessDpiAwareness")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware") // 最老的兜底
	procGetDpiForSystem           = user32.NewProc("GetDpiForSystem")    // Win10 1607+
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	procSystemParametersInfoW     = user32.NewProc("SystemParametersInfoW")
	procGetDC                     = user32.NewProc("GetDC")
	procReleaseDC                 = user32.NewProc("ReleaseDC")
	gdi32GetDeviceCaps            = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetDeviceCaps")
)

func init() {
	enableDPIAwareness()
}

// enableDPIAwareness 把进程设成 Per-Monitor V2,老系统逐级回退。
// 只在"还没有任何窗口"时有效,调用晚了系统直接返回 FALSE(错误码 ERROR_ACCESS_DENIED),
// 那时就别硬来:说明 awareness 已经被别处定下(比如以后加了 manifest),沿用即可。
func enableDPIAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		for _, ctx := range []uintptr{dpiCtxPerMonitorV2, dpiCtxPerMonitor, dpiCtxSystemAware} {
			if r, _, _ := procSetProcessDpiAwarenessCtx.Call(ctx); r != 0 {
				return
			}
		}
		// 三个都失败通常等于"已经设置过",不动它。
		if err := windows.GetLastError(); err != nil && err != windows.ERROR_ACCESS_DENIED {
			slog.Warn("desktop: SetProcessDpiAwarenessContext 失败", "err", err)
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDPI); r == 0 { // S_OK
			return
		}
	}
	_, _, _ = procSetProcessDPIAware.Call() // 最后兜底:至少别被位图拉伸
}

// systemDPI 返回系统 DPI(96 = 100%)。GetDpiForSystem 在更早的系统上没有,退回 GDI。
func systemDPI() uint32 {
	if procGetDpiForSystem.Find() == nil {
		if r, _, _ := procGetDpiForSystem.Call(); r >= 96 {
			return uint32(r)
		}
	}
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 96
	}
	dpi, _, _ := gdi32GetDeviceCaps.Call(hdc, logPixelsX)
	_, _, _ = procReleaseDC.Call(0, hdc)
	if dpi < 96 {
		return 96
	}
	return uint32(dpi)
}

// windowDPI 是查询某个窗口的真实 DPI(诊断/自检用)。
func windowDPI(hwnd uintptr) uint32 {
	if procGetDpiForWindow.Find() != nil {
		return 0
	}
	r, _, _ := procGetDpiForWindow.Call(hwnd)
	return uint32(r)
}

// workAreaSize 返回主显示器工作区(物理像素,已扣任务栏)。
func workAreaSize() (uint, uint) {
	var rect [4]int32
	if r, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rect[0])), 0); r == 0 {
		return 0, 0
	}
	w, h := rect[2]-rect[0], rect[3]-rect[1]
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	return uint(w), uint(h)
}

// scaledWindowSize 把"设计尺寸"(逻辑像素)换算成物理像素,并按工作区夹住。
//
// 这一步不能省:进程一旦 DPI-aware,CreateWindowExW 收的就是物理像素。以前 unaware
// 时系统把 1280x800 虚拟像素又拉伸 2 倍,看着"正好满屏";直接改成 DPI-aware 而不换算,
// 窗口在 200% 屏上就只有半个屏幕那么大,像是"界面突然变小"。
// 100% 缩放的屏幕 scale=1,尺寸与改动前完全一致(外接屏零影响)。
func scaledWindowSize(logicalW, logicalH uint) (uint, uint) {
	if logicalW == 0 || logicalH == 0 {
		return logicalW, logicalH
	}
	scale := float64(systemDPI()) / 96.0
	w := uint(float64(logicalW)*scale + 0.5)
	h := uint(float64(logicalH)*scale + 0.5)
	if aw, ah := workAreaSize(); aw > 0 && ah > 0 {
		// 留出 10% 边距,别让窗口贴死屏幕边缘导致任务栏挡住输入框
		if maxW, maxH := aw*9/10, ah*9/10; w > maxW || h > maxH {
			if w > maxW {
				w = maxW
			}
			if h > maxH {
				h = maxH
			}
		}
	}
	return w, h
}
