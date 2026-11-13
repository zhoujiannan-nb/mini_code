import { setState } from "./store.js";

// 主题(浅色 / 深色 / 跟随系统)。
// 纯客户端偏好,存 localStorage(浏览器与桌面 WebView2 均持久)。首屏由 main.js 在
// 模块顶部先调 applyTheme():旧版靠 <head> 内联脚本防闪烁,重构后 index.html
// 不再有内联脚本,这是唯一能保住"不闪一下白/黑"的位置(见交付报告的风险说明)。
const LS_THEME_KEY = "mc-theme"; // "light" | "dark" | "system"

export const THEME_UI = {
  light: { icon: "☀", label: "浅色" },
  dark: { icon: "☾", label: "深色" },
  system: { icon: "🖥", label: "跟随系统" },
};

export function readThemeMode() {
  try {
    const m = localStorage.getItem(LS_THEME_KEY);
    return (m === "light" || m === "dark" || m === "system") ? m : "system";
  } catch (e) { return "system"; }
}

export function systemPrefersDark() {
  return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
}

// applyTheme 只做 DOM 属性和原生标题栏同步;界面上的按钮文字由组件从 readThemeMode()
// 读,不再由这里反写 —— 组件重渲染比 querySelector 改写更可靠。
export function applyTheme() {
  const m = readThemeMode();
  const dark = m === "dark" || (m === "system" && systemPrefersDark());
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
  try { if (typeof window.mcSetDarkTitleBar === "function") window.mcSetDarkTitleBar(dark); } catch (e) {}
  // 顶栏/设置页的文案由组件从 store 读,所以这里把模式写回 state,
  // 系统深浅色变化(跟随系统模式)时界面上的名字也会跟着更新。
  setState({ themeMode: m });
  return m;
}

export function setThemeMode(m) {
  try { localStorage.setItem(LS_THEME_KEY, m); } catch (e) {}
  return applyTheme();
}

// 顶栏按钮:浅色 → 深色 → 跟随系统 循环
export function cycleThemeMode() {
  const order = ["light", "dark", "system"];
  const next = order[(order.indexOf(readThemeMode()) + 1) % order.length];
  setThemeMode(next);
  return next;
}

// 主题按钮上的后缀:跟随系统时把当前生效的深浅色也标出来
export function themeLabel(mode) {
  const ui = THEME_UI[mode] || THEME_UI.system;
  const eff = document.documentElement.getAttribute("data-theme") === "dark" ? "深色" : "浅色";
  return ui.label + (mode === "system" ? "(" + eff + ")" : "");
}

// initThemeListener:系统深浅色变化时,跟随系统模式要重新应用。
// 老版本 WebView2 只有 addListener。
export function initThemeListener() {
  if (!window.matchMedia) return;
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const onSysTheme = () => { if (readThemeMode() === "system") applyTheme(); };
  if (mq.addEventListener) mq.addEventListener("change", onSysTheme);
  else if (mq.addListener) mq.addListener(onSysTheme);
}
