// Super-Agent(Router)主区面板。
//
// SuperFrame 包在 Chat+Composer 外面:当前会话不是 super 时原样渲染 children,
// 对普通会话零影响;是 super 时在聊天区顶部画页签栏,「会话」页签保留原生聊天,
// 其余页签是只读为主的监控/操作面板。v9 模型:采集源(Source)→绑定 thread→
// 工单;人类在这里配边界、建源、验收工单,全部走 /api/super/* 且后端记审计。
//
// 数据源只有一个:GET /api/super/overview 一次给全(3s 轮询,仅 super 会话打开时)。
import { h, Fragment } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { ROUTES, getJSON, postJSON } from "../lib/api.js";
import { useStore, setState, getState, toast } from "../lib/store.js";
import { relTime } from "../lib/format.js";
import { selectSession } from "../lib/actions.js";

const html = htm.bind(h);

// ---- 小工具 ----
function clock(t) { // 只到时分秒:面板里时间密度高,日期在 title 里
  if (!t) return "—";
  const d = new Date(t);
  if (isNaN(d)) return "—";
  return d.toLocaleTimeString("zh-CN", { hour12: false });
}
function full(t) { return t ? String(t).replace("T", " ").replace(/\.\d+.*$/, "") : ""; }
function dur(a, b) {
  if (!a) return "—";
  const s = new Date(a).getTime();
  if (isNaN(s)) return "—";
  const e = b ? new Date(b).getTime() : Date.now();
  const n = Math.max(0, Math.round((e - s) / 1000));
  return n < 60 ? n + "s" : n < 3600 ? Math.floor(n / 60) + "m" + (n % 60 ? (n % 60) + "s" : "") : Math.floor(n / 3600) + "h" + Math.floor((n % 3600) / 60) + "m";
}
async function post(path, body, okMsg) {
  try {
    const data = await postJSON(path, body);
    if (okMsg) toast(okMsg, "ok");
    loadOv();
    return data;
  } catch (e) {
    toast("操作失败:" + e.message, "err");
    return null;
  }
}
// 主动拉一次 overview(按钮动作后立刻刷新,不等轮询)。
async function loadOv() {
  try {
    const data = await getJSON(ROUTES.superOverview);
    setState({ superOv: data });
  } catch (e) { /* 静默:轮询会自愈,失败刷屏反而干扰 */ }
}

/* ---- worker 会话侧抽屉:在面板内看会话,不离开 super 页签 ---- */

function msgText(m) {
  if (typeof m.content === "string") return m.content;
  if (Array.isArray(m.content)) return m.content.filter((p) => p && p.type === "text").map((p) => p.text || "").join("\n");
  return "";
}

async function openSuperDrawer(id, title) {
  setState({ superDrawer: { id, title: title || "会话", rows: null, loading: true, active: false, error: "" } });
  await refreshSuperDrawer();
}
async function refreshSuperDrawer() {
  const d = getState().superDrawer;
  if (!d) return;
  try {
    const data = await getJSON(ROUTES.session(d.id));
    const rows = (data.messages || [])
      .filter((m) => m && (m.role === "user" || m.role === "assistant"))
      .map((m) => ({ role: m.role, text: msgText(m) }))
      .filter((r) => r.text.trim() !== "");
    if (getState().superDrawer && getState().superDrawer.id === d.id) {
      setState({ superDrawer: Object.assign({}, d, { rows, loading: false, active: !!(data.session && data.session.active), error: "" }) });
    }
  } catch (e) {
    if (getState().superDrawer && getState().superDrawer.id === d.id) {
      setState({ superDrawer: Object.assign({}, d, { loading: false, error: e.message }) });
    }
  }
}

const TABS = [
  { id: "chat", label: "会话" },
  { id: "sources", label: "采集源" },
  { id: "tickets", label: "工单中心" },
  { id: "workers", label: "Workers" },
  { id: "sched", label: "定时任务" },
  { id: "chan", label: "消息通道" },
  { id: "proc", label: "进程查看" },
  { id: "audit", label: "审计轨迹" },
];

/* ================= 外壳:页签栏 + 轮询 + 页签切换 ================= */

