import { h } from "preact";
import htm from "htm";

import { useStore } from "../lib/store.js";
import { bootPlaySettings, bootPick, bootSetEnabled, bootSetMode, bootSetMute, bootPreview } from "../lib/settings.js";

const html = htm.bind(h);

const MODES = [
  ["session", "每个会话一次"],
  ["always", "每次都播"],
  ["daily", "每天一次"],
];

export function bootSummaryText(boot, play) {
  const ok = ((boot && boot.themes) || []).filter((t) => !t.error);
  const p = play || bootPlaySettings();
  const cur = ok.find((t) => t.id === (boot && boot.picked));
  if (!ok.length) return "没有装 —— 去插件市场挑一个";
  const modes = { session: "每个会话一次", always: "每次都播", daily: "每天一次" };
  return (p.enabled ? "已启用" : "已关闭") +
    " · " + (cur ? cur.name || cur.id : "未选择") +
    (p.enabled ? " · " + (modes[p.mode] || p.mode) : "") +
    " · 已装 " + ok.length + " 个";
}

export function BootSummary() {
  const boot = useStore((s) => s.boot);
  return html`<span id="a-boot-summary" class="hint">${bootSummaryText(boot, bootPlaySettings())}</span>`;
}

export function BootPlayPanel({ plugin }) {
  const boot = useStore((s) => s.boot);
  const player = bootPlaySettings();
  const isCur = boot.picked === plugin.id;
  return html`<div class="pl-d-sec pl-play">
    <div class="pl-d-sec-title">开机播放</div>
    ${isCur
      ? html`<div class="pl-play-state on"><span class=${"pl-dot " + (player.enabled ? "ok" : "")}></span>${player.enabled ? "正在使用" : "已关闭"}</div>`
      : html`<div class="pl-play-state">当前用的是别的动画 —— 点「设为开机动画」切过来</div>`}
    <div class="pl-seg" role="group" aria-label="播放时机">
      ${MODES.map(([v, label]) => html`<button class=${"pl-seg-btn" + (v === player.mode ? " on" : "")}
        data-act="boot-mode" data-mode=${v} type="button" key=${v}
        onClick=${() => bootSetMode(v)}>${label}</button>`)}
    </div>
    <div class="pl-play-row">
      <label class="ck-row"><input type="checkbox" data-act="boot-enabled" checked=${player.enabled}
        onChange=${(e) => bootSetEnabled(e.target.checked)} />启用</label>
      <label class="ck-row"><input type="checkbox" data-act="boot-mute" checked=${player.mute}
        onChange=${(e) => bootSetMute(e.target.checked)} />静默</label>
    </div>
  </div>`;
}

export function bootActions(plugin, picked) {
  return {
    preview: () => bootPreview(plugin.id),
    set: () => bootPick(plugin.id),
    isCur: picked === plugin.id,
  };
}
