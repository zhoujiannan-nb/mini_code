import { ROUTES } from "./api.js";
import { dispatch, getState, setState, toast } from "./store.js";
import {
  resyncRunning, resyncSessionWorker, loadSessions,
  scheduleListRefresh, updateTokenUsage, rememberCacheHit,
} from "./actions.js";

let esRef = null;      
let sseAliveAt = 0;    
let sseWatchdog = null;
let esFactory = null;

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
  es.onopen = () => { resyncRunning(); };
  sseAliveAt = Date.now();
  es.onmessage = (e) => {
    sseAliveAt = Date.now();
    let m = null;
    try { m = JSON.parse(e.data); } catch (err) { return; }
    onEvent(m);
  };
  es.addEventListener("ping", () => { sseAliveAt = Date.now(); });
  es.onerror = () => {  };
  if (!sseWatchdog) {
    sseWatchdog = setInterval(() => {
      if (Date.now() - sseAliveAt > 75000) {
        toast("事件流无响应,已自动重连", "");
        connectEvents();
      }
    }, 10000);
    if (sseWatchdog && typeof sseWatchdog.unref === "function") sseWatchdog.unref();
  }
}

export function disconnectEvents() {
  if (esRef) { try { esRef.close(); } catch (e) {} esRef = null; }
  if (sseWatchdog) { clearInterval(sseWatchdog); sseWatchdog = null; }
}

export function onEvent(m) {
  const state = getState();
  dispatch({ type: "sse", msg: m });

  const mine = !m.session_id || m.session_id === state.current;

  switch (m.kind) {
    case "approval_request":
    case "approval_resolved":
      return; 
    case "status":
      if (m.status === "session_created") {
        scheduleListRefresh(1000);
        return;
      }
      if (m.status === "task_started") {
        if (mine) clearSending();
        loadSessions();
        return;
      }
      if (m.status === "task_done" || m.status === "interrupted") {
        if (mine) { stopSpeedTicker(); updateTokenUsage(); }
        loadSessions();
        return;
      }
      if (mine) updateTokenUsage();
      return;
    case "assistant":
    case "error":
      if (mine) { stopSpeedTicker(); updateTokenUsage(); } 
      loadSessions();
      return;
    case "usage":
      rememberCacheHit(m.session_id || state.current, m.prompt_tokens, m.cached_tokens, m.cache_reported);
      if (mine) stopSpeedTicker();
      return;
    case "content_delta":
      if (mine) speedDelta(m.content); 
      return;
    case "tool_end":
      if (mine && m.tool_name === "workers") resyncSessionWorker(state.current);
      return;
    default:
      break;
  }

  if (m.session_id && m.session_id !== state.current) scheduleListRefresh();
}

function clearSending() {
  setState({ sending: false });
}

const SPEED_WINDOW_MS = 4000;
const speedState = { samples: [], timer: null, sid: null };

function speedDelta(text) {
  const cur = getState().current;
  if (speedState.sid !== cur) {
    speedState.sid = cur;
    speedState.samples.length = 0;
  }
  const now = Date.now();
  const len = (text || "").length;
  const s = speedState.samples;
  if (s.length && len < s[s.length - 1][1]) s.length = 0;
  s.push([now, len]);
  while (s.length > 2 && now - s[0][0] > SPEED_WINDOW_MS) s.shift();
  if (!speedState.timer) speedState.timer = setInterval(speedTick, 1000);
  if (speedState.timer && typeof speedState.timer.unref === "function") speedState.timer.unref();
  speedTick();
}

function speedTick() {
  const s = speedState.samples;
  if (!getState().running) { stopSpeedTicker(); return; }
  if (s.length >= 2) {
    const t0 = s[0][0], l0 = s[0][1];
    const t1 = s[s.length - 1][0], l1 = s[s.length - 1][1];
    const dt = (t1 - t0) / 1000;
    if (dt > 0.5 && l1 > l0) dispatch({ type: "speed", value: (l1 - l0) / dt / 3, accurate: false });
  }
}

export function stopSpeedTicker() {
  if (speedState.timer) { clearInterval(speedState.timer); speedState.timer = null; }
  speedState.samples.length = 0;
  speedState.sid = null;
}
