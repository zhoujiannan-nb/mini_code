// 后台命令(job)面板:分「运行中 / 已结束」两段(可折叠),运行中行两按停止,
// 已结束行 ⌄ 就地展开输出。面板开着才轮询(2s),关了零开销。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, toast } from "../../lib/store.js";
import { ROUTES, api } from "../../lib/api.js";
import { loadJobs, killJob, clearJobs } from "../../lib/actions.js";
import { jobElapsed } from "../../lib/format.js";

const html = htm.bind(h);

const JOB_STATUS = { running: "运行中", completed: "已完成", failed: "失败", killed: "已终止", lost: "丢失" };
const JOB_DOT = { running: "running", completed: "completed", failed: "failed", killed: "idle", lost: "idle" };

export function JobsPanel() {
  const jobs = useStore((s) => s.jobs);
  const current = useStore((s) => s.current);
  const [closed, setClosed] = useState({});
  const [armed, setArmed] = useState(null);       // {id, timer}:两按确认中的 job
  const [outOpen, setOutOpen] = useState(null);   // 同时只开一个
  const [out, setOut] = useState({});             // job id -> 输出文本
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    loadJobs();
    const t = setInterval(() => { loadJobs(); setNow(Date.now()); }, 2000);
    return () => clearInterval(t);
  }, [current]);

  // 展开中的输出跟着轮询刷新,不动 outOpen/out 状态。
  useEffect(() => {
    if (!outOpen) return;
    (async () => {
      try {
        const r = await api(ROUTES.jobOutput(current, outOpen));
        setOut((m) => ({ ...m, [outOpen]: r.output || "(无输出)" }));
      } catch (e) { /* 静默:下一次轮询还会试 */ }
    })();
  }, [jobs, outOpen, current]);

  const pressKill = (id) => {
    if (armed && armed.id === id) {
      clearTimeout(armed.timer);
      setArmed(null);
      killJob(id);
      return;
    }
    if (armed) clearTimeout(armed.timer);
    const timer = setTimeout(() => setArmed(null), 3000);
    setArmed({ id, timer });
  };

  const toggleOut = async (id) => {
    if (outOpen === id) { setOutOpen(null); return; }
    setOutOpen(id);
    try {
      const r = await api(ROUTES.jobOutput(current, id));
      setOut((m) => ({ ...m, [id]: r.output || "(无输出)" }));
    } catch (e) {
      setOut((m) => ({ ...m, [id]: "读取失败:" + e.message }));
    }
  };

  const list = jobs || [];
  const live = list.filter((j) => j.status === "running").sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at));
  const settled = list.filter((j) => j.status !== "running")
    .sort((a, b) => Date.parse(b.ended_at || b.started_at) - Date.parse(a.ended_at || a.started_at));

  const row = (j) => html`<div class="job-item" key=${j.id}>
    <span class=${"s " + (JOB_DOT[j.status] || "idle")}></span>
    <span class="j-kind">${j.kind || "exec"}</span>
    <span class="j-label" title=${j.label || ""}>${j.label || j.id}</span>
    <span class="j-st">${j.status === "running" ? JOB_STATUS[j.status] : (j.detail || JOB_STATUS[j.status] || j.status)}</span>
    <span class="j-el" title=${j.id}>${jobElapsed(j, now)}</span>
    <span class="j-act">
      ${j.status === "running"
        ? html`<button class=${"j-stop job-stop" + (armed && armed.id === j.id ? " armed" : "")}
            data-id=${j.id} title="停止(按两次确认)"
            onClick=${(e) => { e.stopPropagation(); pressKill(j.id); }}>${armed && armed.id === j.id ? "确认停止" : "停"}</button>`
        : html`<button class="j-btn job-out" data-id=${j.id} title="展开/收起输出"
            onClick=${(e) => { e.stopPropagation(); toggleOut(j.id); }}>${outOpen === j.id ? "⌃" : "⌄"}</button>`}
    </span>
  </div>
  ${outOpen === j.id ? html`<div class="j-out" key=${j.id + "-out"}>${out[j.id] != null ? out[j.id] : "加载中…"}</div>` : null}`;

  const secHead = (key, label, right) => html`<div class=${"j-sec" + (closed[key] ? " closed" : "")} data-sec=${key}
      onClick=${() => setClosed((c) => ({ ...c, [key]: !c[key] }))}>
    <span class="chev">⌄</span>${label}${right || null}
  </div>`;

  const empty = !live.length && !settled.length;
  return html`<div>
    ${empty ? html`<div class="empty-hint">暂无后台命令</div>` : null}
    ${live.length ? secHead("live", "运行中 " + live.length, null) : null}
    ${settled.length ? secHead("settled", "已结束 " + settled.length,
      html`<span class="j-clear" onClick=${(e) => { e.stopPropagation(); clearJobs(); }}>清空</span>`) : null}
    ${live.length && !closed.live ? live.map(row) : null}
    ${settled.length && !closed.settled ? settled.map(row) : null}
  </div>`;
}
