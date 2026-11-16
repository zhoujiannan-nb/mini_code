// 桌面无边框模式的自绘标题条(Windows 式右上窗口钮)。
//
// 原生标题栏已被 Win32 壳去掉(desktop/windows/frameless.go),窗口按钮通过
// mcWin* bind 回调原生。检测方式就是这些全局函数存在与否 —— 纯浏览器(web
// 模式)里它们不存在,标题条不渲染,设置/主题入口留在侧栏与设置弹窗。
//
// 布局(v3.8):左=E7 徽标 + Mini Code(品牌行从侧栏上提进标题条,侧栏
// .brand 由 layers.css 在 frameless 下隐藏),中=干净的拖拽带(整段),
// 右=主题 / 最小化 / 最大化 / 关闭,46px 一方、关闭悬停红。钮组宽 4×46=184,
// Go 侧 tbControlsW 与 CSS --tb-ctl-w 必须同步 —— 那是命中测试 HTCAPTION 的
// 排除区,主题钮落在排除区里才点得到(落在拖拽带上就会被系统当标题栏按下)。
//
// 拖动:不靠 Win32 命中穿透(WebView2 四层子窗口的 HTTRANSPARENT 让位链实测
// 走不通),用 mousedown → mcWinDrag 的原生做法 —— ReleaseCapture +
// WM_NCLBUTTONDOWN(HTCAPTION),Electron/Tauri/Wails 同款。DOM 覆盖整个
// client,mousedown 必然进 DOM,由页面告诉原生"这下的是一次标题栏按下"。
// 双击拖拽带 = 最大化/还原(原生双击语义在接管后不会自己来,这里补上)。
// v3.2:标题条不再是 position:fixed 的浮层 —— 它作为 body(窗口圆角壳)常规流
// 的第一格参与 flex 布局,四角由壳的 border-radius 裁切,背景不再溢出。
//
// 状态同步:Go 在 WM_SIZE 时调 window.__mcZoom(zoomed),用于最大化钮的 tooltip。
import { setState, toast } from "./store.js";
import { cycleThemeMode, themeLabel, readThemeMode } from "./theme.js";

export function isFrameless() {
  return document.documentElement.classList.contains("frameless");
}

// 窗控钮用内联 SVG 画线,不借字体字符——任何系统不缺字形,粗细跟 currentColor 走。
const CAP_ICONS = {
  min: '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="M1.5 6h9" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  max: '<svg viewBox="0 0 12 12" aria-hidden="true"><rect x="1.7" y="1.7" width="8.6" height="8.6" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  restore: '<svg viewBox="0 0 12 12" aria-hidden="true"><rect x="1.2" y="3.4" width="6.6" height="6.6" stroke="currentColor" stroke-width="1.1" fill="none"/><path d="M3.6 3.4V1.9h6.5v6.5H8.4" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  close: '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="m2 2 8 8M10 2l-8 8" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
};

// 三态主题图标:与侧栏品牌行那套同源(描线、跟 currentColor)。
const THEME_ICONS = {
  light: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4.2"/><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M18.5 5.5l-1.7 1.7M7.2 16.8l-1.7 1.7"/></svg>',
  dark: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 13.5A8.5 8.5 0 1 1 10.5 4 6.8 6.8 0 0 0 20 13.5Z"/></svg>',
  system: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="4.5" width="18" height="12.5" rx="2"/><path d="M9 20.5h6"/></svg>',
};

