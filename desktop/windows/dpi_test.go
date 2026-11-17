package desktopwin


import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetProcessDpiAwareness = windows.NewLazySystemDLL("shcore.dll").NewProc("GetProcessDpiAwareness")
)

func TestDPIAwarenessApplied(t *testing.T) {
	if procGetProcessDpiAwareness.Find() != nil {
		t.Skip("shcore.GetProcessDpiAwareness 不可用")
	}
	enableDPIAwareness()
	var awareness int32
	r, _, _ := procGetProcessDpiAwareness.Call(0, uintptr(unsafe.Pointer(&awareness)))
	if r != 0 {
		t.Fatalf("GetProcessDpiAwareness 失败 hr=%#x", uint32(r))
	}
	t.Logf("进程 DPI awareness = %d (0=unaware 1=system 2=per-monitor)", awareness)
	if awareness != processPerMonitorDPI {
		t.Fatalf("期望 per-monitor aware(2),实际 %d —— 高分屏会被系统位图拉伸", awareness)
	}
}

func TestSystemDPIAndScale(t *testing.T) {
	dpi := systemDPI()
	if dpi < 96 || dpi > 384 {
		t.Fatalf("systemDPI 不合理: %d", dpi)
	}
	aw, ah := workAreaSize()
	t.Logf("systemDPI=%d (%.0f%%) 工作区=%dx%d", dpi, float64(dpi)/96*100, aw, ah)
	if aw == 0 || ah == 0 {
		t.Fatal("SystemParametersInfoW(SPI_GETWORKAREA) 失败")
	}
	if dpi == 96 {
		if w, h := scaledWindowSize(1280, 800); w != 1280 || h != 800 {
			gotW, gotH := scaledWindowSize(1280, 800)
			t.Fatalf("100%% 缩放不该改尺寸,得到 %dx%d", gotW, gotH)
		}
		return
	}
}

func TestScaledWindowSize(t *testing.T) {
	dpi := systemDPI()
	scale := float64(dpi) / 96.0
	aw, ah := workAreaSize()

	for _, c := range []struct{ lw, lh uint }{{1280, 800}, {800, 600}, {1920, 1200}} {
		w, h := scaledWindowSize(c.lw, c.lh)
		wantW := uint(float64(c.lw)*scale + 0.5)
		wantH := uint(float64(c.lh)*scale + 0.5)
		if wantW > aw*9/10 {
			wantW = aw * 9 / 10
		}
		if wantH > ah*9/10 {
			wantH = ah * 9 / 10
		}
		if w != wantW || h != wantH {
			t.Errorf("%dx%d @%.0f%% 期望 %dx%d,实际 %dx%d", c.lw, c.lh, scale*100, wantW, wantH, w, h)
		}
		if w > aw || h > ah {
			t.Errorf("%dx%d 超出工作区 %dx%d: %dx%d", c.lw, c.lh, aw, ah, w, h)
		}
		t.Logf("%dx%d 逻辑 → %dx%d 物理", c.lw, c.lh, w, h)
	}
	if w, h := scaledWindowSize(0, 0); w != 0 || h != 0 {
		t.Errorf("0x0 应保持 0x0,得到 %dx%d", w, h)
	}
}

func TestWindowDPIFollowsMonitor(t *testing.T) {
	if windowDPI(uintptr(windows.GetDesktopWindow())) == 0 {
		t.Skip("GetDpiForWindow 不可用(早于 Win10 1607)")
	}
	dpi := windowDPI(uintptr(windows.GetDesktopWindow()))
	t.Logf("桌面窗口 DPI=%d,系统 DPI=%d", dpi, systemDPI())
	if dpi < 96 {
		t.Fatalf("窗口 DPI 异常: %d", dpi)
	}
	if dpi != systemDPI() {
		t.Logf("提示: 窗口 DPI(%d) 与系统 DPI(%d) 不同,属多屏混合缩放场景", dpi, systemDPI())
	}
}
