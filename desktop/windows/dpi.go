package desktopwin


import (
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	dpiCtxSystemAware    = ^uintptr(1)
	dpiCtxPerMonitor     = ^uintptr(2)
	dpiCtxPerMonitorV2   = ^uintptr(3)
	processPerMonitorDPI = 2

	spiGetWorkArea = 0x0030
	logPixelsX     = 88
)

var (
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness    = windows.NewLazySystemDLL("shcore.dll").NewProc("SetProcessDpiAwareness")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem           = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	procSystemParametersInfoW     = user32.NewProc("SystemParametersInfoW")
	procGetDC                     = user32.NewProc("GetDC")
	procReleaseDC                 = user32.NewProc("ReleaseDC")
	gdi32GetDeviceCaps            = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetDeviceCaps")
)

func init() {
	enableDPIAwareness()
}

func enableDPIAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		for _, ctx := range []uintptr{dpiCtxPerMonitorV2, dpiCtxPerMonitor, dpiCtxSystemAware} {
			if r, _, _ := procSetProcessDpiAwarenessCtx.Call(ctx); r != 0 {
				return
			}
		}
		if err := windows.GetLastError(); err != nil && err != windows.ERROR_ACCESS_DENIED {
			slog.Warn("desktop: SetProcessDpiAwarenessContext 失败", "err", err)
		}
	}
	if procSetProcessDpiAwareness.Find() == nil {
		if r, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDPI); r == 0 {
			return
		}
	}
	_, _, _ = procSetProcessDPIAware.Call()
}

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

func windowDPI(hwnd uintptr) uint32 {
	if procGetDpiForWindow.Find() != nil {
		return 0
	}
	r, _, _ := procGetDpiForWindow.Call(hwnd)
	return uint32(r)
}

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

func scaledWindowSize(logicalW, logicalH uint) (uint, uint) {
	if logicalW == 0 || logicalH == 0 {
		return logicalW, logicalH
	}
	scale := float64(systemDPI()) / 96.0
	w := uint(float64(logicalW)*scale + 0.5)
	h := uint(float64(logicalH)*scale + 0.5)
	if aw, ah := workAreaSize(); aw > 0 && ah > 0 {
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
