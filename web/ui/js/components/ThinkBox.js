import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import htm from "htm";

import { mdToHtml } from "../lib/markdown.js";

const html = htm.bind(h);

const STICK_PX = 40;

export function ThinkBox({ text, live }) {
  const body = String(text || "");
  const boxRef = useRef(null);
  const stick = useRef(true); 
  const [open, setOpen] = useState(!!live);

  useEffect(() => {
    const el = boxRef.current;
    if (!el || !live || !open || !stick.current) return;
    el.scrollTop = el.scrollHeight;
  }, [body, live, open]);

  const onWheel = (e) => {
    const el = boxRef.current;
    if (!el) return;
    if (e.deltaY < 0) stick.current = false;
    else if (el.scrollHeight - el.scrollTop - el.clientHeight < STICK_PX) stick.current = true;
  };

  const toggle = () => setOpen((v) => !v);

  return html`<div class=${"think-box" + (live ? " live" : "") + (open ? "" : " collapsed")}>
    <div class="think-box-hd" onClick=${toggle}>
      <span class="tb-caret">${open ? "▾" : "▸"}</span>
      <span class="tb-tag">💭 思考过程</span>
      <span class="tb-n">${Array.from(body).length} 字</span>
      ${live ? html`<span class="tb-live"><span class="pulse"></span>思考中</span>` : null}
    </div>
    <div class="think-box-body md" ref=${boxRef} onWheel=${onWheel}
      dangerouslySetInnerHTML=${{ __html: mdToHtml(body) }} />
  </div>`;
}
