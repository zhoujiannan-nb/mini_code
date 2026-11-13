// 侧栏:品牌、新建、worker 切换条 + 子栏(公约/摘要/名称/删除)、会话列表。
// 列表的三种状态(加载骨架 / 拉取失败 / 空)都画出来 —— 静默空白会让人以为
// "会话没了"。
import { h } from "preact";
import { useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState, getState } from "../lib/store.js";
import { AgentAvatar } from "../lib/avatars.js";
import { relTime } from "../lib/format.js";
import {
  workerBuckets, workerBucketId, workerEmptyHint, loadSessions,
  selectSession, renameSession, deleteSession, detachWorkerForNewSession, resetToNew,
  createWorker, editSummary, renameWorker, deleteWorker, openNoteEditor,
} from "../lib/actions.js";
import { WorkerMenu } from "./WorkerMenu.js";

const html = htm.bind(h);

// 拉列表中的占位:三行会话形状的骨架,真行落下来时面板不会跳。
export function sessSkeleton() {
  return html`<div class="sess-list-skeleton">
    ${[0, 1, 2].map((i) => html`<div class="sess-item sess-skeleton" aria-hidden="true" key=${i}>
      <div class="sk sk-title"></div><div class="sk sk-meta"></div>
    </div>`)}
  </div>`;
}

// worker 子栏:📄 公约 / ✎ 摘要 / ✎ 名称 / 🗑。
// 「＋ 会话」小按钮已删:顶部「＋ 新建会话」就落在当前区,语义完全重复。
export function paintWorkerBar() {
  const list = getState().workers;
  const id = workerBucketId();
  const w = (list || []).find((x) => x.worker_id === id);
  // 查不到 worker(服务端过旧 / 刚被删)时没有共享目录,子栏整行留空。
  if (!w || (!w.work_dir && !w.reserved)) return null;
  return html`<span>
    <button class="wk-sub-btn" id="wk-note-btn" title="查看/编辑公约(共享给这个 worker 的全部会话)"
      onClick=${() => openNoteEditor(w.worker_id)}>📄 公约</button>
    <button class="wk-sub-btn" id="wk-summary-btn" title="改写摘要(新会话靠它决定加入哪个 worker)"
      onClick=${() => editSummary(w.worker_id)}>✎ 摘要</button>
    ${w.reserved ? null : html`<button class="wk-sub-btn" id="wk-rename-btn" title="改名(只改名字:目录不动,会话里的路径不受影响)"
      onClick=${() => renameWorker(w.worker_id)}>✎ 名称</button>`}
    ${w.reserved ? null : html`<button class="wk-sub-btn wk-sub-danger" id="wk-del-btn" title="删除工作区及其全部会话(需手打确认语;目录保留)"
      onClick=${() => deleteWorker(w.worker_id)}>🗑</button>`}
  </span>`;
}