// E7「卫星穿环」mini 脸(与 .ico / 侧栏 BrandMark 同款几何;渐变 id 用 tt-*
// 前缀,别和页面其它 E7 实例撞)。26px 高,正好坐在 40px 标题条里。
const BRAND_E7 =
  '<svg class="tb-e7" viewBox="-26 64 564 384" fill="none" aria-hidden="true">' +
  '<defs>' +
  '<linearGradient id="tt-g" x1="20" y1="343" x2="483" y2="213" gradientUnits="userSpaceOnUse">' +
  '<stop offset="0" stop-color="#5ec5ff"/><stop offset="1" stop-color="#a06bff"/></linearGradient>' +
  '<radialGradient id="tt-sat" cx="0.5" cy="0.5" r="0.5">' +
  '<stop offset="0" stop-color="#ffffff"/><stop offset="0.35" stop-color="#9fd0ff"/>' +
  '<stop offset="1" stop-color="#4f7dff" stop-opacity="0"/></radialGradient></defs>' +
  '<ellipse cx="256" cy="278" rx="236" ry="104" stroke="url(#tt-g)" stroke-opacity="0.38" stroke-width="18" transform="rotate(-16 256 278)"/>' +
  '<rect x="64" y="64" width="384" height="384" rx="88" fill="var(--ink)"/>' +
  '<g transform="translate(256 256) scale(0.66) skewX(-9) translate(-256 -256)">' +
  '<polygon fill="var(--ink-fg)" points="148,356 148,156 212,156 256,244 300,156 364,156 364,356 316,356 316,232 256,320 196,232 196,356"/></g>' +
  '<path d="M482.8 213 A236 104 -16 0 1 29.2 343" stroke="url(#tt-g)" stroke-width="22" stroke-linecap="round"/>' +
  '<circle cx="342" cy="358" r="40" fill="url(#tt-sat)"/><circle cx="342" cy="358" r="15" fill="#f2f8ff"/></svg>';

export function initTitlebar() {
  if (typeof window.mcWinMin !== "function") return; // web 模式:什么都不做
  document.documentElement.classList.add("frameless");

  const bar = document.createElement("div");
  bar.id = "mc-titlebar";
  bar.innerHTML =
    `<span id="mc-title-brand" class="tb-brand">${BRAND_E7}<span class="tb-brand-name">Mini Code</span></span>` +
    `<span class="tb-flex"></span>` +
    `<div class="tb-dots">` +
    `<button id="tb-theme" class="tb-dot tb-dot-t" title="切换主题"></button>` +
    `<button id="tb-min" class="tb-dot tb-dot-n" title="最小化">${CAP_ICONS.min}</button>` +
    `<button id="tb-max" class="tb-dot tb-dot-z" title="最大化">${CAP_ICONS.max}</button>` +
    `<button id="tb-close" class="tb-dot tb-dot-x" title="关闭(窗口隐藏到托盘,任务继续)">${CAP_ICONS.close}</button>` +
    `</div>`;
  document.body.prepend(bar);

  const byId = (id) => bar.querySelector("#" + id);

  // 主题钮:三态循环(浅色→深色→跟随系统),图标即时反映当前态,toast 报模式名
  // —— 与侧栏品牌行那颗行为一致(那颗在 frameless 下随 .brand 一起隐藏)。
  const themeBtn = byId("tb-theme");
  const paintTheme = (mode) => {
    themeBtn.innerHTML = THEME_ICONS[mode] || THEME_ICONS.system;
    themeBtn.title = "切换主题:浅色 / 深色 / 跟随系统(当前:" + themeLabel(mode) + ")";
  };
  paintTheme(readThemeMode());
  themeBtn.addEventListener("click", () => {
    const next = cycleThemeMode();
    setState({ themeMode: next });
    paintTheme(next);
    toast("主题:" + themeLabel(next), "ok");
  });

  byId("tb-min").addEventListener("click", () => window.mcWinMin());
  byId("tb-max").addEventListener("click", () => window.mcWinMax());
  byId("tb-close").addEventListener("click", () => window.mcWinClose());

  // 拖拽带:空白处的左键按下交给原生进 SC_MOVE 模态循环(见文件头)。
  // 钮组(.tb-dot)上的按下不能接管,否则 click 永远等不到 mousedown 的后续;
  // 品牌标(.tb-brand)不用点,留在拖拽带语义里(按着就能拖窗)。
  bar.addEventListener("mousedown", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    if (e.detail >= 2) return; // 第二击交给 dblclick
    window.mcWinDrag();
  });
  bar.addEventListener("dblclick", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    window.mcWinMax(); // 双击拖拽带:最大化 / 还原
  });

  const maxBtn = byId("tb-max");
  window.__mcZoom = (z) => {
    document.body.classList.toggle("mc-maxed", !!z);
    maxBtn.title = z ? "向下还原" : "最大化";
    maxBtn.innerHTML = z ? CAP_ICONS.restore : CAP_ICONS.max; // 钮图形跟着状态换(□ ⇄ ⧉)
  };
}
