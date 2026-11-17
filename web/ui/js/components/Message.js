import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { AgentAvatar, UserAvatar } from "../lib/avatars.js";
import { memo } from "../lib/memo.js";
import { mdToHtml } from "../lib/markdown.js";
import { noticeOf } from "../lib/transcript.js";
import { useStore, setState } from "../lib/store.js";
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

const EXAMPLES = [
  {
    key: "code",
    text: ["写一段代码", "并运行测试"],
    prompt: "帮我写一段代码实现____,顺手补一个最小测试并跑给我看。",
    ico: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"
      stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="m8.5 8.5-4 3.5 4 3.5" /><path d="m15.5 8.5 4 3.5-4 3.5" /><path d="M13 6.5 11 17.5" /></svg>`,
  },
  {
    key: "analyze",
    text: ["分析项目中的", "问题"],
    prompt: "通读这个项目,找出 3 个最值得修的隐患(正确性 / 性能 / 可读性各一项),给出证据位置和修法。",
    ico: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"
      stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M6 3.5h8.5L19 8v12.5H6z" /><path d="M14 3.5V8h5" /><path d="M9 12.5h7M9 16h5" /></svg>`,
  },
  {
    key: "optimize",
    text: ["帮我优化", "这个方案"],
    prompt: "我接下来贴一段方案描述,请从可行性、边界情况和成本三个角度挑毛病,并给出改进版。",
    ico: html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"
      stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M12 13.5c-1-3 .5-6.5 2.5-8.5 2.5.5 4 3 3.5 5.5-1 2-3 3.5-6 3.5" />
      <path d="M12 13.5c-2 0-4-1.5-5-3.5-.5-2.5 1-5 3.5-5.5C12.5 7 13.5 10 12.5 13" />
      <path d="M8.5 15.5 7 20l3.5-2 2 3 1.5-4.5" /></svg>`,
  },
];
const MORE_EXAMPLES = [
  "给这个项目补一份 README,再配一个最小可跑的示例。",
  "把这段报错栈贴给你,帮我定位根因并给出修法。",
  "帮我把这批散乱脚本整理成一个带参数解析的 CLI 工具。",
  "对这份配置文件做一次体检:重复项、失效项、命名不一致。",
  "写一个数据迁移脚本,先给我看方案和执行步骤,我确认后再跑。",
  "逐条解释这份 diff 的改动意图,并指出可疑的地方。",
];

function WelcomeHero() {
  const [moreOpen, setMoreOpen] = useState(false);
  useEffect(() => {
    if (!moreOpen) return undefined;
    const onDoc = (e) => {
      if (e.target && e.target.closest && e.target.closest(".ex-more-wrap")) return;
      setMoreOpen(false);
    };
    document.addEventListener("click", onDoc, true);
    return () => document.removeEventListener("click", onDoc, true);
  }, [moreOpen]);
  const fill = (text) => setState({ composerSeed: { id: Date.now(), text } });
  return html`<div class="placeholder hero" id="placeholder">
    <div class="hero-logo" aria-hidden="true">
      <svg class="hl-svg" viewBox="0 0 512 512" fill="none">
        <defs>
          <linearGradient id="hl-g" x1="39" y1="332" x2="473" y2="208" gradientUnits="userSpaceOnUse">
            <stop offset="0" stop-color="#5ec5ff"/><stop offset="1" stop-color="#a06bff"/>
          </linearGradient>
          <radialGradient id="hl-sat" cx="0.5" cy="0.5" r="0.5">
            <stop offset="0" stop-color="#ffffff"/><stop offset="0.35" stop-color="#9fd0ff"/>
            <stop offset="1" stop-color="#4f7dff" stop-opacity="0"/>
          </radialGradient>
        </defs>
        <ellipse cx="256" cy="270" rx="226" ry="76" stroke="url(#hl-g)" stroke-opacity="0.35"
          stroke-width="5" transform="rotate(-16 256 270)"/>
        <rect x="96" y="96" width="320" height="320" rx="74" fill="var(--ink)"/>
        <rect x="97" y="97" width="318" height="318" rx="73" stroke="var(--border)" stroke-opacity="0.6"/>
        <g transform="translate(256 256) scale(0.6) skewX(-9) translate(-256 -256)">
          <polygon fill="var(--ink-fg)"
            points="148,356 148,156 212,156 256,244 300,156 364,156 364,356 316,356 316,232 256,320 196,232 196,356"/>
        </g>
        <path d="M473.3 207.7 A226 76 -16 0 1 38.7 332.3" stroke="url(#hl-g)" stroke-width="5" stroke-linecap="round"/>
        <circle cx="312" cy="336.9" r="20" fill="url(#hl-sat)"/>
        <circle cx="312" cy="336.9" r="6.5" fill="#f2f8ff"/>
      </svg>
    </div>
    <h1 class="hero-title">你好，我是 Mini Code</h1>
    <p class="hero-sub">发一条消息开始对话 — 我会结合自动调度与工具帮你完成任务</p>
    <div class="ex-grid">
      ${EXAMPLES.map((ex) => html`<button class="ex-card" key=${ex.key} title="填入示例提示词"
        onClick=${() => fill(ex.prompt)}>
        <span class="ex-ico">${ex.ico}</span>
        <span class="ex-text">${ex.text[0]}<br />${ex.text[1]}</span>
      </button>`)}
      <span class="ex-more-wrap">
        <button class=${"ex-card more" + (moreOpen ? " open" : "")} id="btn-more-ex"
          onClick=${(e) => { e.stopPropagation(); setMoreOpen((v) => !v); }}>
          <span class="ex-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"
            stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <rect x="4" y="4" width="7" height="7" rx="1.6" /><rect x="13" y="4" width="7" height="7" rx="1.6" />
            <rect x="4" y="13" width="7" height="7" rx="1.6" /><rect x="13" y="13" width="7" height="7" rx="1.6" /></svg></span>
          <span class="ex-text">更多示例<span class="ex-arrow">↗</span></span>
        </button>
        ${moreOpen ? html`<div class="ex-pop" id="ex-pop">
          ${MORE_EXAMPLES.map((t, i) => html`<button class="ex-pop-item" key=${i}
            onClick=${() => { setMoreOpen(false); fill(t); }}>${t}</button>`)}
        </div>` : null}
      </span>
    </div>
  </div>`;
}

export function Placeholder() {
  const current = useStore((s) => s.current);
  if (!current) return html`<${WelcomeHero} />`;
  return html`<div class="placeholder" id="placeholder">
    <div class="big">M</div>
    <p>这个会话还没有消息</p>
  </div>`;
}
