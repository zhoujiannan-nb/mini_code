import { h } from "preact";
import htm from "htm";
import { useStore } from "./store.js";
import { esc } from "./markdown.js";

const html = htm.bind(h);

export const AVA_COLORS = ["#166534", "#1e40af", "#6b21a8", "#9d174d", "#9a3412", "#155e75", "#3730a3", "#713f12", "#991b1b", "#065f46", "#5b21b6", "#475569"];
export function hashColor(seed) {
  let h = 2166136261;
  const t = String(seed || "");
  for (let i = 0; i < t.length; i++) { h ^= t.charCodeAt(i); h = Math.imul(h, 16777619) >>> 0; }
  return AVA_COLORS[h % AVA_COLORS.length];
}
export function firstChar(name) {
  const t = String(name || "").trim();
  return t ? Array.from(t)[0] : "M";
}
export const USER_FALLBACK_BG = hashColor("user");
export const AGENT_DEFAULT_LETTER = "M";

export const AGENT_MARK = html`<svg class="ava-mark" viewBox="0 0 100 100" aria-hidden="true"><g
  transform="translate(50 51.25) skewX(-8) translate(-50 -51.25)">
  <polygon fill="currentColor"
    points="16.25,82.5 16.25,20 36.25,20 50,47.5 63.75,20 83.75,20 83.75,82.5 69,82.5 69,43.8 50,71.2 31.25,43.8 31.25,82.5"/>
</g></svg>`;

export function UserAvatar({ cls = "c-avatar", id }) {
  const src = useStore((s) => s.userAvatar);
  if (src) {
    return html`<div id=${id || null} class=${cls + " clickable"} data-uav=${cls} title="点击修改用户头像">
      <img src=${src} alt="用户头像" />
    </div>`;
  }
  return html`<div id=${id || null} class=${cls + "-fallback"} data-uav=${cls} title="点击设置用户头像"
    style=${"background:" + USER_FALLBACK_BG + ";color:#fff"}>你</div>`;
}

export function AgentAvatar({ cls = "c-avatar", tag = "div", id }) {
  const src = useStore((s) => s.agentAvatar);
  const title = useStore((s) => {
    const cur = (s.sessions || []).find((x) => x.session_id === s.current);
    return (cur && cur.title) || "Mini Code";
  });
  const mark = cls + "|" + (tag || "div");
  const Tag = tag === "span" ? "span" : "div";
  if (src) {
    return html`<${Tag} id=${id || null} class=${cls} data-aav=${mark} title=${title}>
      <img src=${src} alt="agent 头像" /></${Tag}>`;
  }
  const solid = tag === "span" ? cls : cls + "-fallback";
  return html`<${Tag} id=${id || null} class=${solid + " agent-ava"} data-aav=${mark} title=${title}>${AGENT_MARK}</${Tag}>`;
}

export function fileToAvatarDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("文件读取失败"));
    reader.onload = () => {
      const img = new Image();
      img.onload = () => {
        const c = document.createElement("canvas");
        c.width = 256; c.height = 256;
        const ctx = c.getContext("2d");
        ctx.fillStyle = "#fff";
        ctx.fillRect(0, 0, 256, 256);
        const side = Math.min(img.width, img.height);
        const sx = (img.width - side) / 2, sy = (img.height - side) / 2;
        ctx.drawImage(img, sx, sy, side, side, 0, 0, 256, 256);
        resolve(c.toDataURL("image/jpeg", 0.85));
      };
      img.onerror = () => reject(new Error("图片解析失败"));
      img.src = reader.result;
    };
    reader.readAsDataURL(file);
  });
}

export function validateAvatarFile(f) {
  if (!f) return "没有选择文件";
  if (!/image\/(jpeg|png|webp)/.test(f.type)) return "仅支持 jpg / png / webp";
  if (f.size > 4 * 1024 * 1024) return "图片超过 4MB";
  return "";
}

export const avatarEsc = esc;
