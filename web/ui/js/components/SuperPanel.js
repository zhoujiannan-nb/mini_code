import { h, Fragment } from "preact";
import { useEffect, useState } from "preact/hooks";
import htm from "htm";

import { ROUTES, getJSON, postJSON } from "../lib/api.js";
import { useStore, setState, getState, toast } from "../lib/store.js";
import { relTime } from "../lib/format.js";
import { selectSession } from "../lib/actions.js";

const html = htm.bind(h);

function clock(t) { 
  if (!t) return "—";
  const d = new Date(t);
  if (isNaN(d)) return "—";
  return d.toLocaleTimeString("zh-CN", { hour12: false });
}
function full(t) { return t ? String(t).replace("T", " ").replace(/\.\d+.*$/, "") : ""; }
function everyHuman(sec) {
  sec = Number(sec) || 0;
  if (sec >= 86400 && sec % 86400 === 0) return sec / 86400 + "d";
  if (sec >= 3600 && sec % 3600 === 0) return sec / 3600 + "h";
  if (sec >= 60 && sec % 60 === 0) return sec / 60 + "m";
  return sec + "s";
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
async function loadOv() {
  try {
    const data = await getJSON(ROUTES.superOverview);
    setState({ superOv: data });
  } catch (e) {  }
}


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
  { id: "sched", label: "定时任务" },
  { id: "sessions", label: "会话" },
  { id: "audit", label: "审计轨迹" },
  { id: "chat", label: "Super对话" },
];


export function SuperFrame({ children }) {
  const role = useStore((s) => s.currentRole);
  const tab = useStore((s) => s.superTab) || "sched";
  const ov = useStore((s) => s.superOv);

  useEffect(() => {
    if (role !== "super") return undefined;
    loadOv();
    const t = setInterval(loadOv, 3000);
    return () => clearInterval(t);
  }, [role]);

  const drawerId = useStore((s) => (s.superDrawer ? s.superDrawer.id : ""));
  useEffect(() => {
    if (!drawerId) return undefined;
    const t = setInterval(refreshSuperDrawer, 3000);
    return () => clearInterval(t);
  }, [drawerId]);

  if (role !== "super") return children;

  const enabled = ov ? (ov.schedules || []).filter((x) => x.enabled).length : 0;

  return html`
    <div class="sup-banner">
      <span class="sb-bolt">⚡</span>
      <b class="sb-title">Super-Agent</b>
      <span class="sb-stats">任务 ${ov ? (ov.schedules || []).length : 0} · 启用 ${enabled}</span>
      <span class="sup-tabs-flex"></span>
      <span class="sup-hint" title="runner=minicode 常驻定时器(tick 15s):到点把写好的话投给绑定会话">
        ${(ov && ov.runner_started_at) ? "runner " + clock(ov.runner_started_at) + " 起" : "runner —"}
      </span>
    </div>
    <div class="sup-tabs">
      ${TABS.map((t) => html`
        <button key=${t.id} class=${"sup-tab" + (tab === t.id ? " on" : "")}
          onClick=${() => setState({ superTab: t.id })}>
          ${t.label}
          ${t.id === "sched" && enabled ? html`<span class="sup-badge">${enabled}</span>` : null}
        </button>`)}
      <span class="sup-tabs-flex"></span>
    </div>
    ${tab === "chat" ? children : html`
      <div class="sup-scroll">
        ${tab === "sched" ? html`<${SchedPanel} ov=${ov}/>` : null}
        ${tab === "sessions" ? html`<${SessionsPanel} ov=${ov}/>` : null}
        ${tab === "audit" ? html`<${AuditPanel} ov=${ov}/>` : null}
      </div>`}
    <${SuperDrawer}/>`;
}


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
          <div class="sd-role">${r.role === "user" ? "人类/来件" : "会话"}</div>
          <div class="sd-text">${r.text}</div>
        </div>`)}
      </div>
    </div>
  </div>`;
}


