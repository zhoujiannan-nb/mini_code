// 顶栏 + 聊天头。顶栏:审批徽标、主题快捷切换、配置入口;
// 聊天头:返回父会话、状态胶囊、头像、标题(双击重命名)、子任务/后台命令入口。
import { h } from "preact";
import htm from "htm";

import { useStore, setState, getState, toast } from "../lib/store.js";
import { cycleThemeMode, themeLabel } from "../lib/theme.js";
import { selectSession, renameSession, loadChildren, loadJobs } from "../lib/actions.js";
import { openSettings } from "../lib/settings.js";
import { AgentAvatar } from "../lib/avatars.js";
import { TasksPanel } from "./panels/TasksPanel.js";
import { JobsPanel } from "./panels/JobsPanel.js";

const html = htm.bind(h);

export function Topbar() {
  const approvals = useStore((s) => s.approvals);
  const themeMode = useStore((s) => s.themeMode);
  const count = Object.keys(approvals || {}).length;
  const mode = themeMode || "system";
  const icon = { light: "☀", dark: "☾", system: "🖥" }[mode] || "🖥";

  const onTheme = () => {
    const next = cycleThemeMode();
    setState({ themeMode: next });
    toast("主题:" + themeLabel(next), "ok");
  };
  return html`<header>
    <div class="spacer"></div>
    <button class=${"approval-badge" + (count ? " show" : "")} id="btn-approval-badge"
      title="有待审批的危险操作"
      onClick=${() => {
        const ids = Object.keys(getState().approvals || {});
        if (ids.length) setState({ approvalShown: ids[0] });
      }}>🔔 审批 <span id="approval-count">${count}</span></button>
    <button class="icon-btn theme-btn" id="btn-theme" title="切换主题:浅色 / 深色 / 跟随系统" onClick=${onTheme}>
      <svg class="ico" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="12" r="8" />
        <path d="M12 4a8 8 0 0 1 0 16z" fill="currentColor" stroke="none" />
      </svg>
      <span class="lbl">${icon} ${themeLabel(mode)}</span>
    </button>
    <button class="icon-btn" id="btn-settings" onClick=${openSettings}>
      <svg class="ico" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M4 8.5h8M16.6 8.5H20M4 15.5h3.4M11.6 15.5H20" />
        <circle cx="14" cy="8.5" r="2.4" /><circle cx="9.6" cy="15.5" r="2.4" />
      </svg>
      <span class="lbl">配置</span>
    </button>
  </header>`;
}

export function ChatHead() {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const model = useStore((s) => s.model);
  const defaultModel = useStore((s) => s.defaultModel);
  const port = useStore((s) => s.port);
  const models = useStore((s) => s.models);
  const children = useStore((s) => s.children);
  const tasksOpen = useStore((s) => s.tasksOpen);
  const jobsOpen = useStore((s) => s.jobsOpen);

  const cur = (sessions || []).find((s) => s.session_id === current);
  const parentId = cur && cur.parent_id;
  const title = current ? (cur ? (cur.title || "会话 " + current.slice(0, 8)) : "会话 " + current.slice(0, 8)) : "新会话";
  const sub = current ? "" : "发送后自动创建会话";
  const modelName = model || defaultModel || "未配置模型";
  const statusText = models.length === 0 && !port
    ? "状态加载中…"
    : html`<span><b>${modelName}</b> · 端口 ${port}</span>`;

  const runningKids = (children || []).filter((c) => c.active).length;
  const jobsRunning = cur ? (cur.jobs_running || 0) : 0;
  const jobsTotal = cur ? (cur.jobs_total || 0) : 0;

  return html`<div class="chat-head">
    <button class=${"back-btn" + (parentId ? " show" : "")} id="btn-back-parent" title="返回父会话"
      onClick=${() => { if (parentId) selectSession(parentId); }}>← 父会话</button>
    <span class="pill" id="status-pill" title="当前模型 / 本地服务端口">${statusText}</span>
    ${current ? html`<${AgentAvatar} id="chat-ava" cls="chat-ava" tag="span" />` : null}
    <span class="title" id="chat-title" title=${title + "(双击重命名)"}
      onDblClick=${() => { if (current) renameSession(current, cur ? cur.title : ""); }}>${title}</span>
    <span id="chat-sub">${sub}</span>
    <span class="spacer"></span>
    <button class=${"tasks-btn" + (current && (children || []).length ? " show" : "")} id="btn-tasks"
      title="查看该会话派遣的子代理任务"
      onClick=${() => { const open = !getState().tasksOpen; setState({ tasksOpen: open }); if (open) loadChildren(); }}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="9" cy="8" r="3.2" />
        <path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" />
        <circle cx="17" cy="9" r="2.4" />
        <path d="M15.5 14.4c2.3.3 3.9 1.9 4.4 4.3" />
      </svg>
      <span class=${"tb-n" + (runningKids ? " hot" : "")} id="tasks-count">${runningKids > 0 ? runningKids : (children || []).length}</span> 个子智能体
    </button>
    <button class=${"tasks-btn" + (current && jobsTotal ? " show" : "")} id="btn-jobs"
      title="该会话的后台命令(运行中/已完成,可停止)"
      onClick=${() => { const open = !getState().jobsOpen; setState({ jobsOpen: open }); if (open) loadJobs(); }}>
      <svg class="tb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <path d="M12 3.5 21 8l-9 4.5L3 8Z" />
        <path d="M3 12.5 12 17l9-4.5" />
        <path d="M3 16.5 12 21l9-4.5" />
      </svg>
      <span class=${"tb-n" + (jobsRunning ? " hot" : "")} id="jobs-count">${jobsRunning > 0 ? jobsRunning : jobsTotal}</span> 个后台任务
    </button>
    <div class=${"tasks-panel" + (tasksOpen ? " open" : "")} id="tasks-panel">
      ${tasksOpen ? html`<${TasksPanel} />` : null}
    </div>
    <div class=${"tasks-panel" + (jobsOpen ? " open" : "")} id="jobs-panel">
      ${jobsOpen ? html`<${JobsPanel} />` : null}
    </div>
  </div>`;
}