export function SuperFrame({ children }) {
  const role = useStore((s) => s.currentRole);
  const tab = useStore((s) => s.superTab);
  const ov = useStore((s) => s.superOv);

  // 只在 super 会话里轮询;离开(切会话/关页)自动停。
  useEffect(() => {
    if (role !== "super") return undefined;
    loadOv();
    const t = setInterval(loadOv, 3000);
    return () => clearInterval(t);
  }, [role]);

  // 抽屉打开时跟着 3s 刷新会话内容(执行中的输出会动)。
  const drawerId = useStore((s) => (s.superDrawer ? s.superDrawer.id : ""));
  useEffect(() => {
    if (!drawerId) return undefined;
    const t = setInterval(refreshSuperDrawer, 3000);
    return () => clearInterval(t);
  }, [drawerId]);

  if (role !== "super") return children;

  const running = ((ov && ov.procs) || []).filter((p) => p.status === "running").length;
  const liveWorkers = ov ? ((ov.workers || []).filter((w) => w.live).length) : 0;
  const chanPending = ov ? (ov.buf_count || 0) : 0;
  const badges = {
    sources: ov ? (ov.sources || []).filter((s) => s.enabled).length : 0,
    tickets: ov ? (ov.running_tickets || 0) : 0,
    workers: ov ? (ov.workers || []).length : 0,
    sched: ov ? (ov.schedules || []).filter((s) => s.enabled).length : 0,
    chan: chanPending,
    proc: running,
  };

  return html`
    <div class="sup-banner">
      <span class="sb-bolt">⚡</span>
      <b class="sb-title">Super-Agent</b>
      <span class="sb-chip">v9 直投</span>
      <span class="sb-stats">采集源 ${badges.sources} · 工单 ${ov ? (ov.running_tickets || 0) : 0}/${ov ? (ov.ticket_cap || 3) : 3} · 排队 ${ov ? (ov.queued_tickets || 0) : 0} · Worker ${badges.workers}</span>
      <span class="sup-tabs-flex"></span>
      <button class="sup-linkbtn" title="切到采集源页签:建源/启停/测连通/绑定 thread"
        onClick=${() => setState({ superTab: "sources" })}>⚙ 采集源管理</button>
      <button class="sup-linkbtn" onClick=${() => setState({ superTab: "tickets" })}>→ 去工单中心</button>
    </div>
    <div class="sup-tabs">
      ${TABS.map((t) => html`
        <button key=${t.id} class=${"sup-tab" + (tab === t.id ? " on" : "")}
          onClick=${() => setState({ superTab: t.id })}>
          ${t.label}
          ${t.id !== "chat" && t.id !== "audit" && badges[t.id]
            ? html`<span class="sup-badge">${badges[t.id]}</span>` : null}
          ${t.id === "chan" && badges.chan === 0 ? html`<span class="sup-badge zero">0</span>` : null}
        </button>`)}
      <span class="sup-tabs-flex"></span>
      <span class="sup-hint" title="runner=minicode 常驻定时器(tick 15s):到点投递任务 / 兜底冲刷 / 捞起队列">
        ${(ov && ov.runner_started_at) ? "runner " + clock(ov.runner_started_at) + " 起" : "runner —"}
      </span>
    </div>
    ${tab === "chat" ? children : html`
      <div class="sup-scroll">
        ${tab === "sources" ? html`<${SourcesPanel} ov=${ov}/>` : null}
        ${tab === "tickets" ? html`<${TicketsPanel} ov=${ov}/>` : null}
        ${tab === "workers" ? html`<${WorkersPanel} ov=${ov}/>` : null}
        ${tab === "sched" ? html`<${SchedPanel} ov=${ov}/>` : null}
        ${tab === "chan" ? html`<${ChanPanel} ov=${ov}/>` : null}
        ${tab === "proc" ? html`<${ProcPanel} ov=${ov}/>` : null}
        ${tab === "audit" ? html`<${AuditPanel} ov=${ov}/>` : null}
      </div>`}
    <${SuperDrawer}/>`;
}

/* ================= worker 会话侧抽屉(不离开面板) ================= */

function SuperDrawer() {
  const d = useStore((s) => s.superDrawer);
  if (!d) return null;
  const close = () => setState({ superDrawer: null });
  const rows = d.rows || [];
  return html`<div class="sup-drawer-mask" onClick=${close}>
    <div class="sup-drawer" onClick=${(e) => e.stopPropagation()}>
      <div class="sd-head">
        ${d.active ? html`<span class="sup-dot on"></span>` : null}
        <b class="sd-title" title=${d.id}>${d.title}</b>
        <span class=${"sup-tag " + (d.active ? "active" : "")}>${d.active ? "运行中" : "空闲"}</span>
        <span class="sup-tabs-flex"></span>
        <button class="sup-linkbtn" title="重拉最新消息" onClick=${refreshSuperDrawer}>⟳</button>
        <button class="sup-linkbtn" title="整页打开(会离开面板,侧栏 Super 入口可回)"
          onClick=${() => selectSession(d.id).then(() => setState({ superOwned: true, superDrawer: null }))}>整页打开 ↗</button>
        <button class="sd-x" title="收起" onClick=${close}>✕</button>
      </div>
      <div class="sd-body">
        ${d.loading && !rows.length ? html`<div class="sup-empty">加载中…</div>` : null}
        ${d.error ? html`<div class="sup-empty">拉取失败:${d.error}</div>` : null}
        ${!d.loading && !d.error && !rows.length ? html`<div class="sup-empty">还没有消息</div>` : null}
        ${rows.map((r, i) => html`<div key=${i} class=${"sd-msg sd-" + r.role}>
          <div class="sd-role">${r.role === "user" ? "人类/来件" : "worker"}</div>
          <div class="sd-text">${r.text}</div>
        </div>`)}
      </div>
    </div>
  </div>`;
}

