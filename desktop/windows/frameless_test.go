package desktopwin

// 无边框窗口命中测试的自检。分三层:
//   - hitTestFrame:纯几何(client 坐标系),表驱动:拖拽带/左右按钮带/最大化/越界。
//   - browserClass:钩子覆盖面 —— RenderWidgetHostHWND 必须在名单里,
//     它是鼠标实际命中的最深窗口,漏了它整条让位链就断(回归哨兵)。
//   - frameHitTest:拿真实窗口走「物理屏幕坐标 → client 逻辑坐标」换算链,
//     并且验证摘掉 WS_CAPTION 后,窗口外沿(真 NC 缩放边框)不再被我们接管。
// 只建一个不显示的窗口,不画任何东西,无桌面/CI 上失败就 Skip。

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateWindowExWTest = user32.NewProc("CreateWindowExW")
	procDestroyWindowTest   = user32.NewProc("DestroyWindow")
	procMoveWindowTest      = user32.NewProc("MoveWindow")
)

func TestHitTestFrame(t *testing.T) {
	const (
		w = 1280.0
		h = 800.0
	)
	for _, c := range []struct {
		name string
		x, y float64
		zoom bool
		want uintptr
	}{
		{"拖拽条", 300, 20, false, htCaption},
		{"拖拽条贴顶", 300, 0, false, htCaption},
		{"拖拽条贴底部", 300, tbLogicalH - 1, false, htCaption},
		{"拖拽条下方一格", 300, tbLogicalH, false, 0},
		{"拖拽条左起点", tbAppW, 20, false, htCaption},
		{"右上钮组内(close 位)", w - 20, 20, false, 0},  // v3.3:右上 Windows 钮组是排除区
		{"右上角内沿", w - 1, 0, false, 0},
		// v3.3 换防:左侧三点下线整段并入拖拽带(tbAppW=0),右侧钮组
		// (min/max/close)占 [w-138, w)。排除区要算漏了(tbControlsW 退回 0),
		// 钮会被判进 HTCAPTION,点钮变成拖窗 —— 回归哨兵。
		{"左侧并入拖拽带", 20, 20, false, htCaption},
		{"右钮组左界外", w - tbControlsW - 1, 20, false, htCaption},
		{"右钮组左界内", w - tbControlsW + 1, 20, false, 0},
		{"正文", 640, 400, false, 0},
		{"底部边缘", 640, h - 1, false, 0}, // 缩放是系统 NC 的事,hitTest 一律不管
		{"左侧边框上方(负坐标)", -1, 5, false, 0},
		{"越界右", w, 5, false, 0},
		{"越界下", 5, h, false, 0},
		// 最大化:同一套 client 坐标语义(隐形边框已在 frameHitTest 里扣掉)
		{"最大化左上(拖拽带起点)", 0, 0, true, htCaption},
		{"最大化拖拽条", 300, 10, true, htCaption},
		{"最大化右上钮组", w - 20, 10, true, 0},
		{"最大化正文", 640, 400, true, 0},
	} {
		if got := hitTestFrame(c.x, c.y, w, h, c.zoom); got != c.want {
			t.Errorf("%s: (%.1f,%.1f) zoom=%v 期望 %#x,实际 %#x", c.name, c.x, c.y, c.zoom, c.want, got)
		}
	}
}

// TestBrowserClass 钉死钩子覆盖面:诊断工具实测的四层窗口树里,鼠标实际命中的
// 是最深的 Chrome_RenderWidgetHostHWND —— 曾经只挂 Chrome_WidgetWin* 导致
// HTTRANSPARENT 让位链走不到 root,拖边拖顶全灭。这是那条 bug 的回归哨兵。
func TestBrowserClass(t *testing.T) {
	must := []string{
		"Chrome_WidgetWin_0",
		"Chrome_WidgetWin_1",
		"Chrome_RenderWidgetHostHWND", // 上一版漏掉的就是它
		"Intermediate D3D Window",
		"Chrome_Legacy_Window",
	}
	for _, c := range must {
		if !browserClass(c) {
			t.Errorf("browserClass(%q) = false,必须挂钩", c)
		}
	}
	mustNot := []string{"webview", "SysstrayClass", "Static", ""}
	for _, c := range mustNot {
		if browserClass(c) {
			t.Errorf("browserClass(%q) = true,不该挂钩", c)
		}
	}
}

