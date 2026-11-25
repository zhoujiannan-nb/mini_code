import { h } from "preact";
import htm from "htm";

import { useStore, toggleSummaryPanel } from "../lib/store.js";
import { TodoCard } from "./TodoCard.js";
import { todoItemsOf, toolArgsOf, todoStatusOf } from "../lib/tools.js";
import { fmtK2, paintSpeed, fmtTaskDur } from "../lib/format.js";
import { openFilePath } from "../lib/markdown.js";

const html = htm.bind(h);

const FILE_TOOLS = new Set(["read", "write", "edit", "read_image",
  "read_file", "write_file", "edit_file"]);
const FILE_KEYS = ["file_path", "path", "file", "target", "dest"];

export function summarize(rows, liveSteps) {
  let todos = null;
  const files = [];
  const seen = new Set();
  let steps = 0;
  const visit = (list) => {
    for (const s of list || []) {
      if (s.kind === "think") continue;
      steps += 1;
      if (!FILE_TOOLS.has(String(s.name || "").toLowerCase())) continue;
      const o = toolArgsOf(s.args);
      if (!o) continue;
      for (const k of FILE_KEYS) {
        const v = o[k];
        if (typeof v === "string" && v.trim() && !seen.has(v)) {
          seen.add(v);
          files.push(v.trim());
        }
        break; 
      }
    }
  };
  const scanTodos = (list) => {
    for (const s of list || []) {
      if (s.kind === "think") continue;
      const t = todoItemsOf(s.name, s.args);
      if (t) todos = t;
    }
  };
  const arr = rows || [];
  for (let i = arr.length - 1; i >= 0; i--) {
    const r = arr[i];
    if (r.steps) visit(r.steps);
  }
  visit(liveSteps);
  for (const r of arr) scanTodos(r.steps);
  scanTodos(liveSteps);
  return { todos, files, steps };
}

function persistedTodos(sess) {
  const raw = sess && Array.isArray(sess.todos) ? sess.todos : null;
  if (!raw || !raw.length) return null;
  const items = [];
  for (const t of raw) {
    const text = String((t && (t.content || t.text)) || "").replace(/\s+/g, " ").trim();
    if (!text) continue;
    items.push({ text: text, status: todoStatusOf(t.status) });
  }
  if (!items.length) return null;
  const done = items.filter((i) => i.status === "completed").length;
  const doing = items.filter((i) => i.status === "in_progress").length;
  return { items: items, done: done, doing: doing, total: items.length, label: done + "/" + items.length };
}

function statusOf(s, running) {
  if (!s) return { key: "new", text: "等待第一条消息" };
  if (running || s.active) return { key: "running", text: "任务执行中" };
  if (s.children_active) return { key: "running", text: "子任务执行中" };
  if (s.status === "completed") return { key: "done", text: "任务已完成" };
  if (s.status === "failed") return { key: "failed", text: "任务失败" };
  if (s.status === "interrupted") return { key: "stopped", text: "任务已中断" };
  return { key: "idle", text: "空闲" };
}

function tailName(p) {
  const segs = String(p || "").replace(/\\/g, "/").split("/").filter(Boolean);
  return segs.slice(-2).join("/");
}

export function SummaryPanel() {
  const open = useStore((s) => s.summaryOpen);
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
  const ctxInfo = useStore((s) => s.ctxInfo);

  if (!current) return null;
  const sess = (sessions || []).find((x) => x.session_id === current);
  const st = statusOf(sess, running);
  const dur = sess ? fmtTaskDur(sess, Date.now()) : "";
  const sum = summarize(rows, runSteps);
  const todos = sum.todos || persistedTodos(sess);
  const speedText = paintSpeed(speed, speedAccurate);
  const ctxPct = ctxWindow > 0 ? Math.min(100, Math.round((tokens || 0) * 100 / ctxWindow)) : 0;
  const totals = ctxInfo && ctxInfo.totals;
  let hitText = "—";
  if (totals && totals.reported) hitText = totals.input > 0 ? Math.round(totals.cached * 100 / totals.input) + "%" : "—";
  else if (totals) hitText = "未上报";
  else if (cacheHit) hitText = cacheHit.reported
    ? (cacheHit.prompt > 0 ? Math.round(cacheHit.cached * 100 / cacheHit.prompt) + "%" : "—")
    : "未上报";
  const MAX_FILES = 12;
  const files = sum.files.slice(0, MAX_FILES);
  const moreFiles = sum.files.length - files.length;

  return html`<aside class=${"ctx-panel" + (open ? " open" : "")} id="summary-panel"
    aria-hidden=${open ? "false" : "true"}>
    <div class="ctx-head">
      <span class="ctx-title">执行摘要</span>
      <span class=${"sum-chip " + st.key}>${st.text}</span>
      <span class="sh-flex"></span>
      <button class="sh-btn" id="sum-close" title="收起面板" onClick=${() => toggleSummaryPanel()}>×</button>
    </div>
    <div class="ctx-body">
      <div class="rail-sec">
        <div class="rail-sec-title">执行结果</div>
        ${todos
          ? html`<${TodoCard} todos=${todos} />`
          : html`<div class="rail-empty">本轮还没有待办清单 —— agent 用 todo_write 列计划后,这里会跟着跑。</div>`}
      </div>

      <div class="rail-sec">
        <div class="rail-sec-title">相关文件${sum.files.length ? html` <span class="rs-n">${sum.files.length}</span>` : null}</div>
        ${files.length ? html`<ul class="ctx-events sum-files">
          ${files.map((p) => html`<li key=${p} title=${p} onClick=${() => openFilePath(p)}>
            <span class="ce-main">${tailName(p)}</span>
          </li>`)}
          ${moreFiles > 0 ? html`<li class="rf-more">还有 ${moreFiles} 个文件</li>` : null}
        </ul>` : html`<div class="rail-empty">这一步还没有读写过文件。</div>`}
      </div>

      <div class="rail-sec">
        <div class="rail-sec-title">性能指标</div>
        <div class="rail-kv">
          <div class="kv-row"><span class="kv-k">状态</span><span class="kv-v">${st.text}${dur ? " · " + dur : ""}</span></div>
          <div class="kv-row"><span class="kv-k">上下文</span><span class="kv-v">${tokens ? fmtK2(tokens) : "—"}${ctxWindow ? " / " + fmtK2(ctxWindow) + "(" + ctxPct + "%)" : ""}</span></div>
          <div class="kv-row"><span class="kv-k">缓存命中</span><span class="kv-v">${hitText}</span></div>
          <div class="kv-row"><span class="kv-k">输出速度</span><span class="kv-v">${speedText || "—"}</span></div>
          <div class="kv-row"><span class="kv-k">已执行</span><span class="kv-v">${sum.steps} 步${sess && sess.message_count ? " · " + sess.message_count + " 条消息" : ""}</span></div>
        </div>
      </div>
    </div>
  </aside>`;
}
