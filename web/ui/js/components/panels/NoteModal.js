// 两个小弹窗:worker 公约编辑器、程序插件日志。都挂在 body 层(#app 是
// display:contents,所以它们仍然是 body 的布局子节点)。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, setState } from "../../lib/store.js";
import { closeNoteEditor, saveNote } from "../../lib/actions.js";

const html = htm.bind(h);

export function WorkerNoteModal() {
  const note = useStore((s) => s.workerNote);
  const [text, setText] = useState("");
  useEffect(() => { if (note) setText(note.content || ""); }, [note]);
  if (!note) return null;
  return html`<div id="wk-note-box" class="wk-note">
    <div class="wk-note-head">公约 · ${note.name}</div>
    <div class="wk-note-path">当前版本:v${note.ver || 0}</div>
    <textarea class="wk-note-area" spellcheck="false" value=${text}
      onInput=${(e) => setText(e.target.value)}></textarea>
    <div class="wk-note-foot">
      <span class="wk-note-tip">保存后同 worker 的会话下一轮自动收到更新</span>
      <button class="wk-note-btn" id="wk-note-cancel" onClick=${closeNoteEditor}>关闭</button>
      <button class="wk-note-btn primary" id="wk-note-save" onClick=${() => saveNote(text)}>保存</button>
    </div>
  </div>`;
}

export function PluginLogsModal() {
  const logs = useStore((s) => s.pluginLogs);
  if (!logs) return null;
  return html`<div class="modal-mask open" id="pl-log-mask"
    onClick=${(e) => { if (e.target.id === "pl-log-mask") setState({ pluginLogs: null }); }}>
    <div class="modal modal-620">
      <div class="set-head">
        <h2 id="pl-log-title">日志</h2>
        <button class="set-close" id="pl-log-close" aria-label="关闭"
          onClick=${() => setState({ pluginLogs: null })}>&#10005;</button>
      </div>
      <pre class="pl-log" id="pl-log-body">${(logs.lines || []).length
        ? logs.lines.join("\n")
        : "(还没有输出)"}</pre>
    </div>
  </div>`;
}
