// Super-Agent(Router)主区面板。
//
// SuperFrame 包在 Chat+Composer 外面:当前会话不是 super 时原样渲染 children,
// 对普通会话零影响;是 super 时在聊天区顶部画六页签,「会话」页签保留原生聊天,
// 其余五页签换成只读为主的监控面板(人类在这里只做"配边界 + 拨开关"两件事:
// 配置条 / worker 公约 / 定时任务启停 / 手动冲刷,全部走 /api/super/* 且后端记审计)。
//
// 数据源只有一个:GET /api/super/overview 一次给全(3s 轮询,仅 super 会话打开时)。
import { h, Fragment } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { ROUTES, getJSON, postJSON } from "../lib/api.js";
import { useStore, setState, toast } from "../lib/store.js";
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

const TABS = [
  { id: "chat", label: "会话" },
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

  if (role !== "super") return children;

  const running = ((ov && ov.procs) || []).filter((p) => p.status === "running").length;
  const badges = {
    workers: ov ? (ov.workers || []).length : 0,
    sched: ov ? (ov.schedules || []).filter((s) => s.enabled).length : 0,
    chan: ov ? (ov.buf_count || 0) : 0,
    proc: running,
  };

  return html`
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
      <span class="sup-hint" title=${"脚本目录:" + ((ov && ov.scripts_dir) || "")}>
        ${(ov && ov.runner_started_at) ? "runner " + clock(ov.runner_started_at) + " 起" : "runner —"}
      </span>
    </div>
    ${tab === "chat" ? children : html`
      <div class="sup-scroll">
        ${tab === "workers" ? html`<${WorkersPanel} ov=${ov}/>` : null}
        ${tab === "sched" ? html`<${SchedPanel} ov=${ov}/>` : null}
        ${tab === "chan" ? html`<${ChanPanel} ov=${ov}/>` : null}
        ${tab === "proc" ? html`<${ProcPanel} ov=${ov}/>` : null}
        ${tab === "audit" ? html`<${AuditPanel} ov=${ov}/>` : null}
      </div>`}`;
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
          </div>
          <div class="sup-wsessions">
            ${sess.map((x) => html`
              <button key=${x.id} class=${"sup-sess" + (x.active ? " act" : "")} title=${"打开会话 " + x.id}
                onClick=${() => selectSession(x.id)}>
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
    <div class="sup-note">脚本放进 <code>${(ov.scripts_dir || "") + "\\scripts"}</code> 即自动上线(每 5 分钟扫一次);
      stdout 非空自动进消息通道。启停与手动执行都以 human 身份记审计。</div>
    <table class="sup-table">
      <thead><tr><th></th><th>脚本</th><th>周期</th><th>通道</th><th>上次</th><th>结果</th><th></th></tr></thead>
      <tbody>
        ${list.length === 0 ? html`<tr><td colspan="7" class="sup-empty">scripts/ 目录还是空的</td></tr>` : null}
        ${list.map((s) => {
          const hist = ((ov.hist || {})[s.file] || []);
          const open = openFile === s.file;
          return html`<${Fragment} key=${s.file}>
            <tr class=${s.enabled ? "" : " off"}>
              <td><button class=${"sup-sw" + (s.enabled ? " on" : "")} title=${s.enabled ? "停用" : "启用"}
                onClick=${() => post(ROUTES.superSchedToggle, { file: s.file })}><span></span></button></td>
              <td class="mono" title=${s.file}>${s.file}</td>
              <td>${Math.round(s.every_sec / 60)}min</td>
              <td><span class=${"sup-tag " + s.mode}>${s.mode === "imm" ? "立即" : "批次"}</span></td>
              <td title=${full(s.last_run)}>${s.last_run ? relTime(s.last_run) : "从未"}</td>
              <td class=${s.last_exit === 0 ? "ok" : (s.last_run ? "bad" : "")}>
                ${s.last_run ? "exit " + s.last_exit : "—"}
                ${hist.length ? html` <button class="sup-linkbtn" onClick=${() => setOpenFile(open ? "" : s.file)}>${open ? "收起" : "历史 " + hist.length}</button>` : null}
              </td>
              <td><button class="sup-linkbtn" title="立即跑一次" onClick=${() => post(ROUTES.superSchedRun, { file: s.file }, "已触发:" + s.file)}>▶ 跑一次</button></td>
            </tr>
            ${open ? html`<tr class="sup-histrow"><td colspan="7">
              ${hist.map((r, i) => html`<div class="sup-hist" key=${i}>
                <span class="t">${full(r.ts)}</span>
                <span class=${r.exit === 0 ? "ok" : "bad"}>exit ${r.exit}</span>
                <span>${r.ms}ms</span>
                <pre title=${r.tail}>${(r.tail || "").slice(0, 400)}</pre>
              </div>`)}
            </td></tr>` : null}
          </${Fragment}>`;
        })}
      </tbody>
    </table>`;
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
      ${ov.config ? ov.config.flush_timeout_min : "?"}min 合并成一条。worker 汇报走立即,脚本 stdout 走批次。</div>
    <div class="sup-chan2">
      <div class="sup-colt">立即通道</div>
      ${imm.length === 0 ? html`<div class="sup-empty">暂无</div>` : null}
      ${imm.map((m) => html`<div class="sup-msg" key=${m.id} title=${full(m.created_at)}>
        <span class=${"sup-msg-src s-" + m.src}>${m.src}</span>
        <span class="sup-msg-t">${m.payload}</span>
        <span class="sup-msg-c">${clock(m.created_at)}</span>
      </div>`)}
    </div>
    <div class="sup-chan2">
      <div class="sup-colt">缓冲批次(冲刷前只有这里能看到)</div>
      ${buf.length === 0 ? html`<div class="sup-empty">空</div>` : null}
      ${buf.map((m) => html`<div class="sup-msg" key=${m.id} title=${full(m.created_at)}>
        <span class=${"sup-msg-src s-" + m.src}>${m.src}</span>
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
    <div class="sup-note">runner 心跳 15s。全量输出归档在 <code>${ov.outbox_dir || ""}</code>;
      这里保留最近 100 条进程记录(stdout 给前 400 字符)。</div>
    <table class="sup-table">
      <thead><tr><th>开始</th><th>进程</th><th>PID</th><th>状态</th><th>exit</th><th>时长</th><th>输出</th></tr></thead>
      <tbody>
        ${ps.length === 0 ? html`<tr><td colspan="7" class="sup-empty">还没有跑过脚本</td></tr>` : null}
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
