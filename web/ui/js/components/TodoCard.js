// 待办清单卡(todo_write):三态图标 + 文本 + 完成数统计,**默认展开**。
//
// 为什么单独画:todo_write 的参数(todos=[{content,status}])本身就是一张清单,
// 摊成 JSON 给人看是最浪费的画法 —— 读者要的是"计划里几条做完了、现在在做哪一条"。
// 历史消息与「正在执行」卡走同一个组件,两边看到的是同一份结构(lib/tools.js 的
// todoItemsOf 算出来的东西)。
//
// 类名跟着 ThinkBox 的 .think-box-* 命名法走:.todo-card / .todo-card-hd /
// .todo-card-body + 里面的 .todo-item。颜色一律走主题变量(--text3 那档淡色),
// 见 css/layers.css 的对应段落。
import { h } from "preact";
import htm from "htm";

import { TODO_ICON, TODO_STATUS_TEXT } from "../lib/tools.js";

const html = htm.bind(h);

// TodoCard 无 hook:todos 由 lib/tools.js 的 todoItemsOf 算好,读不出清单就不该
// 渲染这个组件(组件对空清单也只画一个空壳,不会崩)。
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
