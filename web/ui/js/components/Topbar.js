// 顶栏 + 聊天头(v3.1:按用户反馈回摆)。
//   顶栏(左→右):模型芯片(绿点 + 模型名 + 窗口大小徽标)、「轨迹」开关;
//         右侧只在有审批时出现徽标。深度思考不再有顶栏入口——输入区的思考
//         胶囊 / 双击模型按钮就是它;「改进」已按需求下线。
//   聊天头:标题做成下划线 tab;新会话副标「发送后自动保存会话 ⓘ」;
//         右侧四颗:子代理 / 后台 / 摘要 / 上下文。
import { h } from "preact";
import htm from "htm";

import { useStore, setState, getState, toggleCtxPanel, toggleTrajPanel, toggleSummaryPanel } from "../lib/store.js";
import { selectSession, renameSession, loadChildren, loadJobs } from "../lib/actions.js";
import { fmtK } from "../lib/format.js";
import { TasksPanel } from "./panels/TasksPanel.js";
import { JobsPanel } from "./panels/JobsPanel.js";

const html = htm.bind(h);

// v3.8:桌面(frameless)下顶栏整条隐藏,模型芯片组并进聊天头(标题不再显示);
// web 模式布局不变。frameless 类由 titlebar.js 在 App 渲染前加好(main.js 顺序)。
export function isFramelessUi() {
  return typeof document !== "undefined"
    && document.documentElement.classList.contains("frameless");
}

/* ---------------- 模型芯片组:模型 + 轨迹 + 审批徽标 ----------------
   顶栏(web)与聊天头(frameless)共用这一份,DOM id 一致。 */
export function ModelChips() {
  const approvals = useStore((s) => s.approvals);
  const model = useStore((s) => s.model);
  const defaultModel = useStore((s) => s.defaultModel);
  const port = useStore((s) => s.port);
  const models = useStore((s) => s.models);
  const trajOpen = useStore((s) => s.trajOpen);
  const count = Object.keys(approvals || {}).length;

  const modelName = model || defaultModel || "未配置模型";
  const cur = (models || []).find((m) => m.name === (model || defaultModel));
  const sizeBadge = cur && cur.context_window ? fmtK(cur.context_window) : "";
  const loading = (models || []).length === 0 && !port;

  return html`<span class=${"hchip model-chip" + (loading ? " loading" : "")} id="status-pill"
    title=${"当前模型:" + modelName + (port ? " · 本地服务端口 " + port : "")}>
    <i class="mc-dot"></i>
    <b id="model-name-top">${loading ? "状态加载中…" : modelName}</b>
    ${!loading && sizeBadge ? html`<span class="mc-size">${sizeBadge}</span>` : null}
  </span>
  <button class=${"hchip traj-chip" + (trajOpen ? " on" : "")} id="btn-traj"
    title="轨迹:本会话逐步执行流(任务/助手/工具/压缩),含时间线与搜索"
    onClick=${() => toggleTrajPanel()}>
    <svg class="hc-ico" viewBox="0 0 24 24" aria-hidden="true">
      <path d="M3 16.5 8 10l4 4.5L16 6l5 7.5" />
      <circle cx="8" cy="10" r="1.5" fill="currentColor" stroke="none" />
      <circle cx="16" cy="6" r="1.5" fill="currentColor" stroke="none" />
    </svg>
    轨迹
  </button>
  <button class=${"approval-badge" + (count ? " show" : "")} id="btn-approval-badge"
    title="有待审批的危险操作"
    onClick=${() => {
      const ids = Object.keys(getState().approvals || {});
      if (ids.length) setState({ approvalShown: ids[0] });
    }}>🔔 审批 <span id="approval-count">${count}</span></button>`;
}

/* ---------------- 顶栏 ---------------- */
export function Topbar() {
  // frameless 下整条不渲染(不是 display:none —— 顶栏和聊天头共用芯片组 DOM,
  // 两份 id 重复会让 getElementById/querySelector 命中隐藏的那份)。
  if (isFramelessUi()) return null;
  return html`<header>
    <${ModelChips} />
    <div class="spacer"></div>
  </header>`;
}

