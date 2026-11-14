// 思考过程(reasoning_content)小框:淡色小字、限高可滚动、**默认折叠**。
//
// 折叠规则(2026-11 需求):一轮里有多个思考框(执行步骤里的历史思考 + 正在想的
// 那条),只有 live=true 的那一个默认展开;live 结束、内容落成普通思考步骤时组件
// 换成非 live 实例 —— 天然回到折叠态,即"刚输出完就折叠"。标题行可点开/收,
// 任何时候想看旧思考点一下即可。
//
// 三处共用这一个组件:历史消息里的 reasoning_content、正在生成的那一条、以及
// 「执行过程」里的思考步骤。折叠只藏框体(加 .collapsed 类),正文仍在 DOM 里,
// 测试与全文搜索都还能摸到内容。
//
// 颜色一律走主题变量:--text3 是本项目的"淡色"档(浅底 ≥8:1、深底 ≥8.6:1),
// 比正文的 --text 明显退后一档;写死 #8a8a8a 会在暗色主题下糊成一片,所以不用。
import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { mdToHtml } from "../lib/markdown.js";

const html = htm.bind(h);

// 距底不到这么多像素算"正在看最新一段",这时才跟着往下滚。
const STICK_PX = 40;

// live=true(流式增量)时默认展开并自动滚到底;用户自己往上翻了就不再抢视角。
// 非 live 的(历史消息、已完成的思考步骤)默认折叠,点头部可展开查看。
export function ThinkBox({ text, live }) {
  const body = String(text || "");
  const boxRef = useRef(null);
  const [open, setOpen] = useState(!!live);

  useEffect(() => {
    const el = boxRef.current;
    if (!el || !live || !open) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < STICK_PX) el.scrollTop = el.scrollHeight;
  }, [body, live, open]);

  const toggle = () => setOpen((v) => !v);

  return html`<div class=${"think-box" + (live ? " live" : "") + (open ? "" : " collapsed")}>
    <div class="think-box-hd" onClick=${toggle}>
      <span class="tb-caret">${open ? "▾" : "▸"}</span>
      <span class="tb-tag">💭 思考过程</span>
      <span class="tb-n">${Array.from(body).length} 字</span>
      ${live ? html`<span class="tb-live"><span class="pulse"></span>思考中</span>` : null}
    </div>
    <div class="think-box-body md" ref=${boxRef}
      dangerouslySetInnerHTML=${{ __html: mdToHtml(body) }} />
  </div>`;
}
