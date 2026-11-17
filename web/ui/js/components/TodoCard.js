import { h } from "preact";
import htm from "htm";

import { TODO_ICON, TODO_STATUS_TEXT } from "../lib/tools.js";

const html = htm.bind(h);

export function TodoCard({ todos }) {
  const t = todos || { items: [], done: 0, doing: 0, total: 0, label: "0/0" };
  return html`<div class="todo-card">
    <div class="todo-card-hd">
      <span class="td-tag">🗒 待办清单</span>
      <span class="td-n" title="已完成 / 总数">${t.done}/${t.total}</span>
      ${t.doing ? html`<span class="td-now">进行中 ${t.doing}</span>` : null}
    </div>
    <ul class="todo-card-body">
      ${t.items.map((it, i) => html`<li key=${i} class=${"todo-item " + it.status}>
        <span class="td-ico" title=${TODO_STATUS_TEXT[it.status] || ""}>${TODO_ICON[it.status] || TODO_ICON.pending}</span>
        <span class="td-text">${it.text}</span>
      </li>`)}
    </ul>
  </div>`;
}
