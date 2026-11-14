// 消息区:keyed + memo 的列表、智能滚动、历史懒加载,以及"过程条 / 正在执行卡 /
// 流式块 / 转圈"这四层的顺序。
//
// 流式 delta 只改 state.live,所以这段组件里只有 LiveMessage 会重渲;历史列表被
// MessageList(memo)按 rows 数组引用挡在外面 —— 不再有旧实现里"每个 delta 把整条
// 消息 mdToHtml(全文) 重算一遍"的 O(n²)。
import { h } from "preact";
import { useState, useEffect, useLayoutEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore } from "../lib/store.js";
import { memo } from "../lib/memo.js";
import { Message, LiveMessage, PendingMessage, Placeholder } from "./Message.js";
import { LiveSteps, StepsCard } from "./Steps.js";

const html = htm.bind(h);

// 打开会话只渲染最近 HIST_PAGE 条,更早的等用户上滑接近顶部再按批补齐。
// 100 条:工具步骤本来就折进「执行过程」一条里,10 条常常"翻两下就到顶"。
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

  const [visible, setVisible] = useState(HIST_PAGE);
  const boxRef = useRef(null);
  const stickRef = useRef(true); // 用户当前是否"在底部附近"
  const lastScrollHeight = useRef(0);

  // 切会话:回到最近一页,并强制跟到底(旧行为:初始视图直接落在最新一条)。
  useEffect(() => {
    setVisible(HIST_PAGE);
    stickRef.current = true;
  }, [current]);

  // 追加内容前的高度快照由 onScroll 维护的 stick 决定:
  // 用户手动上滚回看历史时不抢视角。
  const contentKey = live ? (live.content || "").length + ":" + (live.reasoning || "").length : "";
  useLayoutEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    if (stickRef.current) el.scrollTop = el.scrollHeight;
    lastScrollHeight.current = el.scrollHeight;
  }, [rows, runSteps, contentKey, pending]);

  const onScroll = () => {
    const el = boxRef.current;
    if (!el) return;
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 90;
    if (el.scrollTop < 120 && visible < rows.length) {
      // 上滑接近顶部:先记住位置,补一批更早的,再把视口放回原处。
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

  // 向上滚轮 = 用户明确要回看:立刻解锁吸底。不能只靠 onScroll 的 90px 判定——
  // 流式高频追加时,小幅上滚还没来得及触发 scroll 事件就被下一块内容吸回底部,
  // 体验上就是"往上翻翻不动、像卡住"。
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
