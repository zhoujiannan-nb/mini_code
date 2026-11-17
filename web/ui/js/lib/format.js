
export function fmtK(n) {
  if (!n) return "—";
  if (n >= 1000) {
    const k = n / 1000;
    return (k >= 100 ? Math.round(k) : Math.round(k * 10) / 10) + "K";
  }
  return String(n);
}

export function fmtK2(n) {
  n = n || 0;
  return (Math.round((n / 1000) * 100) / 100) + "K";
}

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

export function fmtTok(n) {
  if (!n || n < 1) return "";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M tok";
  if (n >= 1e3) return Math.round(n / 1e3) + "k tok";
  return n + " tok";
}

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

export function paintSpeed(v, accurate) {
  if (!v || v <= 0) return "";
  const n = v >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  return (accurate ? "" : "≈ ") + n + " token/s";
}

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

export const fmtSec = fmtRun;

export function fmtDurMs(ms) {
  ms = Number(ms) || 0;
  if (ms < 10000) return (Math.round(ms / 100) / 10) + "秒";
  const sec = Math.round(ms / 1000);
  return Math.floor(sec / 60) + "分" + (sec % 60) + "秒";
}
