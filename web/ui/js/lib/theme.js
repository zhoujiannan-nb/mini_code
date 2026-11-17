import { setState } from "./store.js";

const LS_THEME_KEY = "mc-theme"; 

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

export function applyTheme() {
  const m = readThemeMode();
  const dark = m === "dark" || (m === "system" && systemPrefersDark());
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
  try { if (typeof window.mcSetDarkTitleBar === "function") window.mcSetDarkTitleBar(dark); } catch (e) {}
  setState({ themeMode: m });
  return m;
}

export function setThemeMode(m) {
  try { localStorage.setItem(LS_THEME_KEY, m); } catch (e) {}
  return applyTheme();
}

export function cycleThemeMode() {
  const order = ["light", "dark", "system"];
  const next = order[(order.indexOf(readThemeMode()) + 1) % order.length];
  setThemeMode(next);
  return next;
}

export function themeLabel(mode) {
  const ui = THEME_UI[mode] || THEME_UI.system;
  const eff = document.documentElement.getAttribute("data-theme") === "dark" ? "深色" : "浅色";
  return ui.label + (mode === "system" ? "(" + eff + ")" : "");
}

export function initThemeListener() {
  if (!window.matchMedia) return;
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const onSysTheme = () => { if (readThemeMode() === "system") applyTheme(); };
  if (mq.addEventListener) mq.addEventListener("change", onSysTheme);
  else if (mq.addListener) mq.addListener(onSysTheme);
}
