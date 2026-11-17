import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { useStore, getState, toggleTrajPanel } from "../lib/store.js";
import { ROUTES, api } from "../lib/api.js";
import { fmtDurMs, fmtK2 } from "../lib/format.js";

const html = htm.bind(h);

const KIND = {
  task: { tag: "任务", color: "#f59e0b" },
  assistant: { tag: "助手", color: "#8b5cf6" },
  tool: { tag: "工具", color: "#16a34a" },
  compact: { tag: "压缩", color: "#ef4444" },
  prune: { tag: "清理", color: "#f97316" },
  switch: { tag: "换模型", color: "#06b6d4" },
};
const FILTERS = [
  ["", "全部"], ["task", "任务"], ["assistant", "助手"], ["tool", "工具"], ["compact", "压缩"],
];

function fmtTime(ms) {
  try {
    return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  } catch (e) { return ""; }
}

export function TrajPanel() {
  const open = useStore((s) => s.trajOpen);
  const current = useStore((s) => s.current);
  const running = useStore((s) => s.running);
  const [data, setData] = useState(null);   
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const [more, setMore] = useState(false);
  const [filter, setFilter] = useState("");
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(null);     
  const listRef = useRef(null);

  useEffect(() => {
    if (!open || !current) return;
    setLoading(true); setErr("");
    api(ROUTES.trajectory(current)).then((d) => {
      setData(d); setSel(null);
    }).catch((e) => setErr(e.message)).finally(() => setLoading(false));
  }, [open, current]);
  const prevRun = useRef(running);
  useEffect(() => {
    if (prevRun.current && !running && open && current && getState().trajOpen) {
      api(ROUTES.trajectory(current)).then((d) => setData(d)).catch(() => {});
    }
    prevRun.current = running;
  }, [running, open, current]);
  useEffect(() => { setFilter(""); setQuery(""); setSel(null); }, [current]);

  const loadMore = () => {
    const rows = (data && data.rows) || [];
    if (!rows.length || !data.has_more || more) return;
    setMore(true);
    api(ROUTES.trajectory(current, { before: rows[0].seq })).then((d) => {
      setData((c) => (c ? { ...c, rows: (d.rows || []).concat(c.rows), has_more: !!d.has_more } : c));
    }).catch((e) => setErr(e.message)).finally(() => setMore(false));
  };

  const rowsAll = ((data && data.rows) || []).slice().reverse(); 
  const q = query.trim().toLowerCase();
  const rows = rowsAll.filter((r) => {
    if (filter === "compact" ? (r.kind !== "compact" && r.kind !== "prune") : (filter && r.kind !== filter)) return false;
    if (q && !((r.title || "") + " " + (r.sub || "")).toLowerCase().includes(q)) return false;
    return true;
  });

  const tl = rowsAll.filter((r) => KIND[r.kind]);
  let maxW = 1;
  for (const r of tl) { const w = Math.sqrt(Math.max(r.dur_ms || 0, 200)); if (w > maxW) maxW = w; }

  return html`<aside class=${"traj-panel" + (open ? " open" : "")} id="traj-panel"
    aria-hidden=${open ? "false" : "true"}>
    <div class="ctx-head">
      <span class="ctx-title">轨迹</span>
      ${data ? html`<span class="tr-sum">${data.steps || 0} 步 · ${data.rounds || 0} 轮</span>` : null}
      <span class="sh-flex"></span>
      <button class="sh-btn" id="traj-close" title="收起面板" onClick=${() => toggleTrajPanel()}>×</button>
    </div>
    <div class="ctx-body traj-body">
      <div class="traj-inner">
      ${!current ? html`<div class="rail-empty">新会话还没有轨迹,发第一条消息后来看。</div>` : null}
      ${current && loading && !data ? html`<div class="rail-empty">加载轨迹…</div>` : null}
      ${err ? html`<div class="rail-empty">轨迹加载失败:${err}</div>` : null}
      ${data && tl.length ? html`
        <div class="tr-tl" title="一步一块:色=类型,宽≈耗时">
          ${tl.map((r) => html`<i key=${r.seq} class="tr-blk k-${r.kind}"
            style=${{ width: (6 + 34 * Math.sqrt(Math.max(r.dur_ms || 0, 200)) / maxW) + "px",
                       background: (KIND[r.kind] || KIND.tool).color }}
            title=${(KIND[r.kind] || { tag: r.kind }).tag + " · " + (r.title || "")
              + (r.dur_ms ? " · " + fmtDurMs(r.dur_ms) : "") + (r.tokens ? " · " + fmtK2(r.tokens) + " tok" : "")}></i>`)}
        </div>
        <div class="tr-filters">
          ${FILTERS.map(([k, label]) => html`
            <button key=${k} class=${"tr-chip" + (filter === k ? " on" : "")}
              onClick=${() => setFilter(k)}>${label}</button>`)}
          <input class="tr-search" placeholder="搜索…" value=${query}
            onInput=${(e) => setQuery(e.target.value)} />
        </div>
        ${data.has_more ? html`<button class="cd-more" id="traj-more" disabled=${more}
          onClick=${loadMore}>${more ? "加载中…" : "加载更早的轨迹"}</button>` : null}
        <div class="tr-rows" ref=${listRef}>
          ${rows.length ? rows.map((r) => {
            const k = KIND[r.kind] || KIND.tool;
            const meta = [r.dur_ms ? fmtDurMs(r.dur_ms) : "", r.tokens ? fmtK2(r.tokens) + " tok" : "",
                          r.round ? "第" + r.round + "轮" : "", r.at ? fmtTime(Date.parse(r.at)) : ""].filter(Boolean).join(" · ");
            return html`<div key=${r.seq} class=${"tr-row k-" + r.kind + (sel === r.seq ? " on" : "")}>
              <button class="tr-main" onClick=${() => setSel(sel === r.seq ? null : r.seq)}>
                <span class="tr-tag" style=${{ background: k.color }}>${k.tag}</span>
                <span class="tr-title">${r.title || ""}${r.sub && sel !== r.seq ? html` <span class="tr-sub">${r.sub}</span>` : null}</span>
                <span class="tr-meta">${meta}</span>
              </button>
              ${sel === r.seq && r.sub ? html`<pre class="tr-pre">${r.sub}</pre>` : null}
            </div>`;
          }) : html`<div class="rail-empty">${query || filter ? "没有匹配的轨迹行。" : "还没有轨迹。"}</div>`}
        </div>` : null}
      </div>
    </div>
  </aside>`;
}
