import { h } from "preact";
import htm from "htm";

const html = htm.bind(h);

export function FsPicker({ open, q, hits, sel, indexing, err, onPick, onHover }) {
  if (!open) return null;
  return html`<div class="fs-picker" id="fs-picker">
    <div class="fs-head">
      <span>📁 插入文件夹路径</span>
      <span class="fs-q" id="fs-q">@${q}</span>
      <span class="fs-hint">↑↓ 选择 · Enter 确认 · Esc 取消</span>
    </div>
    <div class="fs-list" id="fs-list">
      ${err
        ? html`<div class="fs-empty">${err}</div>`
        : (hits || []).length === 0
          ? html`<div class="fs-empty">${indexing ? "🔍 正在建立目录索引,稍候…" : "没有匹配的文件夹"}</div>`
          : hits.map((h, i) => html`<div class=${"fs-item" + (i === sel ? " sel" : "")} data-i=${i} key=${i}
              onMouseDown=${(e) => { e.preventDefault(); onPick(i); }}
              onMouseEnter=${() => onHover(i)}>
              <div class="fs-name">📁 ${h.name}</div>
              ${h.parent && h.parent !== h.path ? html`<div class="fs-path">${h.parent}</div>` : null}
            </div>`)}
    </div>
  </div>`;
}
