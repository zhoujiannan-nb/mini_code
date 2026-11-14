// 桌面无边框模式的自绘标题条(对标 dsh 的 frameless 窗口)。
//
// 原生标题栏已被 Win32 壳去掉(desktop/windows/frameless.go),窗口按钮通过
// mcWin* bind 回调原生。检测方式就是这些全局函数存在与否 —— 纯浏览器(web
// 模式)里它们不存在,标题条不渲染,设置/主题入口留在顶栏(Topbar.js 里按
// isFrameless() 分流)。
//
// 布局:最左是应用侧两颗中文文字钮(设置 / 主题,各 72px),最右是窗口三颗
// 图标钮(min / max / close,各 46px),中间整段是干净的拖拽带,不放商标
// (商标在侧栏,dsh 同款)。
//
// 拖动:不靠 Win32 命中穿透(WebView2 四层子窗口的 HTTRANSPARENT 让位链实测
// 走不通),用 mousedown → mcWinDrag 的原生做法 —— ReleaseCapture +
// WM_NCLBUTTONDOWN(HTCAPTION),Electron/Tauri/Wails 同款。DOM 覆盖整个
// client,mousedown 必然进 DOM,由页面告诉原生"这下的是一次标题栏按下"。
// 双击拖拽带 = 最大化/还原(原生双击语义在接管后不会自己来,这里补上)。
//
// 两侧按钮带宽(72×2 / 46×3)在 layers.css 定宽,并同步 Go 侧 frameless.go
// 的 tbAppW / tbControlsW —— 那是命中测试里 HTCAPTION 判定的排除区,Go 侧
// 算宽了会把按钮判进拖拽带(点了只拖窗),算窄了按钮正常但空档拖不动。
//
// 状态同步:Go 在 WM_SIZE 时调 window.__mcZoom(zoomed),用于切换
// 「最大化/还原」按钮的图标与 tooltip。
import { openSettings } from "./settings.js";
import { cycleThemeMode, themeLabel } from "./theme.js";
import { getState, setState, subscribe, toast } from "./store.js";

const SVG = {
  min: '<svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.1"><path d="M2 6h8"/></svg>',
  max: '<svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.1"><rect x="2.2" y="2.2" width="7.6" height="7.6" rx="1.2"/></svg>',
  restore: '<svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.1"><rect x="2.2" y="3.6" width="6.4" height="6.2" rx="1"/><path d="M4 3.6V2.6A.6.6 0 0 1 4.6 2h5A.6.6 0 0 1 10.2 2.6v4.2a.6.6 0 0 1-.6.6H9"/></svg>',
  close: '<svg viewBox="0 0 12 12" width="12" height="12" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"><path d="M2.5 2.5l7 7M9.5 2.5l-7 7"/></svg>',
};

// 主题按钮上显示的中文短名(顶栏版的信息量,条高 40px 放不下"跟随系统(浅色)")。
const THEME_SHORT = { light: "浅色", dark: "深色", system: "跟随" };

// isFrameless —— 无边框模式是否生效。顶栏(Topbar.js)用它决定「设置 / 主题」
// 这两颗按钮画在哪里:桌面 → 标题条;浏览器 → 顶栏原位置。
export function isFrameless() {
  return document.documentElement.classList.contains("frameless");
}

export function initTitlebar() {
  if (typeof window.mcWinMin !== "function") return; // web 模式:什么都不做
  document.documentElement.classList.add("frameless");

  const bar = document.createElement("div");
  bar.id = "mc-titlebar";
  bar.innerHTML =
    `<div class="tb-app">` +
    `<button id="tb-settings" class="tb-btn tb-app-btn" title="设置">设置</button>` +
    `<button id="tb-theme" class="tb-btn tb-app-btn" title="切换主题">切换</button>` +
    `</div>` +
    `<div class="tb-ctl">` +
    `<button id="tb-min" class="tb-btn" title="最小化">${SVG.min}</button>` +
    `<button id="tb-max" class="tb-btn" title="最大化">${SVG.max}<span class="tb-restore">${SVG.restore}</span></button>` +
    `<button id="tb-close" class="tb-btn tb-x" title="关闭(窗口隐藏到托盘,任务继续)">${SVG.close}</button>` +
    `</div>`;
  document.body.prepend(bar);

  const byId = (id) => bar.querySelector("#" + id);
  byId("tb-min").addEventListener("click", () => window.mcWinMin());
  byId("tb-max").addEventListener("click", () => window.mcWinMax());
  byId("tb-close").addEventListener("click", () => window.mcWinClose());

  // 拖拽带:空白处的左键按下交给原生进 SC_MOVE 模态循环(见文件头)。
  // 按钮上的按下不能接管,否则 click 永远等不到 mousedown 的后续。
  bar.addEventListener("mousedown", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-btn")) return;
    if (e.detail >= 2) return; // 第二击交给 dblclick
    window.mcWinDrag();
  });
  bar.addEventListener("dblclick", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-btn")) return;
    window.mcWinMax(); // 双击拖拽带:最大化 / 还原
  });

  // 「设置」与「切换主题」沿用原来的那两条链路(lib/settings.js 的 openSettings、
  // lib/theme.js 的 cycleThemeMode),它们本来就是纯 lib 函数、不挂在组件 state 上,
  // 所以这里直接 import 复用,不必再 dispatch 自定义事件绕一圈。
  byId("tb-settings").addEventListener("click", () => openSettings());

  const themeBtn = byId("tb-theme");
  const syncThemeTip = () => {
    const mode = getState().themeMode || "system";
    themeBtn.title = "切换主题:" + themeLabel(mode);
    themeBtn.textContent = THEME_SHORT[mode] || "跟随";
  };
  themeBtn.addEventListener("click", () => {
    const next = cycleThemeMode();
    setState({ themeMode: next });
    toast("主题:" + themeLabel(next), "ok");
  });
  // 「跟随系统」模式下系统换深浅色时,tooltip 里那个「(浅色/深色)」也要跟着变。
  syncThemeTip();
  subscribe(syncThemeTip);

  const maxBtn = byId("tb-max");
  window.__mcZoom = (z) => {
    document.body.classList.toggle("mc-maxed", !!z);
    maxBtn.title = z ? "向下还原" : "最大化";
  };
}