function SchedPanel({ ov }) {
  const [open, setOpen] = useState(false);
  const [f, setF] = useState({ text: "", name: "", session: "", n: 60, unit: "m" });
  if (!ov) return html`<div class="sup-empty">加载中…</div>`;
  const unitSec = { s: 1, m: 60, h: 3600, d: 86400 };
  const set = (k, v) => setF(Object.assign({}, f, { [k]: v }));
  const create = () => {
    const text = (f.text || "").trim();
    if (!text) { toast("到点要发的那句话必填", "err"); return; }
    const everySec = Math.max(30, Math.round(Number(f.n) || 60) * unitSec[f.unit]);
    post(ROUTES.superSchedCreate, {
      name: (f.name || "").trim(), text,
      session: f.session === "__self" ? "" : f.session,
      every_sec: everySec,
    }, "定时任务已建立(每 " + everyHuman(everySec) + ")");
    setF({ text: "", name: "", session: f.session, n: f.n, unit: f.unit });
  };
  const sessTitle = (id) => {
    const x = (ov.sessions || []).find((s) => s.id === id);
    return x ? (x.title || x.id) : id;
  };
  const list = ov.schedules || [];
  return html`
    <div class="sup-cfg">
      <button class="sup-cfg-head" onClick=${() => setOpen(!open)}>
        <span class="sc-title">新建定时任务</span>
        <span class="sc-chips"><span class="sc-chip">最短 30s · 最长 30d</span></span>
        <span class="sc-caret">${open ? "▲" : "▼"}</span>
      </button>
      ${open ? html`<div class="sup-cfg-body">
        <label class="wide">到点发的那句话(原样投递,不做任何加工)
          <textarea rows="2" placeholder="例:该起来喝水走一圈了。" value=${f.text} onInput=${(e) => set("text", e.target.value)}></textarea>
        </label>
        <label>短名<input type="text" placeholder="留空 = 取那句话开头" value=${f.name} onInput=${(e) => set("name", e.target.value)}/></label>
        <label>绑定会话<select value=${f.session} onInput=${(e) => set("session", e.target.value)}>
          <option value="__self">Super 会话自己(默认)</option>
          ${(ov.sessions || []).map((x) => html`
            <option key=${x.id} value=${x.id}>${x.title || "(无题)"} · ${x.id.slice(0, 8)}…</option>`)}
        </select></label>
        <label>每<select value=${f.unit} onInput=${(e) => set("unit", e.target.value)}>
          <option value="m">分钟</option><option value="h">小时</option><option value="d">天</option><option value="s">秒</option>
        </select></label>
        <label>间隔数<input type="number" min="1" value=${f.n} onInput=${(e) => set("n", e.target.value)}/></label>
        <button class="btn" onClick=${create}>建任务</button>
      </div>` : null}
    </div>
    <table class="sup-table">
      <thead><tr><th></th><th>到点发的话</th><th>间隔</th><th>发到</th><th>上次</th><th></th></tr></thead>
      <tbody>
        ${list.length === 0 ? html`<tr><td colspan="6" class="sup-empty">还没有定时任务 —— 展开「新建定时任务」建第一个</td></tr>` : null}
        ${list.map((sc) => {
          const self = !sc.session || sc.session === ov.super_session_id;
          return html`<tr key=${sc.id} class=${sc.enabled ? "" : " off"}>
            <td><button class=${"sup-sw" + (sc.enabled ? " on" : "")} title=${sc.enabled ? "暂停" : "恢复"}
              onClick=${() => post(ROUTES.superSchedToggle, { id: sc.id })}><span></span></button></td>
            <td title=${sc.text}>
              <b>${sc.name || sc.id}</b>
              <div class="sup-wmeta">${sc.text}</div>
            </td>
            <td>${everyHuman(sc.every_sec)}</td>
            <td>${self
              ? html`<span class="sup-tag" title="到点发进 Super 会话本身">Super 自己</span>`
              : html`<button class="sup-sess" title=${sc.session}
                  onClick=${() => openSuperDrawer(sc.session, sessTitle(sc.session))}>
                  ${(sessTitle(sc.session) || "").slice(0, 14)}…
                </button>`}</td>
            <td title=${full(sc.last_run)}>${sc.last_run ? relTime(sc.last_run) : "未跑"}</td>
            <td>
              <button class="sup-linkbtn" onClick=${() => post(ROUTES.superSchedRun, { id: sc.id }, "已立即投递一次")}>发一次</button>
              <button class="sup-linkbtn" onClick=${() => { if (confirm("删除定时任务「" + (sc.name || sc.id) + "」?")) post(ROUTES.superSchedDelete, { id: sc.id }, "已删除"); }}>删</button>
            </td>
          </tr>`;
        })}
      </tbody>
    </table>`;
}


function SessionsPanel({ ov }) {
  if (!ov) return null;
  const bound = {};
  (ov.schedules || []).forEach((sc) => { if (sc.session) bound[sc.session] = (bound[sc.session] || 0) + 1; });
  const rows = (ov.sessions || []).filter((x) => !x.id || x.id !== ov.super_session_id);
  return html`
    <div class="sup-note">这里速览所有可绑定的普通会话(点抽屉预览,不打扰对方)。「整页打开」会切走面板;
      绑定给任务的会话仍留在侧栏——面板不抢走人类的原入口。</div>
    <table class="sup-table">
      <thead><tr><th>会话</th><th>状态</th><th>绑定任务</th><th></th></tr></thead>
      <tbody>
        ${rows.length === 0 ? html`<tr><td colspan="4" class="sup-empty">还没有普通会话</td></tr>` : null}
        ${rows.map((x) => html`<tr key=${x.id}>
          <td class="mono" title=${x.id}><b>${x.title || "(无题)"}</b></td>
          <td>${x.status === "active" ? html`<span class="sup-dot on"></span>活跃` : "空闲/关闭"}</td>
          <td>${bound[x.id] ? html`<span class="sup-tag task">${bound[x.id]} 个</span>` : html`<span class="sup-wmeta">—</span>`}</td>
          <td><button class="sup-linkbtn" onClick=${() => openSuperDrawer(x.id, x.title || x.id)}>速览</button></td>
        </tr>`)}
      </tbody>
    </table>`;
}


function AuditPanel({ ov }) {
  if (!ov) return null;
  const rows = ov.audits || [];
  const ACT = {
    "super.boot": "启动", "super.session": "建会话",
    "schedule.create": "建任务", "schedule.delete": "删任务", "schedule.toggle": "开关",
    "schedule.run_now": "手动触发", "schedule.fire": "到点投递", "schedule.fail": "投递失败",
  };
  return html`
    <div class="sup-note">审计只增不改:谁(actor)在什么时候对什么(target)干了什么(action)。</div>
    <table class="sup-table">
      <thead><tr><th>时间</th><th>谁</th><th>干了啥</th><th>详情</th></tr></thead>
      <tbody>
        ${rows.length === 0 ? html`<tr><td colspan="4" class="sup-empty">还没有记录</td></tr>` : null}
        ${rows.map((a) => html`<tr key=${a.id}>
          <td class="mono" title=${a.ts}>${(a.ts || "").slice(5, 16)}</td>
          <td>${a.actor}</td>
          <td><span class=${"sup-tag " + (a.action.endsWith(".fail") ? "imm" : "task")}>${ACT[a.action] || a.action}</span></td>
          <td class="mono" title=${a.target}>${a.detail}</td>
        </tr>`)}
      </tbody>
    </table>`;
}