// TestFrameHitTestClientMapping 用真实窗口验证「物理屏幕坐标 → client 逻辑坐标」:
// 窗口按 installFrameless 同款操作摘掉 WS_CAPTION 后,
//   - client 顶部按钮带之间的拖拽带命中 HTCAPTION;
//   - 两侧按钮带(应用/窗口)和窗口最外沿(真 NC 边框)都返回 0。
func TestFrameHitTestClientMapping(t *testing.T) {
	if windowDPI(uintptr(windows.GetDesktopWindow())) == 0 {
		t.Skip("GetDpiForWindow 不可用(早于 Win10 1607)")
	}
	cls, _ := windows.UTF16PtrFromString("Static") // 系统自带类,省去注册
	title, _ := windows.UTF16PtrFromString("mini_code_hittest")
	hwnd, _, err := procCreateWindowExWTest.Call(
		0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		0x00CF0000 /*WS_OVERLAPPEDWINDOW*/, 0, 0, 1, 1, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Skipf("CreateWindowExW 失败: %v", err)
	}
	defer procDestroyWindowTest.Call(hwnd)
	if r, _, _ := procMoveWindowTest.Call(hwnd, 200, 150, 1000, 700, 0); r == 0 {
		t.Skip("MoveWindow 失败,隐藏窗口拿不到矩形")
	}
	// 与 installFrameless 同款:摘 WS_CAPTION 并让 NC 重算。
	st, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyleUP)
	if st&wsCaption != 0 {
		procSetWindowLongPtrW.Call(hwnd, gwlStyleUP, st&^uintptr(wsCaption))
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoZOrder|swpFrameChanged)
	}
	var rc winRect
	if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
		t.Skip("GetWindowRect 失败")
	}
	var rcC winRect
	if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rcC))); r == 0 {
		t.Skip("GetClientRect 失败")
	}
	pw, ph := int32(rc.r-rc.l), int32(rc.b-rc.t)
	if pw < 400 || ph < 300 {
		t.Skipf("隐藏窗口尺寸异常: %dx%d", pw, ph)
	}
	scale := windowScale(hwnd)
	px := func(logical float64) int32 { return int32(logical*scale + 0.5) }
	borderX := (pw - int32(rcC.r)) / 2 // 物理边框宽(无 caption,四边近似等宽)
	borderY := (ph - int32(rcC.b)) / 2
	if borderX <= 0 || borderY <= 0 {
		t.Skipf("摘 caption 后仍没留出 NC 边框: win=%dx%d client=%dx%d", pw, ph, int32(rcC.r), int32(rcC.b))
	}
	wLogical := float64(int32(rcC.r)) / scale
	if wLogical <= tbAppW+tbControlsW+40 {
		t.Skipf("client 逻辑宽度 %.0f 太窄(缩放 %.0f%%)", wLogical, scale*100)
	}
	t.Logf("窗口物理 %dx%d client %dx%d 边框(%d,%d) @%.0f%%", pw, ph, int32(rcC.r), int32(rcC.b), borderX, borderY, scale*100)

	cxPhys := rc.l + pw/2
	cyPhys := rc.t + ph/2
	capProbeX := rc.l + borderX + px((wLogical+tbAppW-tbControlsW)/2) // 拖拽带中点
	top20 := rc.t + borderY + px(20)
	cases := []struct {
		name   string
		gx, gy int32
		want   uintptr
	}{
		{"左外沿(真NC边框)", rc.l + 1, cyPhys, 0}, // 缩放边框:交系统,我们闭嘴
		{"右外沿(真NC边框)", rc.r - 1, cyPhys, 0},
		{"上外沿(真NC边框)", cxPhys, rc.t + 1, 0},
		{"下外沿(真NC边框)", cxPhys, rc.b - 1, 0},
		{"左上角边框", rc.l + 1, rc.t + 1, 0},
		{"拖拽带中点", capProbeX, top20, htCaption},
		{"拖拽带贴client顶", capProbeX, rc.t + borderY + 1, htCaption},
		{"右上钮组 close 位", rc.r - borderX - px(20), top20, 0},      // v3.3:右上钮必须可点
		{"右上钮组 min 位", rc.r - borderX - px(20) - px(92), top20, 0}, //
		{"左侧拖拽带(原三点位)", rc.l + borderX + px(20), top20, htCaption}, // v3.3:左侧整段可拖
		{"client顶之上(边框里)", capProbeX, rc.t + borderY/2, 0},
		{"正文中心", cxPhys, cyPhys, 0},
	}
	for _, c := range cases {
		if got := frameHitTest(hwnd, c.gx, c.gy); got != c.want {
			t.Errorf("%s: 屏幕点(%d,%d) 期望 %#x,实际 %#x", c.name, c.gx, c.gy, c.want, got)
		}
	}
}
