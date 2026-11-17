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

const WK_ICON_OTHER = "M4.2 4.2h15.6v15.6H4.2z";
const WK_ICON_FOLDER = "M4 7.2A2.2 2.2 0 0 1 6.2 5h3.4l1.8 2.2H17.8A2.2 2.2 0 0 1 20 9.4v7.4A2.2 2.2 0 0 1 17.8 19H6.2A2.2 2.2 0 0 1 4 16.8V7.2Z";

export function wkIcon(id) {
  const isOther = id === "w-other";
  return html`<svg class="wk-menu-ico" viewBox="0 0 24 24" width="16" height="16"
    fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path d=${isOther ? WK_ICON_OTHER : WK_ICON_FOLDER} stroke-dasharray=${isOther ? "3.6 3" : null} />
  </svg>`;
}

export function wkDot(b) {
  const busy = b.active || 0;
  const cls = busy > 0 ? "running" : ((b.sessions || 0) > 0 ? "done" : "none");
  return html`<span class=${"wk-menu-dot " + cls} title=${busy > 0
    ? busy + " 个会话执行中"
    : ((b.sessions || 0) > 0 ? "没有会话在跑" : "还没有会话")}></span>`;
}

export function placeWorkerMenu(menu, anchor) {
  const r = anchor && anchor.getBoundingClientRect
    ? anchor.getBoundingClientRect()
    : (anchor || { left: 12, bottom: 120, width: 200 });
  const w = menu.offsetWidth, h = menu.offsetHeight;
  const left = Math.max(8, Math.min(r.left, Math.max(8, window.innerWidth - w - 8)));
  const top = Math.max(8, Math.min(r.bottom + 4, Math.max(8, window.innerHeight - h - 8)));
  menu.style.left = left + "px";
  menu.style.top = top + "px";
  const tip = r.left + r.width / 2 - left - 5; 
  menu.style.setProperty("--wk-arrow-x", Math.max(20, Math.min(tip, Math.max(20, w - 32))) + "px");
}

function currentBucket() {
  const id = workerBucketId();
  return (getState().workers || []).find((x) => x.worker_id === id) || null;
}

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
