import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { AgentAvatar } from "../lib/avatars.js";
import { memo } from "../lib/memo.js";
import { groupSteps } from "../lib/transcript.js";
import {
  stepTag, toolArgDisplay, toolWorkHint, toolLivePreview, tailOutput,
  todoItemsOf, goalLineOf,
} from "../lib/tools.js";
import { fmtBytes, byteLenOf, fmtRun } from "../lib/format.js";
import { dispatch } from "../lib/store.js";
import { ThinkBox } from "./ThinkBox.js";
import { TodoCard } from "./TodoCard.js";

const html = htm.bind(h);

function StepView({ step, running, now }) {
  if (step.kind === "think") return html`<${ThinkBox} text=${step.text} />`;

  const tag = stepTag(step, running);
  const arg = toolArgDisplay(step.name, step.args);
  const todos = todoItemsOf(step.name, step.args);
  const goal = todos ? "" : goalLineOf(step.name, step.args);
  const live = step.phase === "run" || step.phase === "prep";
  const preview = live ? toolLivePreview(step.name, step.args) : "";
  const liveText = step.output || preview;
  const outIsPreview = !step.output;
  const argsText = step.args ? String(step.args) : "";
  const argsShown = argsText.length > 2000 ? argsText.slice(0, 2000) + " …" : argsText;

  let runText = "";
  let late = false;
  if (step.phase === "run" && step.t0) {
    const sec = Math.max(0, Math.round(((now || Date.now()) - step.t0) / 1000));
    const lim = Number(step.timeout || 0);
    runText = "执行 " + fmtRun(sec) + (lim ? " / 最长 " + fmtRun(lim) : "");
    late = lim > 0 && sec >= lim;
  }
  let work = "";
  if (step.phase === "prep") {
    const body = preview ? preview.split("\n").length : 0;
    work = "生成参数中 " + fmtBytes(byteLenOf(argsText)) + (body ? " · " + body + " 行" : "");
  } else if (live && !preview) {
    work = toolWorkHint(step.name, step.args);
  }

  const result = step.result == null ? null : String(step.result);
  const shown = result == null ? "" : result.slice(0, 4000);
  const cut = result != null && result.length > shown.length
    ? html`<span class="ms-cut">… 共 ${result.length} 字,已截断</span>` : null;

  return html`<details class=${"ms-step ms-in" + (step.folded ? "" : " ms-live-step")}
    open=${live || !!todos || !!goal}>
    <summary>
      <span class="t-name">⚙ ${step.name}</span>
      ${arg ? html`<span class="ms-arg1" title=${arg.value}>${arg.brief}</span>` : null}
      ${todos ? html`<span class="ms-todo" title=${"已完成 " + todos.done + " / " + todos.total}>✓ ${todos.label}</span>` : null}
      <span class="ms-work" title=${work ? "这次要写入的量,发起调用时就已知;真正的回执要等写完" : ""}>${work}</span>
      <span class=${"ms-run" + (late ? " late" : "")}>${runText}</span>
      <span class=${"ms-tag " + tag.kind}>${tag.spin ? html`<span class="spin"></span>` : null}${tag.text}</span>
    </summary>
    <div class="ms-step-body">
      ${todos ? html`<${TodoCard} todos=${todos} />` : null}
      ${goal ? html`<div class="goal-line">🎯 ${goal}</div>` : null}
      ${live || todos ? null : (argsShown ? html`<div class="ms-args">${argsShown}</div>` : null)}
      ${liveText ? html`<pre class=${"ms-out" + (outIsPreview ? " ms-prev" : "")}>${tailOutput(liveText)}</pre>` : null}
      ${result != null ? html`<div class="ms-res-slot"><div class="t-res">✓ ${shown}${cut}</div></div>` : null}
    </div>
  </details>`;
}

export const Step = memo(StepView);

export function StepStrip({ steps, inline }) {
  const g = groupSteps(steps);
  const [open, setOpen] = useState(false);
  return html`<details class=${"m-steps" + (inline ? " ms-inline" : "")}
    open=${open} onToggle=${(e) => setOpen(e.currentTarget.open)}>
    <summary>
      <span class="ms-t">执行过程</span>
      <span class="ms-n">${g.count} 步</span>
      ${g.thinkCount ? html`<span class="ms-n">${g.thinkCount} 次思考</span>` : null}
      ${g.lastHint ? html`<span class="ms-last">${g.lastHint}</span>` : null}
      <span class="ms-hint">${open ? "收起" : "展开"}</span>
    </summary>
    <div class="m-steps-wrap"><div class="m-steps-body">
      ${steps.map((s) => html`<${Step} key=${s.key} step=${s} running=${false} now=${0} />`)}
    </div></div>
  </details>`;
}

export function StepsCard({ steps }) {
  return html`<div class="msg steps">
    <${AgentAvatar} cls="c-avatar" />
    <div class="m-card"><${StepStrip} steps=${steps} /></div>
  </div>`;
}

export function LiveSteps({ steps, running }) {
  const [open, setOpen] = useState(false);
  const [now, setNow] = useState(Date.now());
  const visible = (steps || []).filter((s) => !s.folded);
  const ticking = (steps || []).some((s) => !s.folded && (s.phase === "run" || s.phase === "prep" || s.holdUntil));
  useEffect(() => {
    if (!ticking) return undefined;
    const t = setInterval(() => {
      const at = Date.now();
      setNow(at);
      dispatch({ type: "foldHeld", now: at });
    }, 1000);
    return () => clearInterval(t);
  }, [ticking, steps]);

  if (!visible.length) return null;
  const parallel = visible.filter((s) => s.phase === "run").length;
  const last = visible[visible.length - 1];
  const lastHint = last.kind === "think" ? "思考中" : String(last.name || "tool");
  return html`<div class="msg steps ms-live-wrap">
    <${AgentAvatar} cls="c-avatar" />
    <div class=${"m-card ms-live" + (open ? "" : " collapsed")}>
      <div class="ms-live-hd" onClick=${() => setOpen((v) => !v)}>
        <span class="spin"></span>正在执行<span class="ms-live-n">${visible.length} 步</span>${parallel > 1
          ? html`<span class="ms-live-n">${parallel} 个并行</span>` : null}
        <span class="ms-last">最近 ${lastHint}</span>
        <span class="ms-hint">${open ? "收起" : "展开"}</span>
      </div>
      <div class="ms-live-body">
        ${visible.map((s) => html`<${Step} key=${s.key} step=${s} running=${running} now=${s.phase === "run" ? now : 0} />`)}
      </div>
    </div>
  </div>`;
}