/* ================= 配置条(人类配边界,全部审计) ================= */

function ConfigBar({ cfg }) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState(null);
  const set = (k, v) => setForm(Object.assign({}, form, { [k]: v }));
  if (!cfg) return null;
  const f = form || cfg;
  return html`<div class="sup-cfg">
    <button class="sup-cfg-head" onClick=${() => { setOpen(!open); if (open) setForm(null); }}>
      <span class="sc-title">边界配置</span>
      <span class="sc-chips">
        <span class="sc-chip" title="worker 数量上限">上限 ${cfg.worker_cap}</span>
        <span class="sc-chip" title="worker 默认模型">${cfg.default_model || "跟随全局"}</span>
        <span class="sc-chip" title="批量冲刷阈值">批量 ${cfg.batch_size}</span>
        <span class="sc-chip" title="缓冲区兜底冲刷:超过 N 分钟自动冲刷,防止消息过夜">兜底 ${cfg.flush_timeout_min}min</span>
      </span>
      <span class="sc-caret">${open ? "▲" : "▼"}</span>
    </button>
    ${open ? html`<div class="sup-cfg-body">
      <label>上限<input type="number" min="1" max="16" value=${f.worker_cap}
        onInput=${(e) => set("worker_cap", parseInt(e.target.value || "0", 10))}/></label>
      <label class="wide">默认模型<input type="text" placeholder="留空 = 跟随全局" value=${f.default_model || ""}
        onInput=${(e) => set("default_model", e.target.value)}/></label>
      <label class="wide">工作区根<input type="text" value=${f.ws_root || ""}
        onInput=${(e) => set("ws_root", e.target.value)}/></label>
      <label>批量 N<input type="number" min="1" max="200" value=${f.batch_size}
        onInput=${(e) => set("batch_size", parseInt(e.target.value || "0", 10))}/></label>
      <label>兜底冲刷(min)<input type="number" min="1" max="1440" value=${f.flush_timeout_min}
        onInput=${(e) => set("flush_timeout_min", parseInt(e.target.value || "0", 10))}/></label>
      <button class="btn" onClick=${() => post(ROUTES.superConfig, {
        worker_cap: f.worker_cap, default_model: f.default_model, ws_root: f.ws_root,
        batch_size: f.batch_size, flush_timeout_min: f.flush_timeout_min,
      }, "边界配置已保存")}>保存</button>
    </div>` : null}
  </div>`;
}

/* ================= Workers ================= */

