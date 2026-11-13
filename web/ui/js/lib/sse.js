// EventSource 连接、心跳看门狗、断线重连与 tail_run 对账。
// SSE 是发后即忘:服务端 30s 发一个 event:ping,前端据此判断连接还活着;
// 75s 收不到任何数据就强制重连(TCP 半开时 EventSource 自己不会察觉)。
import { ROUTES } from "./api.js";
import { dispatch, getState, setState, toast } from "./store.js";
import {
  resyncRunning, resyncSessionWorker, loadSessions,
  scheduleListRefresh, updateTokenUsage, rememberCacheHit,
} from "./actions.js";

let esRef = null;      // 当前 EventSource(看门狗重连时先关旧连接)
let sseAliveAt = 0;    // 最近一次收到服务端数据/心跳的时间
let sseWatchdog = null;
let esFactory = null;

// 测试注入点:render 测试用假 EventSource 替代浏览器实现。
export function setEventSourceFactory(fn) {
  esFactory = fn;
}

function makeEventSource(url) {
  if (esFactory) return esFactory(url);
  return new EventSource(url);
}

export function connectEvents() {
  if (esRef) { try { esRef.close(); } catch (e) {} esRef = null; }
  const es = esRef = makeEventSource(ROUTES.events);
  // 每次连接建立(首连/浏览器自动重连/看门狗重连)都对账一次。
  // 首连时 state.running 还是 false,resyncRunning 自己会退场,不多打接口。
  es.onopen = () => { resyncRunning(); };
  sseAliveAt = Date.now();
  es.onmessage = (e) => {
    sseAliveAt = Date.now();
    let m = null;
    try { m = JSON.parse(e.data); } catch (err) { return; }
    onEvent(m);
  };
  // 服务端每 30s 发一个 event:ping(真实 SSE 事件,不是注释行):
  // 即使 TCP 半开(收不到任何数据),看门狗也能在 75s 内发现并强制重连。
  es.addEventListener("ping", () => { sseAliveAt = Date.now(); });
  es.onerror = () => { /* EventSource 自动重连 */ };
  if (!sseWatchdog) {
    sseWatchdog = setInterval(() => {
      if (Date.now() - sseAliveAt > 75000) {
        toast("事件流无响应,已自动重连", "");
        connectEvents();
      }
    }, 10000);
    // Node 测试/桌面宿主里不需要这个定时器把进程钉住;浏览器里没有 unref。
    if (sseWatchdog && typeof sseWatchdog.unref === "function") sseWatchdog.unref();
  }
}

export function disconnectEvents() {
  if (esRef) { try { esRef.close(); } catch (e) {} esRef = null; }
  if (sseWatchdog) { clearInterval(sseWatchdog); sseWatchdog = null; }
}

/* ---------------- 事件分发 ---------------- */
// 先归约 state(纯函数),再做副作用(刷新列表 / 对账 / 统计)。副作用不在 reducer
// 里做,是为了让 reducer 能脱离浏览器单测。
export function onEvent(m) {
  const state = getState();
  dispatch({ type: "sse", msg: m });

  switch (m.kind) {
    case "approval_request":
    case "approval_resolved":
      return; // 跨会话,已由 reducer 处理
    case "status":
      if (m.status === "session_created") {
        scheduleListRefresh(1000);
        return;
      }
      if (m.status === "task_started") {
        // 服务端已把任务登记进活动表:立即拉列表,侧边栏"执行中"即时点亮。
        clearSending();
        loadSessions();
        return;
      }
      if (m.status === "task_done" || m.status === "interrupted") {
        stopSpeedTicker();
        updateTokenUsage();
        loadSessions();
        return;
      }
      updateTokenUsage();
      return;
    case "assistant":
    case "error":
      stopSpeedTicker(); // 任务结束:保留最后一次的展示值
      updateTokenUsage();
      loadSessions();
      return;
    case "usage":
      // 每轮 LLM 调用后后端推送的真实 usage,画缓存命中率与实测输出速度
      rememberCacheHit(m.session_id || state.current, m.prompt_tokens, m.cached_tokens, m.cache_reported);
      stopSpeedTicker();
      return;
    case "content_delta":
      speedDelta(m.content); // 实时估算输出速度(滚动窗口)
      return;
    case "tool_end":
      // workers 工具会改这条会话自己的归属(join/create/leave):跑完立刻把侧栏
      // 对齐到新的 worker,不然新会话加入了 worker 还停在「未分配」。
      if (m.tool_name === "workers") resyncSessionWorker(state.current);
      return;
    default:
      break;
  }

  // 其他会话的事件是"列表可能变了"的信号:不进入当前视图,但让侧栏在秒级收敛。
  if (m.session_id && m.session_id !== state.current) scheduleListRefresh();
}

function clearSending() {
  setState({ sending: false });
}

/* ---------------- 输出速度(实时估算) ---------------- */
// 生成中:content_delta 累积下发,用滚动窗口(最近 4s)的字符增长估算输出速度
// (≈ token/s,按 ~3 字符/token 折算混合中英文)。实测值由 usage 事件覆盖。
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
  if (!speedState.timer) speedState.timer = setInterval(speedTick, 1000);
  if (speedState.timer && typeof speedState.timer.unref === "function") speedState.timer.unref();
  speedTick();
}

function speedTick() {
  const s = speedState.samples;
  if (s.length >= 2) {
    const t0 = s[0][0], l0 = s[0][1];
    const t1 = s[s.length - 1][0], l1 = s[s.length - 1][1];
    const dt = (t1 - t0) / 1000;
    if (dt > 0.5 && l1 > l0) dispatch({ type: "speed", value: (l1 - l0) / dt / 3, accurate: false });
  }
  if (!getState().running) stopSpeedTicker();
}

export function stopSpeedTicker() {
  if (speedState.timer) { clearInterval(speedState.timer); speedState.timer = null; }
}
