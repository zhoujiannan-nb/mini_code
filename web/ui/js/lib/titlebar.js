import { setState, toast } from "./store.js";
import { cycleThemeMode, themeLabel, readThemeMode } from "./theme.js";

export function isFrameless() {
  return document.documentElement.classList.contains("frameless");
}

const CAP_ICONS = {
  min: '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="M1.5 6h9" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  max: '<svg viewBox="0 0 12 12" aria-hidden="true"><rect x="1.7" y="1.7" width="8.6" height="8.6" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  restore: '<svg viewBox="0 0 12 12" aria-hidden="true"><rect x="1.2" y="3.4" width="6.6" height="6.6" stroke="currentColor" stroke-width="1.1" fill="none"/><path d="M3.6 3.4V1.9h6.5v6.5H8.4" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
  close: '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="m2 2 8 8M10 2l-8 8" stroke="currentColor" stroke-width="1.1" fill="none"/></svg>',
};

const THEME_ICONS = {
  light: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4.2"/><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M18.5 5.5l-1.7 1.7M7.2 16.8l-1.7 1.7"/></svg>',
  dark: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 13.5A8.5 8.5 0 1 1 10.5 4 6.8 6.8 0 0 0 20 13.5Z"/></svg>',
  system: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="4.5" width="18" height="12.5" rx="2"/><path d="M9 20.5h6"/></svg>',
};

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
  if (typeof window.mcWinMin !== "function") return; 
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

  bar.addEventListener("mousedown", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    if (e.detail >= 2) return; 
    window.mcWinDrag();
  });
  bar.addEventListener("dblclick", (e) => {
    if (e.button !== 0 || e.target.closest(".tb-dot")) return;
    window.mcWinMax(); 
  });

  const maxBtn = byId("tb-max");
  window.__mcZoom = (z) => {
    document.body.classList.toggle("mc-maxed", !!z);
    maxBtn.title = z ? "向下还原" : "最大化";
    maxBtn.innerHTML = z ? CAP_ICONS.restore : CAP_ICONS.max; 
  };
}