function WorkersPanel({ ov }) {
  const [openId, setOpenId] = useState("");
  const [draft, setDraft] = useState({});
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const ws = ov.workers || [];
  return html`
    <${ConfigBar} cfg=${ov.config}/>
    <div class="sup-wgrid">
      ${ws.length === 0 ? html`<div class="sup-empty">还没有 worker —— 在「会话」页签里吩咐 Super-Agent 建第一个。</div>` : null}
      ${ws.map((w) => {
        const open = openId === w.id;
        const d = draft[w.id] || {};
        const note = d.note !== undefined ? d.note : w.note;
        const model = d.model !== undefined ? d.model : w.model;
        const sess = w.sessions || [];
        return html`<div key=${w.id} class=${"sup-wcard" + (w.live ? " live" : "")}>
          <div class="sup-whead" onClick=${() => setOpenId(open ? "" : w.id)}>
            <span class=${"sup-dot" + (w.live ? " on" : "")}></span>
            <b>${w.name}</b>
            <span class=${"sup-tag " + w.status}>${w.status === "live" ? "在岗" : w.status === "retired" ? "已退役" : w.status}</span>
            <span class="sup-wmeta">${w.model || "默认模型"} · note v${w.note_ver}</span>
            <span class="sup-tabs-flex"></span>
            ${w.status !== "retired" ? html`<button class="sup-linkbtn" title="退役 worker(会话与历史保留可查)"
              onClick=${(e) => { e.stopPropagation(); post(ROUTES.superWorkerRetire, { id: w.id }, "worker " + w.name + " 已退役"); }}>退役</button>` : null}
          </div>
          <div class="sup-wsessions">
            ${sess.map((x) => html`
              <button key=${x.id} class=${"sup-sess" + (x.active ? " act" : "")} title=${"在侧边抽屉打开会话(不离开面板)"}
                onClick=${() => openSuperDrawer(x.id, x.title || "会话")}>
                ${x.active ? html`<span class="sup-dot on"></span>` : null}${x.title || "会话"}
                <span class="sup-sess-st">${x.status === "active" ? "开" : "关"}</span>
              </button>`)}
            ${sess.length === 0 ? html`<span class="sup-wmeta">暂无绑定会话</span>` : null}
          </div>
          ${open ? html`<div class="sup-wbody">
            <div class="sup-wdir" title=${w.work_dir}>${w.work_dir}</div>
            <label class="sup-label">角色公约(发给该 worker 每个新会话的固定前言)</label>
            <textarea rows="5" value=${note || ""} placeholder="例:你负责 mini_code 前端 CSS 回归,只改 web/ui/css/…"
              onInput=${(e) => setDraft(Object.assign({}, draft, { [w.id]: Object.assign({}, d, { note: e.target.value }) }))}></textarea>
            <div class="sup-wrow">
              <label class="sup-label">模型</label>
              <input type="text" placeholder="留空 = 用默认模型" value=${model || ""}
                onInput=${(e) => setDraft(Object.assign({}, draft, { [w.id]: Object.assign({}, d, { model: e.target.value }) }))}/>
              <button class="btn" onClick=${() => post(ROUTES.superWorkerConfig, { id: w.id, note, model }, "公约已保存,后续消息将带新版本公约")}>保存</button>
            </div>
          </div>` : null}
        </div>`;
      })}
    </div>`;
}

/* ================= 定时任务 ================= */

function SchedPanel({ ov }) {
  const [openFile, setOpenFile] = useState("");
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const list = ov.schedules || [];
  return html`
    <div class="sup-note"><b>旧世界遗物</b>:一句话定时任务已随 v9 退役(Router 不再持有 super_schedule 工具),未来以 timer 采集源形态回归。
      本页仅保留存量计划的监控与手动投递;新需求请建「采集源」。</div>
    <table class="sup-table">
      <thead><tr><th></th><th>定时任务</th><th>周期</th><th>通道</th><th>上次</th><th>结果</th><th></th></tr></thead>
      <tbody>
        ${list.length === 0 ? html`<tr><td colspan="7" class="sup-empty">还没有定时任务 —— 吩咐 Super-Agent「每 3 小时检查某某更新」即可</td></tr>` : null}
        ${list.map((s) => {
          const isTask = s.kind === "task";
          const hist = ((ov.hist || {})[s.file] || []);
          const open = openFile === s.file;
          return html`<${Fragment} key=${s.file}>
            <tr class=${s.enabled ? "" : " off"}>
              <td><button class=${"sup-sw" + (s.enabled ? " on" : "")} title=${s.enabled ? "停用" : "启用"}
                onClick=${() => post(ROUTES.superSchedToggle, { file: s.file })}><span></span></button></td>
              <td class="mono" title=${isTask ? s.note : s.file}>
                <span class=${"sup-tag " + (isTask ? "task" : "script")}>${isTask ? "任务" : "旧脚本"}</span>
                ${isTask ? html`<span class="sup-taskbody">${s.note || s.file}</span>` : s.file}
              </td>
              <td>${everyHuman(s.every_sec)}</td>
              <td><span class=${"sup-tag " + s.mode}>${s.mode === "imm" ? "立即" : "批次"}</span></td>
              <td title=${full(s.last_run)}>${s.last_run ? relTime(s.last_run) : "从未"}</td>
              <td class=${s.last_exit === 0 ? "ok" : (s.last_run ? "bad" : "")}>
                ${s.last_run ? (isTask ? "已投递" : "exit " + s.last_exit) : "—"}
                ${hist.length ? html` <button class="sup-linkbtn" onClick=${() => setOpenFile(open ? "" : s.file)}>${open ? "收起" : "历史 " + hist.length}</button>` : null}
              </td>
              <td><button class="sup-linkbtn" title="立即投一条消息到通道"
                onClick=${() => post(ROUTES.superSchedRun, { file: s.file }, "已投递:" + s.file)}>
                ▶ 投一次</button></td>
            </tr>
            ${open ? html`<tr class="sup-histrow"><td colspan="7">
              ${hist.map((r, i) => html`<div class="sup-hist" key=${i}>
                <span class="t">${full(r.ts)}</span>
                <span class=${r.exit === 0 ? "ok" : "bad"}>${isTask ? "ok" : "exit " + r.exit}</span>
                <span>${r.ms}ms</span>
                <pre title=${r.tail}>${(r.tail || "").slice(0, 400)}</pre>
              </div>`)}
            </td></tr>` : null}
          </${Fragment}>`;
        })}
      </tbody>
    </table>`;
}

