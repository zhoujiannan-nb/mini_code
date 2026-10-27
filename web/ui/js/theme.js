/* ---------------- 主题(浅色 / 深色 / 跟随系统) ---------------- */
// 纯客户端偏好,存 localStorage(浏览器与桌面 WebView2 均持久,见 %APPDATA%\mini_code)。
// "跟随系统" 读 prefers-color-scheme:网页端跟随浏览器/OS,桌面窗口跟随 Windows 深浅色模式。
// 首屏由 <head> 内联脚本先行应用(防闪烁),这里负责交互与后续同步。
const LS_THEME_KEY = "mc-theme"; // "light" | "dark" | "system"

function readThemeMode() {
  try {
    const m = localStorage.getItem(LS_THEME_KEY);
    return (m === "light" || m === "dark" || m === "system") ? m : "system";
  } catch (e) { return "system"; }
}
function systemPrefersDark() {
  return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
}
function applyTheme() {
  const m = readThemeMode();
  const dark = m === "dark" || (m === "system" && systemPrefersDark());
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
  // 桌面窗口:同步原生标题栏深浅色(浏览器端无此函数,忽略)
  try { if (typeof window.mcSetDarkTitleBar === "function") window.mcSetDarkTitleBar(dark); } catch (e) {}
  refreshThemeUI();
}
function setThemeMode(m) {
  try { localStorage.setItem(LS_THEME_KEY, m); } catch (e) {}
  applyTheme();
}
/* 顶栏"主题"按钮:浅色 → 深色 → 跟随系统 一键循环(跟随系统时把当前生效的深浅色也标出来)。
   和配置弹窗里的下拉框读同一个 localStorage key,任意一边改,另一边跟着同步。 */
const THEME_UI = {
  light:  { icon: "☀", label: "浅色" },
  dark:   { icon: "☾", label: "深色" },
  system: { icon: "🖥", label: "跟随系统" },
};
function refreshThemeUI() {
  const m = readThemeMode();
  const ui = THEME_UI[m] || THEME_UI.system;
  const eff = document.documentElement.getAttribute("data-theme") === "dark" ? "深色" : "浅色";
  const suffix = m === "system" ? "(" + eff + ")" : "";
  const btn = document.getElementById("btn-theme");
  if (btn) {
    const _lbl = btn.querySelector(".lbl");
    if (_lbl) _lbl.textContent = ui.label + suffix;
    else btn.textContent = ui.icon + " " + ui.label + suffix;
    btn.title = "当前:" + ui.label + suffix + " · 点击切换(浅色 / 深色 / 跟随系统)";
  }
  const sel = document.getElementById("c-theme");
  if (sel && sel.value !== m) sel.value = m;
}
function cycleThemeMode() {
  const order = ["light", "dark", "system"];
  const next = order[(order.indexOf(readThemeMode()) + 1) % order.length];
  setThemeMode(next);
  const ui = THEME_UI[next];
  toast("主题:" + ui.label + (next === "system" ? "(当前 " + (systemPrefersDark() ? "深色" : "浅色") + ")" : ""), "ok");
}
applyTheme(); // 兜底:确保首屏状态与设置一致
if (window.matchMedia) {
  const _mq = window.matchMedia("(prefers-color-scheme: dark)");
  const _onSysTheme = () => { if (readThemeMode() === "system") applyTheme(); };
  if (_mq.addEventListener) _mq.addEventListener("change", _onSysTheme);
  else if (_mq.addListener) _mq.addListener(_onSysTheme); // 老版本 WebView2
}
{
  // 主题按钮:循环切换;配置弹窗的 change 事件仍走原逻辑(setThemeMode)
  const _tb = document.getElementById("btn-theme");
  if (_tb) _tb.addEventListener("click", cycleThemeMode);
}

const state = {
  sessions: [],
  archived: [],     // 已归档会话(不在主会话列表显示;侧边栏"已归档"分区,平常收起)
  current: null,      // 当前会话 id,null = 新会话
  userAvatar: "",     // 用户头像 data URL(全局,存服务端配置;"" = 首字+哈希色回退)
  running: false,     // 当前会话是否有活动任务(以服务端为准)
   pendingTask: null,  // 等待 session_created 的 task_id
    recentCreated: [], // 最近收到的 session_created 事件队列(竞态兜底:事件比 POST 响应早到时,响应回来按 task_id 精确匹配接管)
  sending: false,     // 消息已发出、task_started 还没回来(此期间不要用轮询重置 running)
  sendingTimer: null, // sending 的兜底超时(SSE 断连漏掉 task_started 时靠它收敛)
  live: null,         // 当前流式输出块(DOM)
  pending: null,      // 等待模型首 token 的转圈块(DOM)
  ctxWindow: 0,       // 当前会话模型的上下文窗口(token)
  defaultCtxWindow: 0,// 默认模型的上下文窗口(新会话视图用)
  models: [],         // 已配置的模型列表(来自 /api/status)
  defaultModel: "",   // 默认模型名
  model: "",          // 当前会话的模型名;新会话视图下 = 下一条消息创建会话时使用的模型
  pendingImages: [],  // 待发送的截图 data URL(data:image/...;base64,...)
  mode: "full",       // 当前会话的权限模式;新会话视图下 = 下一条消息创建会话时使用的模式
    contextReminders: true, // 上下文提醒开关;新会话视图下 = 创建会话时的偏好(默认开)
  thinking: "",       // 当前会话的思考开关覆盖("" = 用模型默认;enabled / disabled)
  reasoningEffort: "",// 当前会话的思考深度覆盖("" = 用模型默认;随 provider 取值)
  approvals: {},      // id → payload(待审批队列,跨会话)
  approvalShown: null,// 审批弹窗里正在展示的 id
  approvalTimer: null,
  children: [],       // 当前会话的子任务(子代理会话)摘要
};