/* ---------------- 聊天头 ---------------- */
export function ChatHead() {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const children = useStore((s) => s.children);
  const tasksOpen = useStore((s) => s.tasksOpen);
  const jobsOpen = useStore((s) => s.jobsOpen);
  const ctxOpen = useStore((s) => s.ctxOpen);
  const summaryOpen = useStore((s) => s.summaryOpen);

  const cur = (sessions || []).find((s) => s.session_id === current);
  const parentId = cur && cur.parent_id;
  const title = current ? (cur ? (cur.title || "会话 " + current.slice(0, 8)) : "会话 " + current.slice(0, 8)) : "新会话";

  const runningKids = (children || []).filter((c) => c.active).length;
  const jobsRunning = cur ? (cur.jobs_running || 0) : 0;
  const jobsTotal = cur ? (cur.jobs_total || 0) : 0;

  return html`<div class="chat-head">
    ${isFramelessUi() ? html`<${ModelChips} />` : null}
    <button class=${"back-btn" + (parentId ? " show" : "")} id="btn-back-parent" title="返回父会话"
      onClick=${() => { if (parentId) selectSession(parentId); }}>← 父会话</button>
    ${isFramelessUi() ? null : html`<span class="title" id="chat-title" title=${title + "(双击重命名)"}
      onDblClick=${() => { if (current) renameSession(current, cur ? cur.title : ""); }}>${title}</span>`}
    ${current ? null : html`<span id="chat-sub" class="chat-sub">发送后自动保存会话<span class="sub-info"
      title="第一条消息发出时自动创建会话并保存到左侧历史列表,不用手动保存">ⓘ</span></span>`}
    <span class="spacer"></span>
    <button class="tasks-btn show" id="btn-tasks" title="子代理:该会话派遣的子任务"
      onClick=${() => { const open = !getState().tasksOpen; setState({ tasksOpen: open }); if (open) loadChildren(); }}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="9" cy="8" r="3.2" />
        <path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" />
        <circle cx="17" cy="9" r="2.4" />
        <path d="M15.5 14.4c2.3.3 3.9 1.9 4.4 4.3" />
      </svg>
      <span class=${"tb-n" + (runningKids ? " hot" : "")} id="tasks-count">${runningKids > 0 ? runningKids : (children || []).length}</span> 子代理
    </button>
    <button class="tasks-btn show" id="btn-jobs" title="后台命令:运行中/已完成,可停止"
      onClick=${() => { const open = !getState().jobsOpen; setState({ jobsOpen: open }); if (open) loadJobs(); }}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M12 3.5 21 8l-9 4.5L3 8Z" />
        <path d="M3 12.5 12 17l9-4.5" />
        <path d="M3 16.5 12 21l9-4.5" />
      </svg>
      <span class=${"tb-n" + (jobsRunning ? " hot" : "")} id="jobs-count">${jobsRunning > 0 ? jobsRunning : jobsTotal}</span> 后台
    </button>
    <button class=${"tasks-btn show" + (summaryOpen ? " on" : "")} id="btn-summary"
      title="摘要:本会话执行结果 / 相关文件 / 性能指标" onClick=${() => toggleSummaryPanel()}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M5 4.5h14" /><path d="M5 9.5h14" /><path d="M5 14.5h9" /><path d="M5 19.5h6" />
      </svg>
      摘要
    </button>
    <button class=${"tasks-btn show" + (ctxOpen ? " on" : "")} id="btn-rail"
      title="上下文管理:占用 / 组成 / 轮次步 / 事件" onClick=${toggleCtxPanel}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <rect x="3.5" y="4.5" width="17" height="4.2" rx="1.6" />
        <rect x="3.5" y="10.4" width="17" height="4.2" rx="1.6" />
        <rect x="3.5" y="16.3" width="10" height="4.2" rx="1.6" />
      </svg>
      上下文
    </button>
    <div class=${"tasks-panel" + (tasksOpen ? " open" : "")} id="tasks-panel">
      ${tasksOpen ? html`<${TasksPanel} />` : null}
    </div>
    <div class=${"tasks-panel" + (jobsOpen ? " open" : "")} id="jobs-panel">
      ${jobsOpen ? html`<${JobsPanel} />` : null}
    </div>
  </div>`;
}
