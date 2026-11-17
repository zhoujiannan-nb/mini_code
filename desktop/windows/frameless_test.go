package desktopwin


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
		{"右上钮组内(close 位)", w - 20, 20, false, 0},
		{"右上角内沿", w - 1, 0, false, 0},
		{"左侧并入拖拽带", 20, 20, false, htCaption},
		{"右钮组左界外", w - tbControlsW - 1, 20, false, htCaption},
		{"右钮组左界内", w - tbControlsW + 1, 20, false, 0},
		{"正文", 640, 400, false, 0},
		{"底部边缘", 640, h - 1, false, 0},
		{"左侧边框上方(负坐标)", -1, 5, false, 0},
		{"越界右", w, 5, false, 0},
		{"越界下", 5, h, false, 0},
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

func TestBrowserClass(t *testing.T) {
	must := []string{
		"Chrome_WidgetWin_0",
		"Chrome_WidgetWin_1",
		"Chrome_RenderWidgetHostHWND",
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

func TestFrameHitTestClientMapping(t *testing.T) {
	if windowDPI(uintptr(windows.GetDesktopWindow())) == 0 {
		t.Skip("GetDpiForWindow 不可用(早于 Win10 1607)")
	}
	cls, _ := windows.UTF16PtrFromString("Static")
	title, _ := windows.UTF16PtrFromString("mini_code_hittest")
	hwnd, _, err := procCreateWindowExWTest.Call(
		0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		0x00CF0000, 0, 0, 1, 1, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Skipf("CreateWindowExW 失败: %v", err)
	}
	defer procDestroyWindowTest.Call(hwnd)
	if r, _, _ := procMoveWindowTest.Call(hwnd, 200, 150, 1000, 700, 0); r == 0 {
		t.Skip("MoveWindow 失败,隐藏窗口拿不到矩形")
	}
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
	borderX := (pw - int32(rcC.r)) / 2
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
	capProbeX := rc.l + borderX + px((wLogical+tbAppW-tbControlsW)/2)
	top20 := rc.t + borderY + px(20)
	cases := []struct {
		name   string
		gx, gy int32
		want   uintptr
	}{
		{"左外沿(真NC边框)", rc.l + 1, cyPhys, 0},
		{"右外沿(真NC边框)", rc.r - 1, cyPhys, 0},
		{"上外沿(真NC边框)", cxPhys, rc.t + 1, 0},
		{"下外沿(真NC边框)", cxPhys, rc.b - 1, 0},
		{"左上角边框", rc.l + 1, rc.t + 1, 0},
		{"拖拽带中点", capProbeX, top20, htCaption},
		{"拖拽带贴client顶", capProbeX, rc.t + borderY + 1, htCaption},
		{"右上钮组 close 位", rc.r - borderX - px(20), top20, 0},
		{"右上钮组 min 位", rc.r - borderX - px(20) - px(92), top20, 0},
		{"左侧拖拽带(原三点位)", rc.l + borderX + px(20), top20, htCaption},
		{"client顶之上(边框里)", capProbeX, rc.t + borderY/2, 0},
		{"正文中心", cxPhys, cyPhys, 0},
	}
	for _, c := range cases {
		if got := frameHitTest(hwnd, c.gx, c.gy); got != c.want {
			t.Errorf("%s: 屏幕点(%d,%d) 期望 %#x,实际 %#x", c.name, c.gx, c.gy, c.want, got)
		}
	}
}
