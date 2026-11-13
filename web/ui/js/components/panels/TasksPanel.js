// 子任务(子代理)面板:当前会话派出的子 agent 列表,运行中可单独停止。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, setState, toast } from "../../lib/store.js";
import { ROUTES, api } from "../../lib/api.js";
import { selectSession, loadChildren } from "../../lib/actions.js";
import { fmtTok, fmtTaskDur } from "../../lib/format.js";

const html = htm.bind(h);

export function TasksPanel() {
  const children = useStore((s) => s.children);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  const stopTask = async (id) => {
    try {
      const r = await api(ROUTES.cancel(id), { method: "POST" });
      toast(r.canceled ? "已发送停止请求" : "该子任务当前没有正在执行的任务", r.canceled ? "ok" : "");
      loadChildren();
    } catch (e) { toast("停止失败:" + e.message, "err"); }
  };

  if (!(children || []).length) return html`<div class="empty-hint">暂无子任务</div>`;
  return html`<div>
    ${children.map((c) => {
      const dot = c.active ? "running" : (["completed", "failed", "interrupted"].includes(c.status) ? c.status : "idle");
      const st = c.active ? "执行中" : ({ completed: "已完成", failed: "失败", interrupted: "已中断" }[c.status] || "");
      const tok = fmtTok(c.tokens || 0);
      const dur = fmtTaskDur(c, now);
      const tip = (c.title || c.session_id) + (st ? "(" + st + ")" : "") + "\n" + c.session_id;
      return html`<div class="task-item" data-id=${c.session_id} title=${tip} key=${c.session_id}
        onClick=${(e) => {
          if (e.target.classList.contains("task-stop")) return;
          setState({ tasksOpen: false });
          selectSession(c.session_id);
        }}>
        <span class=${"s " + dot}></span>
        <span class="a-main">
          <span class="a-title">${c.title || c.session_id.slice(0, 8)}</span>
          <span class="a-kind">${c.agent_role || "build"}</span>
        </span>
        <span class="a-meta"><span>${tok || "—"}</span><span>${dur || st || "—"}</span></span>
        ${c.active ? html`<button class="task-stop" onClick=${(e) => { e.stopPropagation(); stopTask(c.session_id); }}>停止</button>` : null}
        <span class="a-arrow">›</span>
      </div>`;
    })}
  </div>`;
}
