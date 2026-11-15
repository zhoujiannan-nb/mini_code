// 桌面无边框模式的自绘标题条(macOS 风格三点)。
//
// 原生标题栏已被 Win32 壳去掉(desktop/windows/frameless.go),窗口按钮通过
// mcWin* bind 回调原生。检测方式就是这些全局函数存在与否 —— 纯浏览器(web
// 模式)里它们不存在,标题条不渲染,设置/主题入口留在侧栏与设置弹窗。
//
// 布局(v2):最左是 macOS 交通灯三点 —— 红=关闭、黄=最小化、绿=最大化/还原;
// 右侧不再有按钮组,中间整段是干净的拖拽带。设置入口在侧栏导航,主题在设置
// 弹窗的「外观」里选,标题条只留最纯粹的三颗点。
//
// 拖动:不靠 Win32 命中穿透(WebView2 四层子窗口的 HTTRANSPARENT 让位链实测
// 走不通),用 mousedown → mcWinDrag 的原生做法 —— ReleaseCapture +
// WM_NCLBUTTONDOWN(HTCAPTION),Electron/Tauri/Wails 同款。DOM 覆盖整个
// client,mousedown 必然进 DOM,由页面告诉原生"这下的是一次标题栏按下"。
// 双击拖拽带 = 最大化/还原(原生双击语义在接管后不会自己来,这里补上)。
//
// 三点带宽(70px)在 layers.css 定宽,并同步 Go 侧 frameless.go 的 tbAppW ——
// 那是命中测试里 HTCAPTION 判定的排除区,Go 侧算宽了会把三点判进拖拽带
// (点了只拖窗),算窄了三点和空档都正常。
//
// 状态同步:Go 在 WM_SIZE 时调 window.__mcZoom(zoomed),用于绿点 tooltip。

export function isFrameless() {
  return document.documentElement.classList.contains("frameless");
}

export function initTitlebar() {
  if (typeof window.mcWinMin !== "function") return; // web 模式:什么都不做
  document.documentElement.classList.add("frameless");

  const bar = document.createElement("div");
  bar.id = "mc-titlebar";
  bar.innerHTML =
    `<div class="tb-dots">` +
    `<button id="tb-close" class="tb-dot tb-dot-x" title="关闭(窗口隐藏到托盘,任务继续)"><span>✕</span></button>` +
    `<button id="tb-min" class="tb-dot tb-dot-n" title="最小化"><span>−</span></button>` +
    `<button id="tb-max" class="tb-dot tb-dot-z" title="最大化"><span>＋</span></button>` +
    `</div>`;
  document.body.prepend(bar);

  const byId = (id) => bar.querySelector("#" + id);
  byId("tb-min").addEventListener("click", () => window.mcWinMin());
  byId("tb-max").addEventListener("click", () => window.mcWinMax());
  byId("tb-close").addEventListener("click", () => window.mcWinClose());

  // 拖拽带:空白处的左键按下交给原生进 SC_MOVE 模态循环(见文件头)。
  // 三点上的按下不能接管,否则 click 永远等不到 mousedown 的后续。
  bar.addEventListener("mousedown", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    if (e.detail >= 2) return; // 第二击交给 dblclick
    window.mcWinDrag();
  });
  bar.addEventListener("dblclick", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    window.mcWinMax(); // 双击拖拽带:最大化 / 还原
  });

  const maxDot = byId("tb-max");
  window.__mcZoom = (z) => {
    document.body.classList.toggle("mc-maxed", !!z);
    maxDot.title = z ? "向下还原" : "最大化";
  };
}