// 通道来件的 src 归一:task-xxx→任务、sw-/会话→worker、脚本扩展名→脚本。
function srcKind(src) {
  if (!src) return "system";
  if (src.startsWith("task-")) return "schedule";
  if (/\.(ps1|py|bat|cmd|exe)$/i.test(src)) return "script";
  if (src === "schedule") return "schedule";
  return src;
}

// every_sec → "3h" / "90m" / "45s" 这样的人类记法。
function everyHuman(sec) {
  if (sec >= 3600 && sec % 3600 === 0) return sec / 3600 + "h";
  if (sec >= 60 && sec % 60 === 0) return sec / 60 + "min";
  return sec + "s";
}

/* ================= 消息通道 ================= */

function ChanPanel({ ov }) {
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const imm = ov.imm || [];
  const buf = ov.buf || [];
  return html`
    <div class="sup-chandash">
      <div class="sup-kpi"><b>${imm.length}</b><span>立即通道(最近 50)</span></div>
      <div class="sup-kpi"><b>${ov.buf_count || 0}</b><span>缓冲区待冲刷</span></div>
      <div class="sup-kpi"><b>${(ov.queue || []).length}</b><span>上限等待队列</span></div>
      <button class="btn" disabled=${buf.length === 0} title="把缓冲区全部冲刷进 super 会话(记 human 审计)"
        onClick=${() => post(ROUTES.superFlush, {}, "已冲刷")}>冲刷缓冲区</button>
    </div>
    <div class="sup-note">立即 = 到达即注入 super 会话打断回合;批次 = 攒满 ${ov.config ? ov.config.batch_size : "?"} 条或到兜底
      ${ov.config ? ov.config.flush_timeout_min : "?"}min 合并成一条。worker 汇报走立即,定时任务投递默认走批次。</div>
    <div class="sup-chan2">
      <div class="sup-colt">立即通道</div>
      ${imm.length === 0 ? html`<div class="sup-empty">暂无</div>` : null}
      ${imm.map((m) => html`<div class="sup-msg" key=${m.id} title=${full(m.created_at)}>
        <span class=${"sup-msg-src s-" + srcKind(m.src)}>${srcKind(m.src)}</span>
        <span class="sup-msg-t">${m.payload}</span>
        <span class="sup-msg-c">${clock(m.created_at)}</span>
      </div>`)}
    </div>
    <div class="sup-chan2">
      <div class="sup-colt">缓冲批次(冲刷前只有这里能看到)</div>
      ${buf.length === 0 ? html`<div class="sup-empty">空</div>` : null}
      ${buf.map((m) => html`<div class="sup-msg" key=${m.id} title=${full(m.created_at)}>
        <span class=${"sup-msg-src s-" + srcKind(m.src)} title=${m.src}>${srcKind(m.src)}</span>
        <span class="sup-msg-t">${m.payload}</span>
        <span class="sup-msg-c">${clock(m.created_at)}</span>
      </div>`)}
    </div>
    <div class="sup-colt">上限等待队列(worker 满员时任务停在这里,worker 退役自动捞起)</div>
    <table class="sup-table">
      <thead><tr><th>时间</th><th>任务</th><th>指定 worker</th><th>状态</th></tr></thead>
      <tbody>
        ${(ov.queue || []).length === 0 ? html`<tr><td colspan="4" class="sup-empty">空</td></tr>` : null}
        ${(ov.queue || []).map((q) => html`<tr key=${q.id}>
          <td title=${full(q.created_at)}>${clock(q.created_at)}</td>
          <td class="wrap">${q.task}</td>
          <td>${q.worker_name || "任意"}</td>
          <td><span class=${"sup-tag " + q.status}>${q.status}</span>${q.spawned_session ? " → 已起会话" : ""}</td>
        </tr>`)}
      </tbody>
    </table>`;
}

/* ================= 进程查看 ================= */

