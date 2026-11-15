// 会话「轨迹」视图 —— dsh 式执行轨迹:整块覆盖聊天区(与聊天互相盖,像同一
// 舞台的两块幕布):顶部一条时间线(一步一块,按类型着色、宽度 ∝ 耗时),
// 下面逐步事件流:任务输入 / 助手回复 / 工具调用(参数摘要 → 结果摘要)/
// 压缩 / 清理,可点行展开全貌。过滤芯片 + 搜索都是客户端的
// (数据一页 ≤300 行,翻「更早」是服务端分页)。
// 数据:GET /api/sessions/{id}/trajectory(见 web/context_api.go 同包 handler)。
// 开轨迹会收起「上下文」「摘要」抽屉(见 store.toggleTrajPanel)。
import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { useStore, getState, toggleTrajPanel } from "../lib/store.js";
import { ROUTES, api } from "../lib/api.js";
import { fmtDurMs, fmtK2 } from "../lib/format.js";

const html = htm.bind(h);

// KIND 元数据:标签/时间线色/过滤芯片归类。
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
  const [data, setData] = useState(null);   // {rows, has_more, steps, rounds}
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");
  const [more, setMore] = useState(false);
  const [filter, setFilter] = useState("");
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(null);     // 展开的 seq
  const listRef = useRef(null);

  // 打开 / 切会话拉最新一页;每轮任务结束再刷一次。
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

  const rowsAll = ((data && data.rows) || []).slice().reverse(); // 最新在前
  const q = query.trim().toLowerCase();
  const rows = rowsAll.filter((r) => {
    if (filter === "compact" ? (r.kind !== "compact" && r.kind !== "prune") : (filter && r.kind !== filter)) return false;
    if (q && !((r.title || "") + " " + (r.sub || "")).toLowerCase().includes(q)) return false;
    return true;
  });

  // 时间线:每条一行一个小块,宽度 ∝ √耗时(长任务不至于吃光一行)。
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
