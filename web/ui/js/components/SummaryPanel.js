// 「执行摘要」抽屉:与「上下文」抽屉同一位置滑出(互斥),运行中点聊天头
// 「摘要」即可查看 agent 的目标跟你想的是不是一样。
//
// 只画有真实数据支撑的东西 —— 状态/耗时、最近一份待办清单、本轮动过的文件、
// 性能指标。没有数据的小节直接不画,不编数字。数据全部来自 store(历史步骤 +
// 实时步骤)+ ctxInfo 快照,本组件不打后端接口(点文件除外:走 openFilePath)。
import { h } from "preact";
import htm from "htm";

import { useStore, toggleSummaryPanel } from "../lib/store.js";
import { TodoCard } from "./TodoCard.js";
import { todoItemsOf, toolArgsOf } from "../lib/tools.js";
import { fmtK2, paintSpeed, fmtTaskDur } from "../lib/format.js";
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
  const speedText = paintSpeed(speed, speedAccurate);
  const ctxPct = ctxWindow > 0 ? Math.min(100, Math.round((tokens || 0) * 100 / ctxWindow)) : 0;
  const totals = ctxInfo && ctxInfo.totals;
  // 命中率优先说本会话累计;供应商不报缓存就明说,不拿 0% 冒充。
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
        ${sum.todos
          ? html`<${TodoCard} todos=${sum.todos} />`
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