function ProcPanel({ ov }) {
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const ps = ov.procs || [];
  return html`
    <div class="sup-note">这里显示正在执行的 worker 会话与历史进程记录。runner 是 minicode 进程常驻的定时器(基础设施,心跳 15s),执行动作的不是 super、也不是 worker。</div>
    <table class="sup-table">
      <thead><tr><th>开始</th><th>进程</th><th>PID</th><th>状态</th><th>exit</th><th>时长</th><th>输出</th></tr></thead>
      <tbody>
        ${ps.length === 0 ? html`<tr><td colspan="7" class="sup-empty">当前没有执行中的会话</td></tr>` : null}
        ${ps.map((p) => html`<tr key=${p.id} class=${p.status === "running" ? "onrun" : ""}>
          <td title=${full(p.started_at)}>${clock(p.started_at)}</td>
          <td class="mono" title=${p.ref || p.name}>${p.name}</td>
          <td>${p.pid || "—"}</td>
          <td><span class=${"sup-tag " + p.status}>${p.status === "running" ? "运行中" : p.status === "exited" ? "已退出" : p.status}</span></td>
          <td class=${p.status === "exited" ? (p.exit_code === 0 ? "ok" : "bad") : ""}>${p.status === "exited" ? p.exit_code : "—"}</td>
          <td>${dur(p.started_at, p.ended_at)}</td>
          <td class="sup-out" title=${(p.ref || "")}>${p.kind === "script" ? "归档" : p.kind}</td>
        </tr>`)}
      </tbody>
    </table>`;
}

/* ================= 审计轨迹 ================= */

function AuditPanel({ ov }) {
  const [kind, setKind] = useState("");
  const [q, setQ] = useState("");
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  let rows = ov.audits || [];
  if (kind) rows = rows.filter((a) => a.actor === kind);
  if (q) {
    const needle = q.toLowerCase();
    rows = rows.filter((a) => (a.action + " " + a.target + " " + a.detail).toLowerCase().includes(needle));
  }
  return html`
    <div class="sup-filter">
      ${["", "super", "human", "runner", "system"].map((k) => html`
        <button key=${k || "all"} class=${"sup-chipbtn" + (kind === k ? " on" : "")}
          onClick=${() => setKind(k)}>${k || "全部"}</button>`)}
      <input type="text" placeholder="搜索动作 / 对象 / 详情…" value=${q} onInput=${(e) => setQ(e.target.value)}/>
      <span class="sup-count">${rows.length} 条(最近 80)</span>
    </div>
    <table class="sup-table">
      <thead><tr><th>时间</th><th>谁</th><th>动作</th><th>对象</th><th>详情</th></tr></thead>
      <tbody>
        ${rows.length === 0 ? html`<tr><td colspan="5" class="sup-empty">没有匹配的审计记录</td></tr>` : null}
        ${rows.map((a) => html`<tr key=${a.id}>
          <td title=${full(a.ts)}>${clock(a.ts)}</td>
          <td><span class=${"sup-tag a-" + a.actor}>${a.actor}</span></td>
          <td class="mono">${a.action}</td>
          <td class="wrap" title=${a.target}>${a.target}</td>
          <td class="wrap dim" title=${a.detail}>${(a.detail || "").slice(0, 160)}</td>
        </tr>`)}
      </tbody>
    </table>`;
}

/* ================= 采集源(v9 Source 采集层) ================= */

function sourceName(ov, id) {
  const s = ((ov && ov.sources) || []).find((x) => x.id === id);
  return s ? s.name : (id || "");
}

