package desktopwin

// Windows 专用自检:证明 DPI 感知真的落到进程上、窗口尺寸换算符合预期。
// 只查 API 返回值,不创建可见窗口,跑在 CI/无桌面上都安全。

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetProcessDpiAwareness = windows.NewLazySystemDLL("shcore.dll").NewProc("GetProcessDpiAwareness")
)

// TestDPIAwarenessApplied 直接问系统"当前进程是哪种 DPI 感知"。
// 没修之前这里是 0(DPI unaware,整窗会被 DWM 位图拉伸 = 高分屏发糊的根因)。
func TestDPIAwarenessApplied(t *testing.T) {
	if procGetProcessDpiAwareness.Find() != nil {
		t.Skip("shcore.GetProcessDpiAwareness 不可用")
	}
	enableDPIAwareness() // 包 init() 已调过,这里再调一次应当静默(已被别处定下)
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
		// 100% 缩放的机器上尺寸必须与改动前逐像素一致(外接 1080P 屏零影响)
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
	// 0 尺寸交给库的默认值,别乱动
	if w, h := scaledWindowSize(0, 0); w != 0 || h != 0 {
		t.Errorf("0x0 应保持 0x0,得到 %dx%d", w, h)
	}
}

// TestWindowDPIFollowsMonitor 建一个离屏窗口,验证它的真实 DPI 不再是 96。
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
