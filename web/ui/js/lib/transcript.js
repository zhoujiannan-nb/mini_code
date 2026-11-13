// 消息分组的纯函数:把后端 /api/sessions/{id} 的 messageView 列表变成渲染行,
// 以及把步骤集合折叠成「N 步 · M 次思考 · 最近一步」。
//
// 这一层刻意不碰 DOM:store 的流式 reducer 建的是同一种行/步骤对象,
// 历史回显和实时路径因此只有一份形状(旧代码里这两条路径是两套 DOM 代码,
// 才出现了"刷新后正在执行的小卡没了"那类不一致)。
import { newKey } from "./keys.js";
import { stepTag } from "./tools.js";

/* ---------------- 注入型 user 消息 ---------------- */
// 运行时只有 "user" 这一个角色能保证所有模板都读,所以公约广播、上下文提醒、
// 子代理回执、压缩指令/摘要、守卫轻推全都以 user 轮进上下文。后端在每条上带了
// notice 来源(见 session/notices.go),这里只负责分类与文案,不冒充用户说话。
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

// detectNoticeKind 是后端 session.NoticeKind 的前端兜底:正常情况下
// messageView.notice 已经带来源,但旧会话/旧服务端/直接喂进来的 transcript 可能没有。
// marker 与 session/notices.go、worker.go、compaction.go 保持同一批字符串 ——
// 它们本来就是运行时写进消息里的稳定标记,所以按标记分类不需要改消息本身。
// 注意只对 user 轮调用:别的角色不可能是"注入"。
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
  // "<agent " 是当前写法(subagent.go,命名 agent);"<subagent " 属于旧会话。
  if (trimmed.startsWith("<agent ") || trimmed.startsWith("<subagent ")) return "subagent";
  if (trimmed.startsWith("<job ")) return "job";
  if (trimmed === LEGACY_STEER_TEXT) return "steer";
  if (trimmed === EMPTY_RETRY_TEXT) return "retry";
  return "";
}

/* ---------------- 消息内容读取(多模态) ---------------- */
export function contentText(m) {
  if (typeof m.content === "string") return m.content;
  if (Array.isArray(m.content)) {
    return m.content.map((p) => p.type === "text" ? (p.text || "") : "📷 [图片]").join("\n");
  }
  return "";
}

// 提取消息内容里的图片 data URL(多模态消息的 content 是 parts 数组)
export function contentImages(m) {
  if (!Array.isArray(m.content)) return [];
  return m.content.filter((p) => p.type === "image_url" && p.image_url && p.image_url.url)
    .map((p) => p.image_url.url);
}

/* ---------------- 步骤对象 ---------------- */
// 一个步骤的完整形状(stream reducer 与历史回显共用):
//   kind: "tool" | "think"
//   phase: "prep"(参数生成中) | "run"(执行中) | "soft"(待返回) |
//          "done"(有回执) | "lost"(跑了但没回执) | "notrun"(预备步骤没执行)
//   folded: true = 已收进「执行过程」折叠条;false = 还在「正在执行」卡里
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

// findOpenStep 找"还没有回执"的那一步:优先同一工具名,并且按先后顺序取最早的。
// 并行调用同名工具时,回执是按调用顺序回来的(旧 stepsSoft 取 hit[0] 的语义),
// 每份结果只配一次,不会两个并行调用抢同一份结果。
export function findOpenStep(steps, name) {
  const open = (steps || []).filter((s) => s.kind !== "think" && s.result == null && s.phase !== "lost" && s.phase !== "notrun");
  if (name) {
    for (const s of open) if (s.name === name) return s;
  }
  return null;
}

/* ---------------- 步骤折叠摘要 ---------------- */
// groupSteps 回答折叠条上那一行:一共几步、几次思考、最近一步干了什么。
// 步数一律从数组数,不用累加器 —— 旧代码用累加器时出现"执行过程 0 步"却躺着一堆步骤。
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

/* ---------------- 历史消息 → 渲染行 ---------------- */
// 分组规则(与旧 appendHistoryMsg 的 DOM 行为逐条对齐):
//   - user 轮是任务边界:之前攒的步骤先独立成一条「执行过程」卡;
//     带 notice 的 user 轮画成系统注入行,不冒充用户。
//   - assistant 轮:它的正文卡片吸收"之前攒的步骤";它自己的 tool_calls 则另起
//     一组,留给下一条 assistant 轮吸收(所以工具卡看起来总在调用它的那次回复下面)。
//   - tool 轮:按工具名把回执填进还没回执的那一步;配不上才独立成一步。
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
        // 后端没标来源时按稳定 marker 兜底分类:运行时注入的 user 轮绝不能
        // 画成"用户自己说的话"(见 detectNoticeKind)。
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
      // 先找当前这一组,再回头找已经并进 earlier assistant 卡的步骤。
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
