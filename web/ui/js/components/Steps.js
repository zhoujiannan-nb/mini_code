// 工具步骤的渲染:折叠条(执行过程)、「正在执行」卡、单步、思考。
// 这里只画,不做归并 —— 步骤的增删/配对已经在 store 的 reducer 里完成。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { AgentAvatar } from "../lib/avatars.js";
import { memo } from "../lib/memo.js";
import { mdToHtml } from "../lib/markdown.js";
import { groupSteps } from "../lib/transcript.js";
import {
  stepTag, toolArgDisplay, toolWorkHint, toolLivePreview, tailOutput,
} from "../lib/tools.js";
import { fmtBytes, byteLenOf, fmtRun } from "../lib/format.js";
import { dispatch } from "../lib/store.js";

const html = htm.bind(h);

// StepView 是单步(工具或思考)。无 hook,可被 memo 包住。
function StepView({ step, running, now }) {
  if (step.kind === "think") {
    const text = String(step.text || "");
    return html`<details class="m-think">
      <summary>💭 思考过程(${Array.from(text).length} 字)</summary>
      <div class="think-body md" dangerouslySetInnerHTML=${{ __html: mdToHtml(text) }} />
    </details>`;
  }

  const tag = stepTag(step, running);
  const arg = toolArgDisplay(step.name, step.args);
  const live = step.phase === "run" || step.phase === "prep";
  const preview = live ? toolLivePreview(step.name, step.args) : "";
  // 真输出到了就顶掉参数预览:预览是输入,和输出混在同一格里只会看错。
  const liveText = step.output || preview;
  const outIsPreview = !step.output;
  const argsText = step.args ? String(step.args) : "";
  const argsShown = argsText.length > 2000 ? argsText.slice(0, 2000) + " …" : argsText;

  // 运行中计时:从发起调用那一刻起,尽量带上本次的超时上限。
  let runText = "";
  let late = false;
  if (step.phase === "run" && step.t0) {
    const sec = Math.max(0, Math.round(((now || Date.now()) - step.t0) / 1000));
    const lim = Number(step.timeout || 0);
    runText = "执行 " + fmtRun(sec) + (lim ? " / 最长 " + fmtRun(lim) : "");
    late = lim > 0 && sec >= lim;
  }
  // 写类工具:参数还在生成时报"生成参数中",已开始执行报"写入 xx KB · N 行"。
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
    open=${step.phase === "run" || step.phase === "prep"}>
    <summary>
      <span class="t-name">⚙ ${step.name}</span>
      ${arg ? html`<span class="ms-arg1" title=${arg.value}>${arg.brief}</span>` : null}
      <span class="ms-work" title=${work ? "这次要写入的量,发起调用时就已知;真正的回执要等写完" : ""}>${work}</span>
      <span class=${"ms-run" + (late ? " late" : "")}>${runText}</span>
      <span class=${"ms-tag " + tag.kind}>${tag.spin ? html`<span class="spin"></span>` : null}${tag.text}</span>
    </summary>
    <div class="ms-step-body">
      ${live ? null : (argsShown ? html`<div class="ms-args">${argsShown}</div>` : null)}
      ${liveText ? html`<pre class=${"ms-out" + (outIsPreview ? " ms-prev" : "")}>${tailOutput(liveText)}</pre>` : null}
      ${result != null ? html`<div class="ms-res-slot"><div class="t-res">✓ ${shown}${cut}</div></div>` : null}
    </div>
  </details>`;
}

// now 只在运行中的步骤上变化:跑完的步骤传常量 0,memo 才能跳过重渲。
export const Step = memo(StepView);

// StepStrip 是「执行过程」折叠条本体。inline=true 表示它被并进了一条答案卡,
// 这时不能再套 .m-card 外壳(旧 stepsMergeInto 的 ms-inline 语义)。
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

// StepsCard:折叠条独立成一条消息卡(整轮没有正文时)。
export function StepsCard({ steps }) {
  return html`<div class="msg steps">
    <${AgentAvatar} cls="c-avatar" />
    <div class="m-card"><${StepStrip} steps=${steps} /></div>
  </div>`;
}

// LiveSteps 是「正在执行」卡:还没收进折叠条的步骤住在这里,跑完才搬走。
// 每 1 秒刷新一次计时,并顺手结算"快工具最短展示时间"到期的步骤。
export function LiveSteps({ steps, running }) {
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
  return html`<div class="msg steps">
    <${AgentAvatar} cls="c-avatar" />
    <div class="m-card ms-live">
      <div class="ms-live-hd"><span class="spin"></span>正在执行${parallel > 1
        ? html`<span class="ms-live-n">${parallel} 个并行</span>` : null}</div>
      <div class="ms-live-body">
        ${visible.map((s) => html`<${Step} key=${s.key} step=${s} running=${running} now=${s.phase === "run" ? now : 0} />`)}
      </div>
    </div>
  </div>`;
}
