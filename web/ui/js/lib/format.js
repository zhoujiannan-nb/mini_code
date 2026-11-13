// 纯格式化工具。原来散在 usage.js / tasks.js / jobs.js / steps.js / plugins.js 里,
// 这里集中一份,组件只 import 需要的那几个 —— 没有 DOM、没有状态,单测友好。

// fmtK:紧凑 K 单位。0 → "—"(旧行为:未上报不等于 0)。
export function fmtK(n) {
  if (!n) return "—";
  if (n >= 1000) {
    const k = n / 1000;
    return (k >= 100 ? Math.round(k) : Math.round(k * 10) / 10) + "K";
  }
  return String(n);
}

// fmtK2:统一 K 单位(最多 2 位小数,去尾零):135557 → "135.56K",260000 → "260K"
export function fmtK2(n) {
  n = n || 0;
  return (Math.round((n / 1000) * 100) / 100) + "K";
}

// relTime:侧栏里的相对时间。解析不了就原样显示(服务端可能给空串)。
export function relTime(s) {
  if (!s) return "";
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return "刚刚";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return (d.getMonth() + 1) + "-" + d.getDate() + " " +
    String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

export function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10240 ? 1 : 0) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

export function fmtUptime(s) {
  s = Number(s) || 0;
  if (s < 60) return s + " 秒";
  if (s < 3600) return Math.floor(s / 60) + " 分钟";
  if (s < 86400) return Math.floor(s / 3600) + " 小时";
  return Math.floor(s / 86400) + " 天";
}

// byteLenOf:字节数(和 TextEncoder 一致;浏览器没有 TextEncoder 时退回 UTF-16 长度)。
export function byteLenOf(s) {
  const t = String(s == null ? "" : s);
  return typeof TextEncoder === "function" ? new TextEncoder().encode(t).length : t.length;
}

export function fmtRun(sec) {
  if (sec < 60) return sec + "s";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return s ? m + "m" + s + "s" : m + "m";
}

// fmtTok:子任务 token 合计的紧凑写法(4.4M / 128k);0 返空串。
export function fmtTok(n) {
  if (!n || n < 1) return "";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M tok";
  if (n >= 1e3) return Math.round(n / 1e3) + "k tok";
  return n + " tok";
}

// fmtTaskDur:运行中 = now - 创建;已结束 = 最后更新 - 创建。
// created_at/updated_at 是带时区的 RFC3339,Date.parse 直接可吃。
export function fmtTaskDur(c, now) {
  const t0 = Date.parse(c.created_at);
  if (isNaN(t0)) return "";
  const t1 = c.active ? now : (Date.parse(c.updated_at) || now);
  const sec = Math.max(0, Math.round((t1 - t0) / 1000));
  if (sec < 60) return sec + "秒";
  const m = Math.floor(sec / 60);
  if (m < 60) return m + "分" + (sec % 60) + "秒";
  return Math.floor(m / 60) + "小时" + (m % 60) + "分";
}

// 缓存命中率的展示文案。纯函数:Composer 画、测试也能直接断言。
// `reported` 区分"provider 真的报了 0%"与"这个部署根本不上报缓存命中"(n/a),
// 这样一次真的 miss 不会被误标成未知。
export function paintCacheHit(prompt, cached, reported) {
  if (!prompt || prompt <= 0) return { text: "", className: "token-usage" };
  if (!reported) return { text: "缓存 n/a", className: "token-usage nohit" };
  const pct = Math.min(100, Math.round((cached || 0) * 100 / prompt));
  return {
    text: "缓存 " + pct + "% (" + fmtK(cached || 0) + "/" + fmtK(prompt) + ")",
    className: "token-usage",
  };
}

// 输出速度的展示文案。accurate=true 为后端实测(完成 token / 首 token→流结束),
// false 为前端实时估算(带 ≈)。v<=0 清空。
export function paintSpeed(v, accurate) {
  if (!v || v <= 0) return "";
  const n = v >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  return "速度" + (accurate ? "" : " ≈") + " " + n + " token/s";
}

// jobElapsed:运行中行用当前时刻,结束行用 ended_at;小时封顶。
export function jobElapsed(j, now) {
  const t0 = Date.parse(j.started_at || "");
  if (isNaN(t0)) return "";
  const end = j.status === "running" ? (now || Date.now())
    : (j.ended_at ? Date.parse(j.ended_at) : (now || Date.now()));
  let sec = Math.max(0, Math.floor((end - t0) / 1000));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60);
  sec = sec % 60;
  if (h > 0) return h + "时" + m + "分";
  if (m > 0) return m + "分" + sec + "秒";
  return sec + "秒";
}

// 秒数 → "12s" / "1m20s"(工具步骤计时用,和 fmtRun 同义但保留独立名字便于阅读)
export const fmtSec = fmtRun;
