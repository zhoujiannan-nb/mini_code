/* ---------------- 输入框 ---------------- */
function autoGrow() {
  const i = $("#input");
  i.style.height = "auto";
  i.style.height = Math.min(i.scrollHeight, 160) + "px";
}
function setChatTitle(title, sub) {
  $("#chat-title").textContent = title;
  $("#chat-title").title = title ? title + "(双击重命名)" : "双击重命名";
  $("#chat-sub").textContent = sub || "";
  paintChatAva(title);
}
// 聊天头头像:仅单聊显示。用统一的 agent 默认头像(和消息区 agentAvatarBox 同一张脸),
// 群聊/新会话隐藏。设了自定义图 state.agentAvatar 就显示图片。
function paintChatAva(title) {
  const el = $("#chat-ava");
  if (!el) return;
  if (!state.current || state.currentGroup) {
    el.textContent = "";
    el.style.display = "none";
    return;
  }
  if (state.agentAvatar) {
    el.classList.remove("agent-ava");
    el.innerHTML = '<img src="' + state.agentAvatar + '" alt="agent 头像">';
  } else {
    el.className = "chat-ava agent-ava";
    el.textContent = AGENT_DEFAULT_LETTER;
  }
  el.style.display = "flex";
}

/* ---------------- Token 用量(composer 下方) ---------------- */
function fmtK(n) {
  if (!n) return "—";
  if (n >= 1000) {
    const k = n / 1000;
    return (k >= 100 ? Math.round(k) : Math.round(k * 10) / 10) + "K";
  }
  return String(n);
}
// 统一 K 单位(最多 2 位小数,去尾零):135557 → "135.56K",260000 → "260K"
function fmtK2(n) {
  n = n || 0;
  return (Math.round((n / 1000) * 100) / 100) + "K";
}
function paintTokens(used) {
  const el = $("#token-usage");
  const cap = state.ctxWindow;
  if (!state.current) {
    el.textContent = "Token 0K / " + fmtK(cap);
    el.classList.remove("warn");
    return;
  }
  el.textContent = "Token " + fmtK2(used) + " / " + (cap ? fmtK2(cap) : "—") +
    (cap ? " · " + Math.min(999, Math.round((used || 0) * 100 / cap)) + "%" : "");
  el.classList.toggle("warn", cap > 0 && (used || 0) / cap >= 0.8);
}
// paintCacheHit renders the KV prefix-cache hit rate of the last LLM call.
// cached/prompt is the input-only hit ratio (output excluded, matching how
// providers bill cache). `reported` distinguishes "the provider reported a
// genuine 0% hit" (show 0%) from "this deployment does not report cache hits
// at all" (show n/a), so a real miss is no longer mislabeled as unknown.
function paintCacheHit(prompt, cached, reported) {
  const el = $("#cache-hit");
  if (!el) return;
  if (!prompt || prompt <= 0) { el.textContent = ""; el.className = "token-usage"; return; }
  if (!reported) {
    el.textContent = "缓存 n/a";
    el.className = "token-usage nohit";
    return;
  }
  const pct = Math.min(100, Math.round((cached || 0) * 100 / prompt));
  el.textContent = "缓存 " + pct + "% (" + fmtK(cached || 0) + "/" + fmtK(prompt) + ")";
  el.className = "token-usage";
}
// 缓存命中是"上一轮 LLM 调用"的瞬态指标:后端只在 usage 事件里推送一次,
// 刷新页面 / 切会话 / 重启进程后不会重发,指标就空了(看起来像"功能没了")。
// 这里把每个会话最近一次 usage 记进 localStorage,切回或刷新时回显最后一次的值。
const LS_CACHEHIT_KEY = "mini_code.cache_hit";
function cacheHitStore() {
  try { return JSON.parse(localStorage.getItem(LS_CACHEHIT_KEY) || "{}"); } catch (e) { return {}; }
}
function rememberCacheHit(sid, prompt, cached, reported) {
  if (!sid || !prompt || prompt <= 0) return;
  const m = cacheHitStore();
  m[sid] = { p: prompt, c: cached || 0, r: !!reported };
  const keys = Object.keys(m);
  if (keys.length > 50) for (const k of keys.slice(0, keys.length - 50)) delete m[k]; // 防无限增长
  try { localStorage.setItem(LS_CACHEHIT_KEY, JSON.stringify(m)); } catch (e) { /* 忽略配额/隐私模式 */ }
}
function restoreCacheHit(sid) {
  const e = sid && cacheHitStore()[sid];
  if (e && e.p > 0) paintCacheHit(e.p, e.c, e.r);
  else paintCacheHit(0, 0, false);
}
/* ---------------- 输出速度 ---------------- */
 // 生成中:content_delta 累积下发,用滚动窗口(最近 4s)的字符增长估算
 // 输出速度(≈ token/s,按 ~3 字符/token 折算混合中英文);
 // 每次 LLM 调用结束后的 usage 事件带后端实测解码速度(completion_tokens /
 // 首token→流结束,已剔除 TTFT),覆盖估算值并持久化进会话数据库;
 // 任务停止后保留最后一次的展示值(实测值优先,否则最后的实时估算)。
