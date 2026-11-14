// worker 弹层(「历史会话」行尾的漏斗)。承担两件事:
//   ① 切换列表按哪个工作区分流;② 当前工作区的管理动作(公约/摘要/改名/删除/新建)。
// 菜单必须挂在 body 层:aside 带 backdrop-filter,会把自己
// 变成后代 position:fixed 的包含块 —— 放在侧栏里就会被侧栏右缘剪掉。
// App 把它渲染成 <aside> 的兄弟节点,这个文件只负责内容与视口夹紧。
import { h } from "preact";
import { useLayoutEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState } from "../lib/store.js";
import { getState } from "../lib/store.js";
import {
  switchWorker, workerBuckets, workerBucketId, createWorker,
  openNoteEditor, editSummary, renameWorker, deleteWorker,
} from "../lib/actions.js";

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

// 当前分桶对应的 worker 记录(「其他」是服务端常驻的保留档,也有记录)。
function currentBucket() {
  const id = workerBucketId();
  return (getState().workers || []).find((x) => x.worker_id === id) || null;
}

// 分区列表:点一行就把会话列表切到那个桶。
function bucketList() {
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

// 当前分区的动作行:公约 / 摘要 / 名称 / 删除。保留档「其他」没有目录,
// 只有公约可改,改名与删除不参与。
function bucketActions(w) {
  if (!w) return null;
  return html`<div class="wk-menu-actions">
    <button class="wk-act" title="查看/编辑公约(共享给这个工作区的全部会话)"
      onClick=${() => { setState({ workerMenuOpen: false }); openNoteEditor(w.worker_id); }}>📄 公约</button>
    <button class="wk-act" title="改写摘要(新会话靠它决定加入哪个工作区)"
      onClick=${() => { setState({ workerMenuOpen: false }); editSummary(w.worker_id); }}>✎ 摘要</button>
    ${w.reserved ? null : html`<button class="wk-act" title="改名(只改名字:目录不动,会话里的路径不受影响)"
      onClick=${() => { setState({ workerMenuOpen: false }); renameWorker(w.worker_id); }}>✎ 名称</button>`}
    ${w.reserved ? null : html`<button class="wk-act wk-act-danger" title="删除工作区及其全部会话(需手打确认语;目录保留)"
      onClick=${() => { setState({ workerMenuOpen: false }); deleteWorker(w.worker_id); }}>🗑</button>`}
  </div>`;
}

// 菜单内容:分区列表 + 当前分区动作 + 新建入口。
export function openWorkerMenu() {
  const w = currentBucket();
  return html`<div class="wk-menu-body">${bucketList()}</div>
    ${bucketActions(w)}
    <button class="wk-menu-add" id="btn-new-worker" title="新建工作区(共享目录 + 公约)"
      onClick=${() => { setState({ workerMenuOpen: false }); createWorker(); }}>
      ＋ 新建工作区
    </button>`;
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
    ${openWorkerMenu()}
  </div>`;
}