function SourcesPanel({ ov }) {
  const [open, setOpen] = useState(false);
  const [f, setF] = useState({ kind: "git-watch", name: "", path: "", branch: "", poll_sec: 300, bind_mode: "auto", bind_session: "", ttl: "3d" });
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const kinds = ov.kinds || [];
  const sessions = [];
  (ov.workers || []).forEach((w) => (w.sessions || []).forEach((x) => {
    if (x.status === "active") sessions.push({ id: x.id, label: w.name + " / " + (x.title || x.id) });
  }));
  const set = (k, v) => setF(Object.assign({}, f, { [k]: v }));
  const test = async (id) => {
    try {
      const d = await postJSON(ROUTES.superSourceTest, { id });
      toast("连通正常:" + (d.summary || "OK"), "ok");
      loadOv();
    } catch (e) { toast("测连通失败:" + e.message, "err"); }
  };
  const create = () => {
    if (!f.name || (f.kind === "git-watch" && !f.path)) { toast("名称与仓库目录必填", "err"); return; }
    const config = f.kind === "git-watch" ? { path: f.path, branch: f.branch } : {};
    post(ROUTES.superSourceCreate, {
      kind: f.kind, name: f.name, config,
      poll_sec: parseInt(String(f.poll_sec || "300"), 10),
      bind_mode: f.bind_mode, bind_session: f.bind_mode === "assign" ? f.bind_session : "", ttl: f.ttl,
    }, "采集源已建立" + (f.bind_mode === "assign" ? "并完成绑定" : "(首笔事件到达时自动首绑)"));
  };
  return html`
    <div class="sup-note">v9 模型:<b>采集源(Source)→ 绑定 thread(会话)→ 工单</b>。事件直达绑定的会话、不过 Router;
      无在办工单时自动 resume 原会话或按落地方式首绑开单。并发上限写死 ${ov.ticket_cap || 3},其余 FIFO 排队。退订=关闭+3 天宽限(事件再达自动复订),可选永久。</div>
    <div class="sup-cfg">
      <button class="sup-cfg-head" onClick=${() => setOpen(!open)}>
        <span class="sc-title">建采集源</span>
        <span class="sc-chips"><span class="sc-chip">本期仅 git-watch 启用,其余 kind 注册表预留</span></span>
        <span class="sc-caret">${open ? "▲" : "▼"}</span>
      </button>
      ${open ? html`<div class="sup-cfg-body">
        <label>类型<select value=${f.kind} onInput=${(e) => set("kind", e.target.value)}>
          ${kinds.map((k) => html`<option key=${k.name} value=${k.name} disabled=${!k.enabled}>${k.label}${k.enabled ? "" : "(预留)"}</option>`)}
        </select></label>
        <label>名称<input type="text" placeholder="如 mini_code 上游监控" value=${f.name} onInput=${(e) => set("name", e.target.value)}/></label>
        ${f.kind === "git-watch" ? html`
          <label class="wide">仓库目录<input type="text" placeholder="D:\\project_ai\\mini_code" value=${f.path} onInput=${(e) => set("path", e.target.value)}/></label>
          <label>branch<input type="text" placeholder="留空 = 当前分支" value=${f.branch} onInput=${(e) => set("branch", e.target.value)}/></label>` : null}
        <label>轮询(s)<input type="number" min="60" max="86400" value=${f.poll_sec} onInput=${(e) => set("poll_sec", e.target.value)}/></label>
        <label>落地方式<select value=${f.bind_mode} onInput=${(e) => set("bind_mode", e.target.value)}>
          <option value="auto">A · 自动派发首绑(首笔事件自动建 thread)</option>
          <option value="assign">B · 指定已有会话</option>
        </select></label>
        ${f.bind_mode === "assign" ? html`<label class="wide">指定会话<select value=${f.bind_session} onInput=${(e) => set("bind_session", e.target.value)}>
          <option value="">— 选择 worker 会话 —</option>
          ${sessions.map((x) => html`<option key=${x.id} value=${x.id}>${x.label}</option>`)}
        </select></label>` : null}
        <label>退订宽限<select value=${f.ttl} onInput=${(e) => set("ttl", e.target.value)}>
          <option value="3d">3 天(默认)</option><option value="forever">永久保留绑定</option>
        </select></label>
        <button class="btn" onClick=${create}>建源(先做连通性检查)</button>
      </div>` : null}
    </div>
    <table class="sup-table">
      <thead><tr><th></th><th>采集源</th><th>游标</th><th>轮询</th><th>上次</th><th>落地 thread</th><th></th></tr></thead>
      <tbody>
        ${(ov.sources || []).length === 0 ? html`<tr><td colspan="7" class="sup-empty">还没有采集源 —— 展开「建采集源」建第一个(如监控 mini_code 上游仓库)</td></tr>` : null}
        ${(ov.sources || []).map((src) => {
          const b = (ov.bindings || {})[src.id];
          return html`<tr key=${src.id} class=${src.enabled ? "" : " off"}>
            <td><button class=${"sup-sw" + (src.enabled ? " on" : "")} title=${src.enabled ? "停用" : "启用"}
              onClick=${() => post(ROUTES.superSourceToggle, { id: src.id })}><span></span></button></td>
            <td class="mono" title=${src.id}>
              <b>${src.name}</b>
              <span class="sup-tag task">${src.kind}</span>
              ${src.ttl === "forever" ? html`<span class="sup-tag" title="退订宽限=永久">永久</span>` : null}
              ${src.err_count > 0 ? html`<span class="sup-tag bad" title=${src.last_err}>错${src.err_count}</span>` : null}
            </td>
            <td class="mono" title=${src.cursor || "未建基线(首轮静默)"}>${(src.cursor || "—").slice(-8)}</td>
            <td>${everyHuman(src.poll_sec)}</td>
            <td title=${full(src.last_run)}>${src.last_run ? relTime(src.last_run) : "未跑"}</td>
            <td>${b ? html`
              <button class=${"sup-sess" + (b.state === "active" ? " act" : "")} title=${b.sess}
                onClick=${() => openSuperDrawer(b.sess, "thread·" + src.name)}>
                ${b.state === "active" ? html`<span class="sup-dot on"></span>` : null}${(b.sess || "").slice(0, 10)}…
              </button>
              ${b.state === "unsubscribed" ? html`<span class="sup-tag" title="宽限期内,事件再达自动复订">${b.forever ? "永久宽限" : "退订中"}</span>` : null}
              <span class="sup-wmeta">by ${b.binder}</span>` 
              : html`<span class="sup-wmeta">${src.bind_mode === "assign" ? "B·待挂 " + (src.bind_session ? src.bind_session.slice(0, 8) + "…" : "—") : "A·待首绑(等首笔事件)"}</span>`}
            </td>
            <td>
              <button class="sup-linkbtn" onClick=${() => test(src.id)}>测连通</button>
              ${b && b.state === "active" ? html`<button class="sup-linkbtn" title="退订:关闭绑定进入宽限期"
                onClick=${() => post(ROUTES.superSourceUnbind, { id: src.id, forever: false }, "已退订(3 天宽限)")}>退订</button>` : null}
              ${b && b.state === "unsubscribed" && !b.forever ? html`<button class="sup-linkbtn" title="改为永久宽限"
                onClick=${() => post(ROUTES.superSourceUnbind, { id: src.id, forever: true }, "改为永久宽限")}>永久宽限</button>` : null}
              <button class="sup-linkbtn" onClick=${() => { if (confirm("删除采集源「" + src.name + "」?绑定即删,工单留档。")) post(ROUTES.superSourceDelete, { id: src.id }, "已删除"); }}>删</button>
            </td>
          </tr>`;
        })}
      </tbody>
    </table>`;
}

