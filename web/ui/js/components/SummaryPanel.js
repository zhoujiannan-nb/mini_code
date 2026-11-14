// 右侧「执行摘要」栏:当前会话的一张成绩单。
//
// 只画有真实数据支撑的东西 —— 状态 / 耗时、最近一份待办清单、本轮动过的文件、
// token/缓存/速度。没有数据的小节直接不画,不编数字。
// 数据全部来自 store(历史步骤 + 实时步骤),本组件不打任何后端接口
// (点文件除外:走 markdown.js 的 openFilePath → POST /api/fs/open)。
import { h } from "preact";
import htm from "htm";

import { useStore } from "../lib/store.js";
import { TodoCard } from "./TodoCard.js";
import { todoItemsOf, toolArgsOf } from "../lib/tools.js";
import { fmtK2, paintCacheHit, paintSpeed, fmtTaskDur } from "../lib/format.js";
import { openFilePath } from "../lib/markdown.js";

const html = htm.bind(h);

// 哪些工具算"动过文件":dsh 名 + 历史名。read 也计入 —— "读了哪些文件"同样是线索。
const FILE_TOOLS = new Set(["read", "write", "edit", "read_image",
  "read_file", "write_file", "edit_file"]);
const FILE_KEYS = ["file_path", "path", "file", "target", "dest"];

// collectRun(rows, liveSteps):把历史轮 + 当前轮的过程步骤汇成一份摘要输入。
export function summarize(rows, liveSteps) {
  let todos = null;
  const files = [];
  const seen = new Set();
  let steps = 0;
  const visit = (list) => {
    for (const s of list || []) {
      if (s.kind === "think") continue;
      steps += 1;
      if (!todos) todos = todoItemsOf(s.name, s.args);
      if (!FILE_TOOLS.has(String(s.name || "").toLowerCase())) continue;
      const o = toolArgsOf(s.args);
      if (!o) continue;
      for (const k of FILE_KEYS) {
        const v = o[k];
        if (typeof v === "string" && v.trim() && !seen.has(v)) {
          seen.add(v);
          files.push(v.trim());
        }
        break; // 一个调用只认第一个路径键
      }
    }
  };
  // 从最新一轮往回找:待办要的是"最后一份计划",倒着走一次就够。
  const arr = rows || [];
  for (let i = arr.length - 1; i >= 0; i--) {
    const r = arr[i];
    if (r.steps) visit(r.steps);
  }
  visit(liveSteps);
  return { todos, files, steps };
}

// 状态文案与色档:与会话列表同一套语义。
function statusOf(s, running) {
  if (!s) return { key: "new", text: "等待第一条消息", note: "" };
  if (running || s.active) return { key: "running", text: "任务执行中", note: "" };
  if (s.children_active) return { key: "running", text: "子任务执行中", note: "" };
  if (s.status === "completed") return { key: "done", text: "任务已完成", note: "" };
  if (s.status === "failed") return { key: "failed", text: "任务失败", note: "" };
  if (s.status === "interrupted") return { key: "stopped", text: "任务已中断", note: "" };
  return { key: "idle", text: "空闲", note: "" };
}

function tailName(p) {
  const segs = String(p || "").replace(/\\/g, "/").split("/").filter(Boolean);
  return segs.slice(-2).join("/");
}

export function SummaryPanel() {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const rows = useStore((s) => s.rows);
  const runSteps = useStore((s) => s.run.steps);
  const running = useStore((s) => s.running);
  const tokens = useStore((s) => s.tokens);
  const ctxWindow = useStore((s) => s.ctxWindow);
  const cacheHit = useStore((s) => s.cacheHit);
  const speed = useStore((s) => s.speed);
  const speedAccurate = useStore((s) => s.speedAccurate);

  if (!current) return null;
  const sess = (sessions || []).find((x) => x.session_id === current);
  const st = statusOf(sess, running);
  const dur = sess ? fmtTaskDur(sess, Date.now()) : "";
  const sum = summarize(rows, runSteps);

  const cache = paintCacheHit(cacheHit && cacheHit.prompt, cacheHit && cacheHit.cached, cacheHit && cacheHit.reported);
  const speedText = paintSpeed(speed, speedAccurate);
  const ctxPct = ctxWindow > 0 ? Math.min(100, Math.round((tokens || 0) * 100 / ctxWindow)) : 0;
  const MAX_FILES = 12;
  const files = sum.files.slice(0, MAX_FILES);
  const moreFiles = sum.files.length - files.length;

  return html`<aside class="rail" id="summary-rail">
    <div class="rail-head">
      <span class="rail-title">执行摘要</span>
      <span class=${"rail-chip " + st.key}>
        ${st.key === "running" ? html`<span class="spin"></span>` : (st.key === "done" ? "✓" : (st.key === "failed" ? "✕" : ""))}
        ${st.text}
      </span>
    </div>

    <div class="rail-body">
      <div class="rail-hero">
        <div class=${"hero-ico " + st.key}>${st.key === "running" ? html`<span class="spinner"></span>` : (st.key === "done" ? "✓" : (st.key === "failed" ? "!" : "–"))}</div>
        <div class="hero-main">${st.text}</div>
        <div class="hero-sub">${dur ? "耗时 " + dur : ""}${sess && sess.message_count ? " · " + sess.message_count + " 条消息" : ""}${sum.steps ? " · 已执行 " + sum.steps + " 步" : ""}</div>
      </div>

      <div class="rail-sec">
        <div class="rail-sec-title">执行结果</div>
        ${sum.todos
          ? html`<${TodoCard} todos=${sum.todos} />`
          : html`<div class="rail-empty">本轮还没有待办清单 —— agent 用 todo_write 列计划后,这里会跟着走。</div>`}
      </div>

      <div class="rail-sec">
        <div class="rail-sec-title">相关文件${sum.files.length ? html` <span class="rs-n">${sum.files.length}</span>` : null}</div>
        ${files.length ? html`<ul class="rail-files">
          ${files.map((p) => html`<li key=${p} title=${p} onClick=${() => openFilePath(p)}>
            <span class="rf-ico">🗎</span><span class="rf-name">${tailName(p)}</span>
          </li>`)}
          ${moreFiles > 0 ? html`<li class="rf-more">还有 ${moreFiles} 个文件</li>` : null}
        </ul>` : html`<div class="rail-empty">这一步还没有读写过文件。</div>`}
      </div>

      <div class="rail-sec">
        <div class="rail-sec-title">性能指标</div>
        <div class="rail-kv">
          <div class="kv-row"><span class="kv-k">上下文</span><span class="kv-v">${tokens ? fmtK2(tokens) : "—"}${ctxWindow ? " / " + fmtK2(ctxWindow) : ""}</span></div>
          ${ctxWindow ? html`<div class="kv-bar" title=${"已用 " + ctxPct + "%"}><i style=${{ width: ctxPct + "%" }}></i></div>` : null}
          <div class="kv-row"><span class="kv-k">缓存命中</span><span class=${"kv-v " + (cache.className || "")}>${cache.text || "—"}</span></div>
          <div class="kv-row"><span class="kv-k">输出速度</span><span class="kv-v">${speedText || "—"}</span></div>
        </div>
      </div>
    </div>
  </aside>`;
}
