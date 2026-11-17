import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { useStore, toggleCtxPanel, toggleSummaryPanel, toggleTrajPanel } from "../lib/store.js";
import { loadContext } from "../lib/actions.js";
import { ROUTES, api } from "../lib/api.js";
import { fmtK2 } from "../lib/format.js";

const html = htm.bind(h);

const SEC_COLOR = {
  system: "#94a3b8", tools: "#8b5cf6", user: "#2f6fd6",
  assistant: "#16a34a", tool: "#f59e0b",
};
const KIND_META = {
  compact: ["压缩", "c"], prune: ["清理", "p"], switch: ["换模型", "s"], mode: ["模式", "m"],
};
const SEC_DETAIL = {
  system: ["system", ""], tools: ["tools", ""],
  user: ["messages", "user"], assistant: ["messages", "assistant"], tool: ["messages", "tool"],
};

function fmtTime(ms) {
  try {
    return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  } catch (e) { return ""; }
}

export function ContextPanel() {
  const open = useStore((s) => s.ctxOpen);
  const current = useStore((s) => s.current);
  const info = useStore((s) => s.ctxInfo);
  const running = useStore((s) => s.running);
  const [loading, setLoading] = useState(false);
  const [detail, setDetail] = useState(null);
  const [msgFull, setMsgFull] = useState(null);
  const [roundSel, setRoundSel] = useState(null);

  useEffect(() => {
    if (!open || !current) return;
    setLoading(true);
    loadContext().finally(() => setLoading(false));
  }, [open, current]);
  const prevRun = useRef(running);
  useEffect(() => {
    if (prevRun.current && !running && open && current) loadContext();
    prevRun.current = running;
  }, [running, open, current]);
  useEffect(() => { setDetail(null); setMsgFull(null); setRoundSel(null); }, [current]);

  const openDetail = (secKey) => {
    if (detail && detail.key === secKey) { setDetail(null); setMsgFull(null); return; }
    const spec = SEC_DETAIL[secKey];
    if (!spec || !current) return;
    const [part, role] = spec;
    setDetail({ key: secKey, part, role, data: null, loading: true, err: "", more: false });
    setMsgFull(null);
    const url = part === "messages"
      ? ROUTES.contextDetail(current, part, { role })
      : ROUTES.contextDetail(current, part);
    api(url).then((d) => {
      setDetail((cur) => (cur && cur.key === secKey ? { ...cur, data: d, loading: false } : cur));
    }).catch((e) => {
      setDetail((cur) => (cur && cur.key === secKey ? { ...cur, loading: false, err: e.message } : cur));
    });
  };

  const loadMoreMsgs = () => {
    const cur = detail;
    if (!cur || !cur.data || cur.more) return;
    const msgs = cur.data.messages || [];
    if (!msgs.length || !cur.data.has_more) return;
    const oldest = msgs[0].index;
    setDetail({ ...cur, more: true });
    api(ROUTES.contextDetail(current, "messages", { role: cur.role, before: oldest })).then((d) => {
      setDetail((c) => (c && c.key === cur.key ? {
        ...c, more: false,
        data: {
          ...c.data,
          messages: (d.messages || []).concat(msgs),
          has_more: !!d.has_more,
        },
      } : c));
    }).catch((e) => {
      setDetail((c) => (c && c.key === cur.key ? { ...c, more: false, err: e.message } : c));
    });
  };

  const openMsg = (index) => {
    if (msgFull && msgFull.index === index) { setMsgFull(null); return; }
    if (!current) return;
    setMsgFull({ index, data: null, loading: true, err: "" });
    api(ROUTES.contextDetail(current, "messages", { i: index })).then((d) => {
      setMsgFull((cur) => (cur && cur.index === index ? { index, data: d, loading: false, err: "" } : cur));
    }).catch((e) => {
      setMsgFull((cur) => (cur && cur.index === index ? { index, data: null, loading: false, err: e.message } : cur));
    });
  };

  const d = info;
  const pct = d && d.window > 0 ? Math.min(100, Math.round((d.tokens || 0) * 100 / d.window)) : 0;
  const secs = (d && d.sections) || [];
  const secSum = secs.reduce((a, s) => a + (s.tokens || 0), 0);
  const free = d && d.window > 0 ? Math.max(0, d.window - secSum) : 0;
  const totals = d && d.totals;
  const rounds = (d && d.rounds) || [];
  const roundsDesc = rounds.slice().reverse();
  const selRound = roundSel == null ? null : rounds.find((r) => r.round === roundSel) || null;
  const evtsAll = ((d && d.events) || []).slice().reverse();
  const evts = !selRound ? evtsAll : evtsAll.filter((ev) =>
    !ev.round || ev.round === selRound.round);

  const detailMsgs = detail && detail.part === "messages" && detail.data
    ? (detail.data.messages || []).slice().reverse()
    : null;

  return html`<aside class=${"ctx-panel" + (open ? " open" : "")} id="ctx-panel"
    aria-hidden=${open ? "false" : "true"}>
    <div class="ctx-head">
      <span class="ctx-title">上下文</span>
      <span class=${"ctx-pct" + (pct >= 80 ? " warn" : "")}>${pct}%</span>
      <span class="sh-flex"></span>
      <!-- v3 样图:聊天头右上角只留四颗,摘要/轨迹的入口搬进抽屉头。 -->
      <button class="sh-btn rail-view" id="rail-sum" title="切到执行摘要抽屉"
        onClick=${() => toggleSummaryPanel()}>摘要</button>
      <button class="sh-btn rail-view" id="rail-traj" title="切到轨迹抽屉(逐步执行流)"
        onClick=${() => toggleTrajPanel()}>轨迹</button>
      <button class="sh-btn" id="ctx-refresh" title="重新拉取快照"
        onClick=${() => { setLoading(true); loadContext().finally(() => setLoading(false)); }}>⟳</button>
      <button class="sh-btn" id="ctx-close" title="收起面板"
        onClick=${() => toggleCtxPanel()}>×</button>
    </div>
    <div class="ctx-body">
      ${!current ? html`<div class="rail-empty">新会话还没有上下文,发第一条消息后来看。</div>` : null}
      ${current && loading && !d ? html`<div class="rail-empty">加载中…</div>` : null}
      ${current && d ? html`
        <div class="ctx-usage">
          <div class="cu-bar" title=${(d.tokens || 0).toLocaleString() + " / " + (d.window || 0).toLocaleString()}>
            <i style=${{ width: pct + "%" }}></i>
          </div>
          <div class="cu-sub">${d.est ? "≈" : ""}${fmtK2(d.tokens || 0)} / ${d.window ? fmtK2(d.window) : "—"} tokens</div>
        </div>
        <div class="rail-sec">
          <div class="rail-sec-title">组成<span class="rs-hint">点一行看详情</span></div>
          <div class="ctx-stack">
            ${secs.map((s) => html`<i key=${s.key} class="cs-seg" style=${{
              width: d.window > 0 ? (s.tokens || 0) * 100 / d.window + "%" : "0%",
              background: SEC_COLOR[s.key] || "#cbd5e1",
            }} title=${s.label + " ≈" + fmtK2(s.tokens || 0)}></i>`)}
            ${free > 0 ? html`<i class="cs-seg cs-free" style=${{ width: free * 100 / d.window + "%" }}
              title=${"剩余 ≈" + fmtK2(free)}></i>` : null}
          </div>
          <ul class="ctx-legend">
            ${secs.map((s) => html`<li key=${s.key} class=${"cl-row" + (detail && detail.key === s.key ? " on" : "")}
              title="点击${detail && detail.key === s.key ? "收起" : "展开"}${s.label}详情"
              onClick=${() => openDetail(s.key)}>
              <i style=${{ background: SEC_COLOR[s.key] || "#cbd5e1" }}></i>
              <span class="cl-label">${s.label}</span>
              <span class="cl-n">${fmtK2(s.tokens || 0)}</span>
              <span class="cl-pct">${d.window > 0 ? Math.round((s.tokens || 0) * 100 / d.window) : 0}%</span>
            </li>`)}
          </ul>
          ${detail ? html`<div class="ctx-detail" id="ctx-detail">
            ${detail.loading ? html`<div class="rail-empty">加载详情…</div>` : null}
            ${detail.err ? html`<div class="rail-empty">详情加载失败:${detail.err}</div>` : null}
            ${!detail.loading && detail.part === "system" && detail.data ? (
              detail.data.parts && detail.data.parts.length ? html`
                <div class="cd-parts">
                  ${detail.data.parts.map((p, i) => html`<details class="cd-part" key=${i}>
                    <summary>
                      <span class=${"cdp-tag" + (p.injected ? " inj" : "")}>${p.injected ? "注入" : "系统"}</span>
                      <b>${p.label}</b><span class="cd-n">${fmtK2(p.tokens || 0)}</span>
                    </summary>
                    <pre class="cd-pre">${p.text}</pre>
                  </details>`)}
                </div>`
                : (detail.data.text
                  ? html`<pre class="cd-pre">${detail.data.text}</pre>`
                  : html`<div class="rail-empty">${detail.data.note || "当前上下文里没有 system 消息(通常出现在压缩重建后)。工具定义见上一行紫色段。"}</div>`)) : null}
            ${!detail.loading && detail.part === "tools" && detail.data ? html`
              <ul class="cd-tools">
                ${(detail.data.tools || []).map((t, i) => html`<li key=${t.name || i}>
                  <b>${t.name}</b><span class="cd-n">${fmtK2(t.tokens || 0)}</span>
                  <div class="cd-desc">${t.description || ""}</div>
                </li>`)}
              </ul>
              ${!(detail.data.tools || []).length ? html`<div class="rail-empty">${detail.data.note || "这个会话还没配置工具。"}</div>` : null}` : null}
            ${!detail.loading && detailMsgs ? (detailMsgs.length ? html`
              ${detail.data.has_more ? html`<button class="cd-more" id="cd-more"
                disabled=${detail.more} onClick=${loadMoreMsgs}>${detail.more ? "加载中…" : "加载更早的消息"}</button>` : null}
              <ul class="cd-msgs">
                ${detailMsgs.map((m) => html`<li key=${m.index}>
                  <button class="cd-msg" onClick=${() => openMsg(m.index)}>
                    <span class="cm-role ${m.role}">${m.role}</span>
                    <span class="cm-n">${m.est ? "≈" : ""}${fmtK2(m.tokens || 0)}</span>
                    <span class="cm-prev">${m.preview || "(空)"}</span>
                  </button>
                  ${msgFull && msgFull.index === m.index ? (msgFull.loading
                    ? html`<div class="rail-empty">加载全文…</div>`
                    : html`<pre class="cd-pre">${(msgFull.data && (msgFull.data.text + (msgFull.data.truncated ? "\n…(已截断)" : ""))) || msgFull.err || ""}</pre>`) : null}
                </li>`)}
              </ul>` : html`<div class="rail-empty">这一类消息还没有过。</div>`) : null}
          </div>` : null}
        </div>
        <div class="rail-sec">
          <div class="rail-sec-title">累计用量</div>
          ${totals ? html`<div class="rail-kv">
            <div class="kv-row"><span class="kv-k">总上下文</span><span class="kv-v">${fmtK2(totals.input)}</span></div>
            <div class="kv-row"><span class="kv-k">总输出</span><span class="kv-v">${fmtK2(totals.output)}</span></div>
            <div class="kv-row"><span class="kv-k">缓存命中</span><span class="kv-v">${fmtK2(totals.cached)}</span></div>
            <div class="kv-row"><span class="kv-k">未命中</span><span class="kv-v">${
              !totals.reported ? "—" : fmtK2(Math.max(0, totals.uncached))}</span></div>
            <div class="kv-row"><span class="kv-k">命中率</span><span class="kv-v">${
              !totals.reported ? "未上报" : totals.input > 0 ? Math.round(totals.cached * 100 / totals.input) + "%" : "—"}</span></div>
            <div class="kv-row"><span class="kv-k">请求数</span><span class="kv-v">${totals.requests}</span></div>
          </div>` : html`<div class="rail-empty">还没有用量数据。</div>`}
        </div>
        <div class="rail-sec">
          <div class="rail-sec-title">轮次审计
            <span class="rs-n">${rounds.length} 轮 · ${d.steps || 0} 步</span>
          </div>
          ${rounds.length ? html`<ul class="ctx-events ctx-rounds">
            ${roundsDesc.map((r) => html`<li key=${r.round}
              class=${"cr-row" + (roundSel === r.round ? " on" : "")}
              title=${"第" + r.round + "轮:" + r.steps + " 步"
                + (r.compacted ? (r.reclaimed ? ",压缩回收 " + fmtK2(r.reclaimed) : ",已压缩(回收<1K)") : "(仍在进行)")}
              onClick=${() => setRoundSel(roundSel === r.round ? null : r.round)}>
              <span class=${"ce-kind"}>第${r.round}轮</span>
              <span class="ce-main">${r.steps} 步 · 顶到 ${fmtK2(r.peak_tokens || 0)}${
                r.compacted ? (r.reclaimed ? " · 压缩 −" + fmtK2(r.reclaimed) : " · 已压缩") : " · 进行中"}</span>
              <span class="ce-when">${r.at ? fmtTime(r.at) : ""}</span>
            </li>`)}
          </ul>` : html`<div class="rail-empty">还没压缩过 —— 整个会话仍在第 1 轮。</div>`}
        </div>
        <div class="rail-sec">
          <div class="rail-sec-title">上下文事件<span class="rs-n">${evts.length}</span>${
            selRound ? html`<span class="rs-filter" title="点击清除过滤" onClick=${() => setRoundSel(null)}>第${selRound.round}轮 ×</span>` : null}
          </div>
          ${evts.length ? html`<ul class="ctx-events">
            ${evts.map((ev, i) => {
              const [label, cls] = KIND_META[ev.kind] || [ev.kind, "m"];
              return html`<li key=${i} class=${"ce-" + cls}>
                <span class="ce-kind">${label}</span>
                <span class="ce-main">${ev.detail || ""}${ev.tokens ? " · 回收 " + fmtK2(ev.tokens) : ""}</span>
                <span class="ce-when">${ev.round ? "第" + ev.round + "轮 " : ""}${ev.at ? fmtTime(ev.at) : ""}</span>
              </li>`;
            })}
          </ul>` : html`<div class="rail-empty">${selRound ? "这一轮没有压缩/清理事件。" : "还没有压缩/清理事件。"}</div>`}
        </div>` : null}
    </div>
  </aside>`;
}
