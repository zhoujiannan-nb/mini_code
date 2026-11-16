/* ---------------- 头像(用户 / agent 共享) ----------------
   旧 avatars.js 的纯逻辑原样搬过来(哈希取色、首字回退、256×256 裁剪);
   渲染从"返回 HTML 字符串"改成两个组件 —— 组件产出的 DOM 和旧字符串一字不差,
   所以 CSS 选择器(.c-avatar / .c-avatar-fallback / .agent-ava / [data-uav])全部照旧。

   为什么不再用 data-uav 原地重绘:头像存在 store 里,组件订阅它,换图自然重渲,
   不需要 document.querySelectorAll + replaceWith 那一套命令式补丁。 */
import { h } from "preact";
import htm from "htm";
import { useStore } from "./store.js";
import { esc } from "./markdown.js";

const html = htm.bind(h);

/* ===== 头像统一渲染(单聊/个人信息同一套) ===== */
// 哈希取色:种子稳定 → 颜色固定(agent 用会话 id 做种子,改名/换机也不变)
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
// 用户头像回退底色用固定种子,所有未设置头像的用户长一个样(旧行为)
export const USER_FALLBACK_BG = hashColor("user");
export const AGENT_DEFAULT_LETTER = "M";

// agent 的默认"脸":与欢迎页 Hero / 侧栏徽标 / 应用图标同一张斜体 M(多边形,不依赖字体)。
export const AGENT_MARK = html`<svg class="ava-mark" viewBox="0 0 100 100" aria-hidden="true"><g
  transform="translate(50 51.25) skewX(-8) translate(-50 -51.25)">
  <polygon fill="currentColor"
    points="16.25,82.5 16.25,20 36.25,20 50,47.5 63.75,20 83.75,20 83.75,82.5 69,82.5 69,43.8 50,71.2 31.25,43.8 31.25,82.5"/>
</g></svg>`;

// 用户头像:已上传 → 图片;未设置 → 首字"你"+哈希色回退。
// data-uav 保留:全局点击委托靠它识别"点的是用户头像 → 打开头像弹窗"。
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

// agent 头像:统一默认头像(墨色 + "M"),不再从会话派生(标题首字 + id 哈希色)。
// tag="span" 用于侧栏条目那种只有一套盒子的位置(底色靠 .agent-ava 覆盖);
// 消息行才有 c-avatar(图) / c-avatar-fallback(字)两版。
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

// 文件 → 256×256 方形 data URL(居中裁切;透明图铺白底;JPEG 0.85)
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

// 头像文件校验:格式 + 4MB 上限(用户头像与 agent 头像共用)
export function validateAvatarFile(f) {
  if (!f) return "没有选择文件";
  if (!/image\/(jpeg|png|webp)/.test(f.type)) return "仅支持 jpg / png / webp";
  if (f.size > 4 * 1024 * 1024) return "图片超过 4MB";
  return "";
}

// 参数里保留 esc 的引用:旧模块导出了它,组件里偶尔需要在 title 上拼字符串。
export const avatarEsc = esc;