const SPEED_WINDOW_MS = 4000;
const speedState = { samples: [], timer: null };
function speedDelta(text) {
  const now = Date.now();
  const len = (text || "").length;
  const s = speedState.samples;
  // 新一轮 LLM 调用从更短(或空)的内容重新开始:清掉旧窗口,避免跨调用混算
  if (s.length && len < s[s.length - 1][1]) s.length = 0;
  s.push([now, len]);
  while (s.length > 2 && now - s[0][0] > SPEED_WINDOW_MS) s.shift();
  if (!speedState.timer) {
    speedState.timer = setInterval(speedTick, 1000);
  }
  speedTick();
}
function speedTick() {
  const s = speedState.samples;
  if (s.length >= 2) {
    const t0 = s[0][0], l0 = s[0][1];
    const t1 = s[s.length - 1][0], l1 = s[s.length - 1][1];
    const dt = (t1 - t0) / 1000;
    if (dt > 0.5 && l1 > l0) paintSpeed((l1 - l0) / dt / 3, false);
  }
  if (!state.running) speedStopTicker();
}
function speedStopTicker() {
  if (speedState.timer) { clearInterval(speedState.timer); speedState.timer = null; }
}
// paintSpeed 展示输出速度。accurate=true 为后端实测解码速度(完成token/
// 首token→流结束,已剔除 TTFT),false 为前端实时估算(带 ≈)。v<=0 清空。
function paintSpeed(v, accurate) {
  const el = $("#output-speed");
  if (!el) return;
  if (!v || v <= 0) { el.textContent = ""; el.className = "token-usage"; return; }
  const n = v >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  el.textContent = "速度" + (accurate ? "" : " ≈") + " " + n + " token/s";
  el.className = "token-usage";
}
async function updateTokenUsage() {
  if (!state.current) { paintTokens(0); return; }
  try {
    const data = await api("/api/sessions/" + state.current);
    if (state.current !== data.session.session_id) return; // 已切走
    paintTokens(data.tokens);
  } catch (e) { /* 忽略:会话可能刚被删除 */ }
}
  function resetToNew() {
    state.current = null;
    state.workDir = "";   // 新会话还没有目录:相对路径的链接会提示缺少基准目录
    state.pendingTask = null;
    clearSending();
    saveSession(null);
  // setRunning(false) 已负责按钮与侧边栏刷新,这里不再重复渲染
  setRunning(false); // 新会话没有运行中的任务;旧会话仍在跑的,点回它可恢复
  liveRemove();
  pendingRemove();
  messagesEl().innerHTML = "";
  histFull = []; histStart = 0; // 懒加载状态随容器一起清空
  showPlaceholder();
  setChatTitle("新会话", "发送后自动创建会话");
  state.mode = "full"; // 新会话默认完整模式
  state.model = state.defaultModel || ""; // 新会话默认模型
    state.contextReminders = true; // 新会话默认开启上下文提醒
  state.thinking = ""; // 新会话:思考模式用模型默认
  state.reasoningEffort = "";
  state.ctxWindow = state.defaultCtxWindow;
  state.children = [];
  paintMode();
  paintModel();
  paintContextReminders();
  paintTokens(0);
  paintCacheHit(0, 0, false);
  speedStopTicker();
  paintSpeed(0, false); // 新会话:清空上一会话的速度展示
  paintBackBtn("");
  closeTasksPanel();
  renderTasksPanel();
}
async function selectSession(id) {
  mcEmit("session");
  if (state.currentGroup) exitGroupView();
  state.current = id;
  liveRemove();
  pendingRemove();
  try {
    const data = await api("/api/sessions/" + id);
    if (state.current !== id) return; // 等待期间又切走了,丢弃过期响应
    const s = data.session;
    // 工作目录:聊天里的相对路径要按它解析(不是服务进程的 cwd)
    state.workDir = s.work_dir || "";
    setChatTitle(s.title || "会话 " + id.slice(0, 8), (s.work_dir || "").slice(0, 60));
    // 侧栏跟着会话走:从 mc-session 芯片跳进来的会话可能属于另一个 worker,
    // 不切过去就像跳错了地方——左侧列表里连它自己都找不到。不 await:侧栏慢半拍
    // 不该拖住聊天区的重绘。
    if (typeof followSessionWorker === "function") followSessionWorker(s.worker_id || "");
    // await 期间该会话若在运行,delta 可能已建好流式块/转圈——renderHistory
    // 会清空整个容器使引用悬空,重绘前再清一次(流式是累积下发,下个 delta 自动补齐)
    liveRemove();
    pendingRemove();
    renderHistory(data.messages);
    // 权限模式:以服务端为准(谨慎/完整)
    state.mode = s.mode === "cautious" ? "cautious" : "full";
    paintMode();
    // 模型:以服务端为准(空 = 默认模型);token 条窗口跟着会话模型走
    state.model = s.model || state.defaultModel || "";
    // 思考模式:以服务端为准(空 = 模型默认)
    state.thinking = s.thinking || "";
    state.reasoningEffort = s.reasoning_effort || "";
    if (data.context_window) state.ctxWindow = data.context_window;
    paintModel();
    paintStatusPill(); // 顶部胶囊:模型名跟着会话走(切会话必刷)
    // 上下文提醒开关:以服务端为准(缺省 = 开)
    state.contextReminders = s.context_reminders !== false; // 缺省 = 开(与服务端一致)
    paintContextReminders();
    paintBackBtn(s.parent_id);
    closeTasksPanel();
    loadChildren();
    // 执行状态以服务端为准:会话正在跑时恢复停止按钮,并提示任务仍在进行
    // (刷新页面不会中断服务端的任务)。setRunning 已同步按钮/侧边栏/压缩键。
    setRunning(!!s.active);
    if (s.active) {
      // 刷新不中断服务端任务:历史中已持久化的输出(含工具执行前已完成的回复)
      // 已由 renderHistory 回显;流式中的输出为累积下发,重连后自动补齐。
      addStatus("⏳ 任务正在执行中 — 刷新前的已完成输出已回显,后续输出将实时显示");
      // 事件账本对账:补上已发生的工具结果、给没有回音的调用恢复计时起点
      // ("执行 xxs"从真实发起时刻继续)、还在等的挂上转圈。es.onopen 的那次
      // resync 跑在 state.running 置位之前,到不了这里——这才是刷新后的触发点。
      resyncRunning();
    }
    paintTokens(data.tokens);
    // 缓存命中 + 输出速度:优先用数据库里持久化的最后一次 LLM 调用实测值
    // (后端在每次任务中实时写入会话,停止/刷新/重启后仍保留);
    // 旧版本会话(无 last_usage)回退到 localStorage 里的最近值。
    const lu = data.last_usage;
    if (lu && (lu.prompt > 0 || lu.completion > 0)) {
      paintCacheHit(lu.prompt, lu.cached, lu.reported);
      paintSpeed(lu.speed || 0, true);
    } else {
      restoreCacheHit(id); // 切会话:回显该会话最近一次的缓存命中(没有则留空)
      paintSpeed(0, false);
    }
    saveSession(id);
  } catch (e) {
    toast("打开会话失败:" + e.message, "err");
  }
}