/* ================= 工单中心 ================= */

const TK_STATE = { queued: "排队", running: "在跑", ready: "待验收", closed: "完结" };

function TicketsPanel({ ov }) {
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const ts = ov.tickets || [];
  const readyN = ts.filter((t) => t.state === "ready").length;
  return html`
    <div class="sup-chandash">
      <div class="sup-kpi"><b>${ov.running_tickets || 0}/${ov.ticket_cap || 3}</b><span>在跑工单(并发写死)</span></div>
      <div class="sup-kpi"><b>${ov.queued_tickets || 0}</b><span>排队中(FIFO 自动出队)</span></div>
      <div class="sup-kpi"><b>${readyN}</b><span>待人类验收</span></div>
    </div>
    <div class="sup-note">工单生命周期 <b>queued → running → ready → closed</b>:采集事件与人类交办都先成单;能落地立即派发(原话一字不改 + ≤30字备注),
      并发满排队;回合结束自动记 ready 并冒泡审计;关单记 closed 是 <b>人类验收或 Router 确认办结</b> 均可——ready 的工单被同源新事件追上时 = resume 原会话。</div>
    <table class="sup-table">
      <thead><tr><th>工单</th><th>来源</th><th>标题(≤30字转述)</th><th>状态</th><th>thread</th><th>开单</th><th></th></tr></thead>
      <tbody>
        ${ts.length === 0 ? html`<tr><td colspan="7" class="sup-empty">还没有工单 —— 建一个采集源、吩咐第一笔事件,或对 Router 说一句话</td></tr>` : null}
        ${ts.map((t) => html`<tr key=${t.id} class=${t.state === "ready" ? "onrun" : t.state === "closed" ? " off" : ""}>
          <td class="mono" title=${t.text}>${t.id}${t.reopened ? html`<span class="sup-tag" title="被 resume 过 ${t.reopened} 次">resume×${t.reopened}</span>` : null}</td>
          <td>${t.src_id ? html`<span class="sup-tag task" title=${t.src_id}>${sourceName(ov, t.src_id)}</span>` : html`<span class="sup-tag" title="人类/Router 交办">人工·${t.actor}</span>`}</td>
          <td class="wrap" title=${t.text}>${t.title}${t.note ? html` <span class="dim">（${t.note}）</span>` : null}</td>
          <td><span class=${"sup-tag " + t.state}>${TK_STATE[t.state] || t.state}</span></td>
          <td>${t.session ? html`<button class=${"sup-sess"} title=${t.session}
            onClick=${() => openSuperDrawer(t.session, t.title)}>${t.session.slice(0, 10)}…</button>` : html`<span class="sup-wmeta">待落地</span>`}</td>
          <td title=${full(t.created_at)}>${relTime(t.created_at)}</td>
          <td>${t.state !== "closed" ? html`<button class="sup-linkbtn" title="人类验收 → closed"
            onClick=${() => post(ROUTES.superTicketClose, { id: t.id }, "工单 " + t.id + " 已验收")}>验收</button>` : html`<span class="sup-wmeta">${(t.closed_at || "").slice(5, 16)}</span>`}</td>
        </tr>`)}
      </tbody>
    </table>`;
}
