// worker 下拉(切换)。菜单必须挂在 body 层:aside 带 backdrop-filter,会把自己
// 变成后代 position:fixed 的包含块 —— 放在侧栏里就会被侧栏右缘剪掉。
// App 把它渲染成 <aside> 的兄弟节点,这个文件只负责内容与视口夹紧。
import { h } from "preact";
import { useLayoutEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore } from "../lib/store.js";
import { getState } from "../lib/store.js";
import { switchWorker, workerBuckets, workerBucketId } from "../lib/actions.js";

const html = htm.bind(h);

// 固定的「其他」是一格虚线框(它不是工作线,是兜底档),真 worker 一律文件夹。
// 用内联 SVG 不用 emoji —— emoji 在不同字体下大小/基线全飘,颜色也没法跟主题走。
const WK_ICON_OTHER = "M4.2 4.2h15.6v15.6H4.2z";
const WK_ICON_FOLDER = "M4 7.2A2.2 2.2 0 0 1 6.2 5h3.4l1.8 2.2H17.8A2.2 2.2 0 0 1 20 9.4v7.4A2.2 2.2 0 0 1 17.8 19H6.2A2.2 2.2 0 0 1 4 16.8V7.2Z";

export function wkIcon(id) {
  const isOther = id === "w-other";
  return html`<svg class="wk-menu-ico" viewBox="0 0 24 24" width="16" height="16"
    fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d=${isOther ? WK_ICON_OTHER : WK_ICON_FOLDER} stroke-dasharray=${isOther ? "3.6 3" : null} />
  </svg>`;
}

// worker 菜单的状态点:颜色语义与会话列表一致 —— 黄(呼吸)=还有会话在跑,
// 绿=这个区的事都跑完了。没有会话就不上色,但格子照样占住,否则数量药丸那列会漂。
export function wkDot(b) {
  const busy = b.active || 0;
  const cls = busy > 0 ? "running" : ((b.sessions || 0) > 0 ? "done" : "none");
  return html`<span class=${"wk-menu-dot " + cls} title=${busy > 0
    ? busy + " 个会话执行中"
    : ((b.sessions || 0) > 0 ? "没有会话在跑" : "还没有会话")}></span>`;
}

// 摆放下拉:按视口夹一次上下左右,并把上缘的尖角挪回锚点下方。
// (名字沿用旧实现,行为不变:窄窗口、贴近屏幕底部时都不会被切。)
export function placeWorkerMenu(menu, anchor) {
  const r = anchor && anchor.getBoundingClientRect
    ? anchor.getBoundingClientRect()
    : (anchor || { left: 12, bottom: 120, width: 200 });
  const w = menu.offsetWidth, h = menu.offsetHeight;
  const left = Math.max(8, Math.min(r.left, Math.max(8, window.innerWidth - w - 8)));
  const top = Math.max(8, Math.min(r.bottom + 4, Math.max(8, window.innerHeight - h - 8)));
  menu.style.left = left + "px";
  menu.style.top = top + "px";
  const tip = r.left + r.width / 2 - left - 5; // ::before 是 11px 宽的小方块
  menu.style.setProperty("--wk-arrow-x", Math.max(20, Math.min(tip, Math.max(20, w - 32))) + "px");
}

// openWorkerMenu 生成菜单内容(名字/状态点/数量/勾)。由 WorkerMenu 在渲染时调用。
// 条上已经写着 worker | 其他 · 3,菜单里不再重复一行"切换 Worker"小标题。
export function openWorkerMenu(anchor) {
  const id = workerBucketId();
  return workerBuckets().map((w) => {
    const cur = w.worker_id === id;
    return html`<div class=${"wk-menu-item" + (cur ? " cur" : "")} data-id=${w.worker_id}
      key=${w.worker_id} title=${w.name}
      onClick=${() => switchWorker(w.worker_id)}>
      ${wkIcon(w.worker_id)}
      <span class="wk-menu-name">${w.name}</span>
      ${wkDot(w)}
      <span class="wk-menu-n">${w.sessions || 0}</span>
      <span class="wk-menu-tick">${cur ? "✓" : ""}</span>
    </div>`;
  });
}

export function WorkerMenu() {
  const open = useStore((s) => s.workerMenuOpen);
  const anchor = useStore((s) => s.workerMenuAnchor);
  const ref = useRef(null);
  useLayoutEffect(() => {
    if (open && ref.current) placeWorkerMenu(ref.current, anchor);
  }, [open, anchor]);
  if (!open) return html`<div class="worker-menu" id="worker-menu"></div>`;
  return html`<div class="worker-menu open" id="worker-menu" ref=${ref}>
    ${openWorkerMenu(anchor)}
  </div>`;
}
