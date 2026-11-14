// 单条消息:用户 / 助手 / 系统注入 notice / 过程条 / 错误 / 状态行。
// DOM 与旧 messages.js + steps.js 生成的完全一致(class 名、层级、头像位置都不变),
// 变的只是"由 state 渲染"而不是"拼字符串再 innerHTML"。
// 唯一的例外是思考过程:它不再折叠,统一交给 ThinkBox 画成淡色可滚动小框。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { AgentAvatar, UserAvatar } from "../lib/avatars.js";
import { memo } from "../lib/memo.js";
import { mdToHtml } from "../lib/markdown.js";
import { noticeOf } from "../lib/transcript.js";
import { useStore } from "../lib/store.js";
import { StepStrip, StepsCard } from "./Steps.js";
import { ThinkBox } from "./ThinkBox.js";

const html = htm.bind(h);

function NoticeLine({ notice, text }) {
  const n = noticeOf(notice, text);
  return html`<div class="msg notice">
    <div class="m-card">
      <div class="notice-line">
        <details>
          <summary>
            <span class="notice-tag">· ${n.label}</span>
            <span class="notice-brief">${n.brief}</span>
          </summary>
          <pre class="notice-full">${n.full}</pre>
        </details>
      </div>
    </div>
  </div>`;
}

function UserMessage({ row }) {
  const text = String(row.text || "");
  const images = row.images || [];
  return html`<div class="msg user">
    <${UserAvatar} cls="c-avatar" />
    <div class="m-card">
      <div class="m-head"><span class="m-tag user">你</span></div>
      ${text ? html`<div class="m-text">${text}</div>` : null}
      ${images.length ? html`<div class="msg-imgs">
        ${images.map((u, i) => html`<img key=${i} src=${u} alt="截图" title="点击放大" />`)}
      </div>` : null}
    </div>
  </div>`;
}

function AssistantMessage({ row }) {
  const text = String(row.text || "");
  const reasoning = String(row.reasoning || "");
  const steps = row.steps || [];
  return html`<div class="msg ai">
    <${AgentAvatar} cls="c-avatar" />
    <div class="m-card">
      <div class="m-head"><span class="m-tag ai">Mini Code</span></div>
      ${steps.length ? html`<${StepStrip} steps=${steps} inline=${true} />` : null}
      ${reasoning ? html`<${ThinkBox} text=${reasoning} />` : null}
      ${text ? html`<div class="m-text md" dangerouslySetInnerHTML=${{ __html: mdToHtml(text) }} />` : null}
    </div>
  </div>`;
}

// MessageView 无 hook:被 memo 包住之后,只要 row 引用不变就不会重渲
// (流式 delta 期间历史消息一条都不动)。
function MessageView({ row }) {
  switch (row.kind) {
    case "user":
      return html`<${UserMessage} row=${row} />`;
    case "notice":
      return html`<${NoticeLine} notice=${row.notice} text=${row.text} />`;
    case "assistant":
      return html`<${AssistantMessage} row=${row} />`;
    case "steps":
      return html`<${StepsCard} steps=${row.steps || []} />`;
    case "error":
      return html`<div class="msg error">
        <div class="m-card"><div class="m-text">⚠ 任务失败:${String(row.text || "")}</div></div>
      </div>`;
    case "status":
      return html`<div class="msg status">
        <div class="m-card"><div class="m-text">${String(row.text || "")}</div></div>
      </div>`;
    default:
      return null;
  }
}

export const Message = memo(MessageView);

// 正在生成的那一条。它每个 delta 都要重渲(全文 markdown 重算不可避免),
// 但消息列表与它无关 —— 这正是重构要拿到的收益。
export function LiveMessage({ live }) {
  const content = String(live.content || "");
  const reasoning = String(live.reasoning || "");
  return html`<div class="msg ai">
    <${AgentAvatar} cls="c-avatar" />
    <div class="m-card">
      <div class="m-head">
        <span class="m-tag ai">Mini Code</span>
        <span class="live-ind"><span class="pulse"></span>${content ? "生成中…" : "思考中…"}</span>
      </div>
      ${reasoning ? html`<${ThinkBox} text=${reasoning} live=${true} />` : null}
      ${content ? html`<div class="m-text md" dangerouslySetInnerHTML=${{ __html: mdToHtml(content) }} />` : null}
    </div>
  </div>`;
}

// 等待模型首 token 的转圈:大上下文首次推理可能很慢,没有这个秒数会像卡死。
export function PendingMessage() {
  const [sec, setSec] = useState(0);
  useEffect(() => {
    const t0 = Date.now();
    const timer = setInterval(() => setSec(Math.floor((Date.now() - t0) / 1000)), 1000);
    return () => clearInterval(timer);
  }, []);
  return html`<div class="msg pending">
    <div class="m-card">
      <span class="spinner"></span>
      <span>等待模型响应…(大上下文首次推理可能较慢)</span>
      <span class="pending-sec">${sec}s</span>
    </div>
  </div>`;
}

export function Placeholder() {
  const current = useStore((s) => s.current);
  return html`<div class="placeholder" id="placeholder">
    <div class="big">💬</div>
    <p>${current ? "这个会话还没有消息" : "发送消息开始对话,新会话将自动创建"}</p>
  </div>`;
}
