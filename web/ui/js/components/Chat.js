import { h } from "preact";
import { useState, useLayoutEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore } from "../lib/store.js";
import { memo } from "../lib/memo.js";
import { Message, LiveMessage, PendingMessage, Placeholder } from "./Message.js";
import { LiveSteps, StepsCard } from "./Steps.js";

const html = htm.bind(h);

export const HIST_PAGE = 100;

const MessageList = memo(function MessageList({ rows }) {
  return rows.map((row) => html`<${Message} key=${row.key} row=${row} />`);
});

export function Chat() {
  const rows = useStore((s) => s.rows);
  const live = useStore((s) => s.live);
  const pending = useStore((s) => s.pending);
  const runSteps = useStore((s) => s.run.steps);
  const running = useStore((s) => s.running);
  const current = useStore((s) => s.current);
  const jumpTick = useStore((s) => s.jumpTick);

  const [visible, setVisible] = useState(HIST_PAGE);
  const boxRef = useRef(null);
  const stickRef = useRef(true); 
  const lastScrollHeight = useRef(0);
  const jumpedTick = useRef(jumpTick);

  useLayoutEffect(() => {
    setVisible(HIST_PAGE);
    jumpedTick.current = jumpTick; 
    stickRef.current = true;
  }, [current]);

  const contentKey = live ? (live.content || "").length + ":" + (live.reasoning || "").length : "";
  useLayoutEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    if (jumpTick !== jumpedTick.current) {
      jumpedTick.current = jumpTick;
      stickRef.current = true;
    }
    if (stickRef.current) el.scrollTop = el.scrollHeight;
    lastScrollHeight.current = el.scrollHeight;
  }, [rows, runSteps, contentKey, pending, jumpTick]);

  const onScroll = () => {
    const el = boxRef.current;
    if (!el) return;
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 90;
    if (el.scrollTop < 120 && visible < rows.length) {
      const prevH = el.scrollHeight;
      const prevTop = el.scrollTop;
      setVisible((v) => Math.min(rows.length, v + HIST_PAGE));
      requestAnimationFrame(() => {
        if (!boxRef.current) return;
        boxRef.current.scrollTop = boxRef.current.scrollHeight - prevH + prevTop;
        stickRef.current = false;
      });
    }
  };

  const onWheel = (e) => { if (e.deltaY < 0) stickRef.current = false; };

  const shown = rows.length > visible ? rows.slice(rows.length - visible) : rows;
  const folded = runSteps.filter((s) => s.folded);
  const liveSteps = runSteps.filter((s) => !s.folded);
  const empty = rows.length === 0 && !live && !pending && runSteps.length === 0;

  return html`<div class="messages-wrap">
    <div class="messages" id="messages" ref=${boxRef} onScroll=${onScroll} onWheel=${onWheel}>
      ${rows.length > visible ? html`<div class="load-older" />` : null}
      <${MessageList} rows=${shown} />
      ${folded.length ? html`<${StepsCard} steps=${folded} />` : null}
      <${LiveSteps} steps=${liveSteps} running=${running} />
      ${live ? html`<${LiveMessage} live=${live} />` : null}
      ${pending && !live ? html`<${PendingMessage} />` : null}
      ${empty ? html`<${Placeholder} />` : null}
    </div>
  </div>`;
}
