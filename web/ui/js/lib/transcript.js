import { newKey } from "./keys.js";
import { stepTag } from "./tools.js";

export const NOTICE_LABELS = {
  worker: "工作区公约",
  context: "上下文提醒",
  subagent: "子代理回执",
  job: "后台命令回执",
  steer: "继续守卫轻推",
  retry: "空回复提醒",
  compact: "压缩指令",
  summary: "压缩摘要",
  persist: "持久化告警",
  goal: "目标续跑轮",
};

export function noticeOf(kind, text) {
  const label = NOTICE_LABELS[kind] || "系统注入";
  const t = String(text == null ? "" : text);
  return {
    kind: kind || "",
    label,
    full: t,
    brief: t.length > 90 ? t.slice(0, 90) + "…" : t,
  };
}

const LEGACY_STEER_TEXT = "You described an action but did not call any tool. " +
  "If the work is not finished, emit the tool call for it now. " +
  "If it is genuinely finished, give the final answer instead.";
const EMPTY_RETRY_TEXT = "Your previous response was empty. Continue the task: take the next concrete step (a tool call) or answer the user directly.";

export function detectNoticeKind(text) {
  const t = String(text == null ? "" : text);
  const trimmed = t.trim();
  if (t.includes("__MC_WORKER_NOTE__") || t.includes("__MC_WORKER_JOIN__")) return "worker";
  if (t.includes("__CTX_USAGE_")) return "context";
  if (t.includes("__CTX_COMPACT_REQUEST__")) return "compact";
  if (t.includes("<previous-summary>")) return "summary";
  if (trimmed.startsWith("__MC_PERSIST_FAILED__")) return "persist";
  if (trimmed.startsWith("<agent ") || trimmed.startsWith("<subagent ")) return "subagent";
  if (trimmed.startsWith("<job ")) return "job";
  if (trimmed.startsWith("<goal_round")) return "goal"; 
  if (trimmed === LEGACY_STEER_TEXT) return "steer";
  if (trimmed === EMPTY_RETRY_TEXT) return "retry";
  return "";
}

export function contentText(m) {
  if (typeof m.content === "string") return m.content;
  if (Array.isArray(m.content)) {
    return m.content.map((p) => p.type === "text" ? (p.text || "") : "📷 [图片]").join("\n");
  }
  return "";
}

export function contentImages(m) {
  if (!Array.isArray(m.content)) return [];
  return m.content.filter((p) => p.type === "image_url" && p.image_url && p.image_url.url)
    .map((p) => p.image_url.url);
}

export function makeStep(init) {
  const s = Object.assign({
    key: newKey("step"),
    kind: "tool",
    name: "tool",
    args: "",
    result: null,
    phase: "soft",
    t0: 0,
    timeout: 0,
    output: "",
    folded: false,
    holdUntil: 0,
    durationSec: 0,
    text: "",
  }, init || {});
  if (s.kind === "think") {
    s.name = "";
    s.phase = "done";
    s.folded = false;
  }
  return s;
}

export function makeToolStep(init) {
  return makeStep(Object.assign({ kind: "tool" }, init || {}));
}

export function makeThinkStep(text, folded) {
  return makeStep({ kind: "think", text: String(text || ""), folded: !!folded });
}

export function findOpenStep(steps, name) {
  const open = (steps || []).filter((s) => s.kind !== "think" && s.result == null && s.phase !== "lost" && s.phase !== "notrun");
  if (name) {
    for (const s of open) if (s.name === name) return s;
  }
  return null;
}

export function groupSteps(steps) {
  const all = (steps || []).filter(Boolean);
  const thinking = all.filter((s) => s.kind === "think");
  let lastHint = "";
  for (let i = all.length - 1; i >= 0; i--) {
    const s = all[i];
    if (s.kind === "think") continue;
    const tag = stepTag(s, false).text;
    lastHint = "最近 " + s.name + (tag ? " · " + tag : "");
    break;
  }
  return {
    count: all.length,
    thinkCount: thinking.length,
    thinking,
    tools: all.filter((s) => s.kind !== "think"),
    lastHint,
  };
}

export function historyRows(views) {
  const rows = [];
  let run = [];
  const flushRun = () => {
    if (run.length) rows.push({ key: newKey("steps"), kind: "steps", steps: run });
    run = [];
  };
  for (const m of views || []) {
    if (!m || m.role === "system") continue;
    if (m.role === "user") {
      flushRun();
      const text = Array.isArray(m.content)
        ? m.content.filter((p) => p.type === "text").map((p) => p.text || "").join("\n")
        : contentText(m);
      if (m.notice) rows.push({ key: newKey("notice"), kind: "notice", notice: m.notice, text });
      else {
        const kind = detectNoticeKind(text);
        if (kind) rows.push({ key: newKey("notice"), kind: "notice", notice: kind, text });
        else rows.push({ key: newKey("user"), kind: "user", text, images: contentImages(m) });
      }
    } else if (m.role === "assistant") {
      const text = contentText(m);
      const reasoning = m.reasoning_content || "";
      const calls = (m.tool_calls || []).map((tc) => makeToolStep({
        name: (tc.function && tc.function.name) || "tool",
        args: (tc.function && tc.function.arguments) || "",
        result: null,
        phase: "soft",
      }));
      if (text) {
        rows.push({
          key: newKey("ai"),
          kind: "assistant",
          text,
          reasoning,
          steps: run,
        });
        run = calls;
      } else {
        if (reasoning) run.push(makeThinkStep(reasoning));
        run = run.concat(calls);
      }
    } else if (m.role === "tool") {
      const name = m.name || "tool";
      const text = contentText(m);
      let hit = findOpenStep(run, name);
      if (!hit) {
        for (let i = rows.length - 1; i >= 0 && !hit; i--) {
          if (rows[i].steps) hit = findOpenStep(rows[i].steps, name);
        }
      }
      if (hit) {
        Object.assign(hit, { result: text, phase: "done", folded: true, soft: false });
      } else {
        run.push(makeToolStep({ name, args: "", result: text, phase: "done", folded: true }));
      }
    }
  }
  flushRun();
  return rows;
}