function SessionItem({ s }) {
  const current = useStore((x) => x.current);
  // 执行中只看服务端的实时标志:持久化的 status 在进程崩溃/重启后可能停留在
  // "running",不可信。后台子 agent 在跑也算执行中;会话自己的后台命令还活着时
  // 按"后台运行中"显示(agent 空闲等通知,但活确实还在干)。
  const busy = s.active || !!s.children_active || (s.jobs_running > 0);
  const dotClass = busy ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
  const statusText = s.active ? "执行中" : (s.children_active ? "子任务中"
    : (s.jobs_running > 0 ? "后台运行中"
      : ({ completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || "")));
  return html`<div class=${"sess-item" + (s.session_id === current ? " active" : "")}
    onClick=${(e) => { if (e.target.closest("button")) return; selectSession(s.session_id); }}>
    <div class="sess-title-row">
      <${AgentAvatar} cls="sess-ava" tag="span" />
      <div class="sess-title">${s.title || "未命名会话"}</div>
    </div>
    <div class="sess-meta">
      <span class=${"s " + dotClass}></span>
      ${s.mode === "cautious" ? html`<span title="谨慎模式">🛡</span>` : null}
      ${statusText ? html`<span>${statusText}</span>` : null}
      <span>${relTime(s.updated_at)}</span>
      <span>${s.message_count} 条</span>
    </div>
    <button class="rename-btn" title="重命名会话"
      onClick=${(e) => { e.stopPropagation(); renameSession(s.session_id, s.title); }}>✎</button>
    <button class="del-btn" title="删除会话"
      onClick=${(e) => { e.stopPropagation(); deleteSession(s.session_id, s.title); }}>✕</button>
  </div>`;
}

export function Sidebar() {
  const sessions = useStore((s) => s.sessions);
  const loading = useStore((s) => s.sessionsLoading);
  const error = useStore((s) => s.sessionsError);
  const workers = useStore((s) => s.workers);
  const workerId = useStore((s) => s.worker);
  const pickRef = useRef(null);

  const bucket = (workerId === "" || workerId === "*" || workerId == null) ? "w-other" : workerId;
  const w = (workers || []).find((x) => x.worker_id === bucket);
  const wkName = (w ? w.name : (workers || []).find((x) => x.worker_id === bucket)?.name || "未知 worker")
    + (w && w.sessions ? " · " + w.sessions : "");

  const openMenu = (e) => {
    e.stopPropagation();
    const el = pickRef.current;
    const st = getState();
    if (st.workerMenuOpen) { setState({ workerMenuOpen: false }); return; }
    const r = el ? el.getBoundingClientRect() : { left: 12, bottom: 120, width: 200 };
    setState({ workerMenuOpen: true, workerMenuAnchor: { left: r.left, bottom: r.bottom, width: r.width } });
  };

  let list;
  if (loading) list = sessSkeleton();
  else if (error) {
    list = html`<div class="empty-hint">会话列表没刷出来：${error}<br />30s 轮询会自动重试,切一下 worker 也能立刻重来</div>`;
  } else if (!sessions.length) {
    const hint = workerEmptyHint();
    const [line1, line2] = hint.split("\n");
    list = html`<div class="empty-hint">${line1}${line2 ? html`<br />${line2}` : null}</div>`;
  } else {
    list = sessions.map((s) => html`<${SessionItem} key=${s.session_id} s=${s} />`);
  }

  return html`<aside>
    <div class="brand">
      <span class="brand-mark">
        <svg class="mk" viewBox="0 0 32 32" width="32" height="32" aria-hidden="true">
          <defs><linearGradient id="mc-mark-g" x1="0" y1="0" x2="1" y2="1">
            <stop class="s1" offset="0" /><stop class="s2" offset=".55" /><stop class="s3" offset="1" />
          </linearGradient></defs>
          <rect x=".6" y=".6" width="30.8" height="30.8" rx="9" fill="url(#mc-mark-g)" />
          <rect x=".6" y=".6" width="30.8" height="30.8" rx="9" fill="none" stroke="rgba(0,0,0,.22)" />
          <path d="M9.6 11.4 5.4 16l4.2 4.6" fill="none" stroke="#fff" stroke-opacity=".95" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" />
          <path d="M18.6 9.8 13.4 22.2" fill="none" stroke="#fff" stroke-opacity=".95" stroke-width="2.1" stroke-linecap="round" />
          <path d="M22.4 11.4 26.6 16l-4.2 4.6" fill="none" stroke="#fff" stroke-opacity=".95" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </span>
      <div class="logo">Mini <span>Code</span></div>
      <span class="brand-sub">agent</span>
    </div>
    <button class="new-btn" id="btn-new" title="新建会话（落在当前选中的工作区；没选过就是「其他」）"
      onClick=${() => { detachWorkerForNewSession(); resetToNew(); }}>
      ＋ 新建会话
    </button>
    <div class="worker-bar" id="worker-bar">
      <button class="wk-pick" id="wk-pick" title="切换 worker / 查看它的目录与公约"
        ref=${pickRef} onClick=${openMenu}>
        <span class="wk-label">worker</span><span class="wk-div"></span>
        <span class="wk-name" id="wk-name">${wkName}</span><span class="wk-caret">⌄</span>
      </button>
      <button class="wk-add" id="btn-new-worker" title="新建 worker(工作大类:共享目录 + 公约)"
        onClick=${(e) => { e.stopPropagation(); createWorker(); }}>＋</button>
    </div>
    <div class="worker-sub" id="worker-sub">${paintWorkerBar()}</div>
    <div class="sess-list" id="sess-list">${list}</div>
  </aside>`;
}
