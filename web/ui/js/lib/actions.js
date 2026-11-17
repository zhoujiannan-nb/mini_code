import { api, postJSON, ROUTES } from "./api.js";
import { getState, setState, update, dispatch, toast } from "./store.js";
import { historyRows } from "./transcript.js";
import { nav } from "./nav.js";
import { mcEmit } from "./plugin-runtime.js";

export const LS_SESSION_KEY = "mini_code.current_session";
export const LS_WORKER_KEY = "mini_code.worker";
export const WK_OTHER = "w-other";

export function readSavedSession() {
  try { return localStorage.getItem(LS_SESSION_KEY); } catch (e) { return null; }
}
export function saveSession(id) {
  try { id ? localStorage.setItem(LS_SESSION_KEY, id) : localStorage.removeItem(LS_SESSION_KEY); } catch (e) {}
}

export function workerBuckets() {
  const state = getState();
  return (state.workers || []).filter((x) => !x.builtin).slice();
}

export function workerBucketId() {
  const id = getState().worker == null ? "" : getState().worker;
  return (id === "" || id === "*") ? WK_OTHER : id;
}

export function workerLabel(id) {
  if (id === "" || id == null || id === "*") id = WK_OTHER;
  const w = (getState().workers || []).find((x) => x.worker_id === id);
  return w ? w.name : "未知 worker";
}

export function workerEmptyHint() {
  if (workerBucketId() === WK_OTHER) return "这里还没有会话\n顶部「＋ 新建会话」建的会话默认落在这里";
  return "这个 worker 还没有会话\n先切到它再点顶部「＋ 新建会话」,新会话就落在这里";
}

export function restoreWorker() {
  try {
    const v = localStorage.getItem(LS_WORKER_KEY);
    if (v != null) setState({ worker: (v === "" || v === "*") ? WK_OTHER : v });
  } catch (e) {  }
}
export function rememberWorker(id) {
  try { localStorage.setItem(LS_WORKER_KEY, id); } catch (e) {}
}

export async function loadWorkers() {
  let got = true;
  try {
    const r = await api(ROUTES.workers);
    setState({
      workers: r.workers || [],
      unassigned: r.unassigned || 0,
      unassignedActive: r.unassigned_active || 0,
    });
  } catch (e) {
    got = false;
    const st = getState();
    setState({ workers: st.workers || [], unassigned: st.unassigned || 0, unassignedActive: 0 });
  }
  const id = workerBucketId();
  if (got && id !== WK_OTHER && !getState().workers.some((w) => w.worker_id === id)) {
    setState({ worker: WK_OTHER });
    rememberWorker(WK_OTHER);
  }
}

export function initWorkerBar() {
  restoreWorker();
}

export function beginSessionsRefresh() {
  setState({ sessions: [], sessionsLoading: true, sessionsError: "" });
}

export async function loadSessions(select, fresh) {
  const bucket = workerBucketId();
  if (fresh) beginSessionsRefresh();
  let sessions;
  try {
    sessions = await api(ROUTES.sessions(bucket));
  } catch (e) {
    if (fresh) setState({ sessionsLoading: false, sessionsError: (e && e.message) || String(e) });
    return;
  }
  setState({ sessions, sessionsLoading: false, sessionsError: "" });
  syncRunning();
  if (getState().current) loadChildren();
  if (select && select !== getState().current) selectSession(select);
}

let listRefreshTimer = null;
export function scheduleListRefresh(delay) {
  if (listRefreshTimer) return;
  listRefreshTimer = setTimeout(() => {
    listRefreshTimer = null;
    loadSessions();
    loadWorkers(); 
  }, delay == null ? 1500 : delay);
}

export function syncRunning() {
  const state = getState();
  if (state.sending && state.running) return;
  if (!state.current) {
    if (!state.sending && !state.pendingTask) setState({ childrenActive: false, running: false });
    return;
  }
  const s = state.sessions.find((x) => x.session_id === state.current);
  if (!s) return;
  setState({ childrenActive: !!s.children_active, running: !!s.active });
  if (!s.active && !state.sending && !state.live && state.pending) setState({ pending: null });
}

export function resetToNew() {
  saveSession(null);
  dispatch({ type: "reset" });
}

export async function selectSession(id) {
  mcEmit("session");
  setState({ current: id, live: null, pending: null, jumpTick: getState().jumpTick + 1 });
  try {
    const data = await api(ROUTES.session(id));
    if (getState().current !== id) return; 
    const s = data.session;
    let rows = historyRows(data.messages);
    const st = getState();
    if (s.active) {
      rows = rows.concat([{ key: "status-active", kind: "status", text: "⏳ 任务正在执行中 — 刷新前的已完成输出已回显,后续输出将实时显示" }]);
    }
    setState({
      workDir: s.work_dir || "",
      currentRole: s.agent_role || "",
      superOwned: false,   
      superTab: "sched",
      superDrawer: null,   
      rows,
      run: { steps: [] },
      live: null,
      pending: null,
      mode: s.mode === "cautious" ? "cautious" : "full",
      model: s.model || st.defaultModel || "",
      thinking: s.thinking || "",
      reasoningEffort: s.reasoning_effort || "",
      ctxWindow: data.context_window || st.ctxWindow,
      childrenActive: !!s.children_active,
      running: !!s.active,
      tokens: data.tokens || 0,
      usageTokens: data.usage_tokens || 0,
      tasksOpen: false,
    });
    followSessionWorker(s.worker_id || "");
    loadChildren();
    const lu = data.last_usage;
    if (lu && (lu.prompt > 0 || lu.completion > 0)) {
      setState({ cacheHit: { prompt: lu.prompt, cached: lu.cached, reported: !!lu.reported }, speed: lu.speed || 0, speedAccurate: true });
    } else {
      restoreCacheHit(id);
      setState({ speed: 0, speedAccurate: false });
    }
    if (s.active) resyncRunning();
    saveSession(id);
  } catch (e) {
    toast("打开会话失败:" + e.message, "err");
  }
}

export async function openSuperSession() {
  try {
    const data = await postJSON(ROUTES.superEnsure, {});
    if (!data || !data.session_id) {
      toast("Super-Agent 暂不可用:" + ((data && data.error) || "未知错误"), "err");
      return;
    }
    await selectSession(data.session_id);
  } catch (e) {
    toast("打开 Super-Agent 失败:" + e.message, "err");
  }
}

export function adoptNewSession(sid) {
  setState((st) => ({
    ...st,
    current: sid,
    pendingTask: null,
    rows: st.rows,
    tokens: 0,
    usageTokens: 0,
    cacheHit: null,
    speed: 0,
    speedAccurate: false,
  }));
  saveSession(sid);
  loadChildren();
}

export async function resyncRunning() {
  const state = getState();
  if (!state.current || !state.running) return;
  const sid = state.current;
  let data;
  try { data = await api(ROUTES.sessionEvents(sid, true)); } catch (e) { return; }
  if (getState().current !== sid) return; 
  dispatch({ type: "resync", events: data.events || [], active: !!data.active });
}

export async function followSessionWorker(workerID) {
  const id = workerID || WK_OTHER; 
  if (id === workerBucketId() && (getState().workers || []).some((w) => w.worker_id === id)) return false;
  await loadWorkers();
  if (!(getState().workers || []).some((w) => w.worker_id === id)) return false; 
  if (id === workerBucketId()) return false;
  setState({ worker: id, workerMenuOpen: false });
  rememberWorker(id);
  await loadSessions(null, true);
  return true;
}

export async function resyncSessionWorker(sid) {
  if (!sid) return;
  const state = getState();
  try {
    const data = await api(ROUTES.session(sid));
    if (state.current !== sid) return;
    await followSessionWorker((data && data.session && data.session.worker_id) || "");
  } catch (e) {  }
}

export async function switchWorker(id) {
  const changed = id !== workerBucketId();
  setState({ worker: id, workerMenuOpen: false });
  rememberWorker(id);
  await loadSessions(null, true);
  if (!changed) return;
  const state = getState();
  const first = (state.sessions || [])[0];
  if (first) selectSession(first.session_id);
  else resetToNew();
}

export function detachWorkerForNewSession() {
  const id = workerBucketId();
  setState({ worker: id });
  rememberWorker(id);
}

export async function createWorker() {
  const name = (prompt("新建 Worker(工作大类):名称\n会在工作区里创建 worker-<名字> 目录(共享公约存在 worker 记录里)", "") || "").trim();
  if (!name) return;
  const summary = (prompt("这个 worker 在做什么?(新会话判断归属时看这段)", "") || "").trim();
  try {
    const r = await api(ROUTES.workers, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, summary }),
    });
    toast("已创建 worker:" + r.name, "ok");
    await loadWorkers();
    await switchWorker(r.worker_id);
  } catch (err) { toast("创建失败:" + err.message, "err"); }
}

export async function editSummary(workerID) {
  const w = (getState().workers || []).find((x) => x.worker_id === workerID);
  const cur = w ? w.summary || "" : "";
  const ans = prompt("Worker 摘要(会注入到判断归属的会话里,保持一两句)", cur);
  if (ans == null) return; 
  const s = ans.trim();
  if (s === cur) return;
  try {
    await api(ROUTES.worker(workerID) + "/summary", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ summary: s }),
    });
    toast("摘要已更新", "ok");
    await loadWorkers();
  } catch (err) { toast("更新失败:" + err.message, "err"); }
}

export async function renameWorker(workerID) {
  const w = (getState().workers || []).find((x) => x.worker_id === workerID);
  const cur = w ? w.name || "" : "";
  const ans = prompt("Worker 改名。\n只改名字:共享目录的名字不动(会话、工具结果里已经写满了那条绝对路径),会话里显示的 worker 名字会一起更新。", cur);
  if (ans == null) return; 
  const name = ans.trim();
  if (!name || name === cur) return;
  try {
    await api(ROUTES.worker(workerID) + "/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    toast("已改名为:" + name, "ok");
    await loadWorkers();
  } catch (err) { toast("改名失败:" + err.message, "err"); }
}

const WK_DELETE_CONFIRM = "我确认删除工作区";

export async function deleteWorker(workerID) {
  const state = getState();
  const w = (state.workers || []).find((x) => x.worker_id === workerID);
  const n = w ? w.sessions || 0 : 0;
  const typed = (prompt(
    "删除工作区「" + (w ? w.name : workerID) + "」会一并删掉里面的 " + n +
    " 个会话(共享目录里的文件保留在磁盘)。\n要删请一字不差地输入:" + WK_DELETE_CONFIRM,
    "") || "").trim();
  if (typed !== WK_DELETE_CONFIRM) {
    if (typed) toast("没删:确认语要一模一样(" + WK_DELETE_CONFIRM + ")", "err");
    return;
  }
  const shown = (state.sessions || []).find((x) => x.session_id === state.current);
  try {
    const r = await api(ROUTES.worker(workerID), { method: "DELETE" });
    toast("已删除(" + (r.deleted_sessions || 0) + " 个会话)", "ok");
    if (workerBucketId() === workerID) {
      setState({ worker: WK_OTHER });
      rememberWorker(WK_OTHER);
    }
    await loadWorkers();
    await loadSessions(null, true);
    if (shown && shown.worker_id === workerID) resetToNew();
  } catch (err) {
    toast("删除失败:" + err.message, "err");
    await loadSessions(null, true);
  }
}

export async function openNoteEditor(workerID) {
  let data;
  try { data = await api(ROUTES.workerNote(workerID)); }
  catch (e) { toast("读取公约失败:" + e.message, "err"); return; }
  setState({
    workerNote: {
      id: workerID,
      name: workerLabel(workerID),
      ver: data.ver || 0,
      content: data.content || "",
    },
  });
}

export function closeNoteEditor() {
  setState({ workerNote: null });
}

export async function saveNote(content) {
  const note = getState().workerNote;
  if (!note) return;
  try {
    const r = await api(ROUTES.workerNote(note.id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content }),
    });
    toast("公约已保存到 v" + (r.ver || 1), "ok");
    setState({ workerNote: null });
    await loadWorkers();
  } catch (err) { toast("保存失败:" + err.message, "err"); }
}

let sendingTimer = null;

export async function send(text, images) {
  text = String(text == null ? "" : text).trim();
  images = (images || []).slice();
  const state = getState();
  if ((!text && images.length === 0) || state.running) return false;
  const st = state;
  dispatch({ type: "userMessage", text, images });
  mcEmit("send");
  setState({ sending: true });
  clearTimeout(sendingTimer);
  sendingTimer = setTimeout(() => setState({ sending: false }), 8000);
  const body = { content: text };
  if (images.length) body.images = images;
  if (st.current) body.session_id = st.current;
  else {
    body.mode = st.mode;   
    body.model = st.model; 
    body.thinking = st.thinking || "";               
    body.reasoning_effort = st.reasoningEffort || ""; 
    if (state.worker) body.worker_id = state.worker;
  }
  try {
    const r = await api(ROUTES.messages, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!getState().current) {
      const hit = getState().recentCreated.find((e) => e.task_id === r.task_id);
      if (hit) adoptNewSession(hit.session_id);
      else setState({ pendingTask: r.task_id });
    }
    return true;
  } catch (e) {
    update((s) => ({ ...s, rows: s.rows.concat([{ key: "err-send-" + Date.now(), kind: "error", text: "发送失败:" + e.message }]) }));
    setState({ sending: false, running: false, pending: null });
    return false;
  }
}

export async function sendSteer(text) {
  text = String(text == null ? "" : text).trim();
  const st = getState();
  if (!text || !st.current) return false;
  dispatch({ type: "userMessage", text, images: [] });
  mcEmit("send");
  try {
    const r = await api(ROUTES.messages, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content: text, session_id: st.current }),
    });
    if (r && r.mode === "steer") toast("已插话,将在下一步生效", "ok");
    else toast("任务刚好已结束,已按新消息发送", "");
    return true;
  } catch (e) {
    update((s) => ({ ...s, rows: s.rows.concat([{ key: "err-steer-" + Date.now(), kind: "error", text: "插话失败:" + e.message }] ) }));
    return false;
  }
}

export async function stop() {
  const state = getState();
  if (!state.current) { toast("会话尚未创建,无法停止", "err"); return; }
  try {
    const r = await api(ROUTES.cancel(state.current), { method: "POST" });
    if (r.canceled) {
      toast("已发送停止请求", "ok");
    } else if (r.children_canceled > 0) {
      toast("已停止该会话的 " + r.children_canceled + " 个后台子任务", "ok");
      setState({ childrenActive: false });
      loadChildren();
    } else {
      toast("该会话当前没有正在执行的任务", "");
      setState({ running: false, childrenActive: false });
    }
  } catch (e) { toast("停止失败:" + e.message, "err"); }
}

export async function renameSession(id, currentTitle) {
  const t = prompt("重命名会话", currentTitle || "");
  if (t == null) return; 
  const title = t.trim();
  if (!title) { toast("标题不能为空", "err"); return; }
  if ([...title].length > 60) { toast("标题过长(最多 60 字)", "err"); return; }
  try {
    const r = await api(ROUTES.rename(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    update((s) => ({ ...s, sessions: s.sessions.map((x) => x.session_id === id ? { ...x, title: r.title } : x) }));
    toast("已重命名", "ok");
  } catch (err) { toast("重命名失败:" + err.message, "err"); }
}

export async function deleteSession(id, title) {
  if (!confirm("确定删除该会话?(消息将一并删除)")) return;
  try {
    await api(ROUTES.session(id), { method: "DELETE" });
    if (getState().current === id) resetToNew();
    await loadSessions();
    toast("会话已删除", "ok");
  } catch (err) { toast("删除失败:" + err.message, "err"); }
}

export async function setModeTo(next) {
  const state = getState();
  if (state.mode === next) return;
  if (!state.current) {
    setState({ mode: next });
    toast(next === "cautious" ? "新会话将用谨慎模式:危险操作请求审批" : "新会话将用完整模式:直接执行", "ok");
    return;
  }
  const prev = state.mode;
  setState({ mode: next });
  try {
    const r = await api(ROUTES.mode(state.current), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: next }),
    });
    setState({ mode: r.mode });
    toast(next === "cautious" ? "已切换为谨慎模式:危险操作将请求审批" : "已切换为完整模式:直接执行", "ok");
  } catch (e) {
    setState({ mode: prev });
    toast("切换模式失败:" + e.message, "err");
  }
}

export async function toggleMode() {
  const state = getState();
  return setModeTo(state.mode === "cautious" ? "full" : "cautious");
}

export async function pickModel(name) {
  const state = getState();
  if (name === state.model) return;
  const prev = state.model;
  setState({ model: name });
  syncReasoningToModel();
  if (!state.current) {
    const m = state.models.find((x) => x.name === name);
    if (m) setState({ ctxWindow: m.context_window || 0 });
    toast("新会话将使用模型 " + name, "");
    return;
  }
  try {
    const r = await api(ROUTES.model(state.current), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: name }),
    });
    if (r.context_window) setState({ ctxWindow: r.context_window });
    updateTokenUsage();
    toast("已切换模型 " + name + ",下次任务生效", "ok");
  } catch (e) {
    setState({ model: prev });
    toast("切换模型失败:" + e.message, "err");
  }
}

export function syncReasoningToModel() {
  const scale = thinkScale();
  const state = getState();
  let changed = false;
  if (!scale) {
    if (state.thinking || state.reasoningEffort) { setState({ thinking: "", reasoningEffort: "" }); changed = true; }
  } else if (!scale.some((s) => s.thinking === (state.thinking || "") && s.effort === (state.reasoningEffort || ""))) {
    setState({ thinking: "", reasoningEffort: "" });
    changed = true;
  }
  if (changed && state.current) persistReasoning();
}

export async function persistReasoning() {
  const state = getState();
  if (!state.current) return;
  try {
    await api(ROUTES.reasoning(state.current), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ thinking: state.thinking || "", reasoning_effort: state.reasoningEffort || "" }),
    });
    toast("思考强度已更新(下次任务生效)", "ok");
  } catch (e) {
    toast("更新思考强度失败:" + e.message, "err");
  }
}

export async function loadContext() {
  const state = getState();
  if (!state.current) { setState({ ctxInfo: null }); return null; }
  try {
    const r = await api(ROUTES.context(state.current));
    setState({ ctxInfo: r });
    return r;
  } catch (e) {
    setState({ ctxInfo: null });
    return null;
  }
}

export async function loadChildren() {
  const state = getState();
  if (!state.current) { setState({ children: [] }); return; }
  try {
    const children = await api(ROUTES.children(state.current));
    setState({ children, childrenActive: (children || []).some((c) => c.active) });
  } catch (e) { setState({ children: [] }); }
}

export async function loadJobs() {
  const state = getState();
  if (!state.current) { setState({ jobs: [] }); return; }
  try {
    const r = await api(ROUTES.jobs(state.current));
    setState({ jobs: r.jobs || [] });
  } catch (e) { setState({ jobs: [] }); }
}

export async function killJob(id) {
  try {
    const r = await api(ROUTES.jobKill(getState().current), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ job_id: id, reason: "从界面停止" }),
    });
    toast(r.killed ? "已请求停止,正在杀进程树" : "该命令已经不在运行了", r.killed ? "ok" : "");
  } catch (e) { toast("停止失败:" + e.message, "err"); }
  loadJobs();
}

export async function clearJobs() {
  try {
    await api(ROUTES.jobClear(getState().current), { method: "POST" });
  } catch (e) { toast("清除失败:" + e.message, "err"); return; }
  await loadJobs();
  if (!(getState().jobs || []).length) setState({ jobsOpen: false });
  loadSessions();
}

export async function decideApproval(id, decision) {
  try {
    await api(ROUTES.approval(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ decision }),
    });
  } catch (e) {
    toast("审批提交失败:" + e.message, "err");
    syncApprovals(); 
  }
}

export async function syncApprovals() {
  try {
    const list = await api(ROUTES.approvals);
    update((s) => {
      const serverIds = new Set((list || []).map((p) => p.id));
      const approvals = {};
      for (const id of Object.keys(s.approvals)) if (serverIds.has(id)) approvals[id] = s.approvals[id];
      for (const p of list || []) if (p && p.id) approvals[p.id] = p;
      let shown = s.approvalShown;
      if (!shown || !approvals[shown]) shown = Object.keys(approvals)[0] || null;
      return { ...s, approvals, approvalShown: shown };
    });
  } catch (e) {}
}

export async function loadStatus() {  try {
    const s = await api(ROUTES.status);
    const st = getState();
    const models = s.models || [];
    const defaultModel = s.default_model || (models[0] && models[0].name) || "";
    setState({
      port: s.web_port || 0,
      models,
      defaultModel,
      defaultCtxWindow: s.context_window || 0,
      model: (st.model && !models.some((x) => x.name === st.model)) ? defaultModel : st.model,
      ctxWindow: st.current ? st.ctxWindow : (s.context_window || 0),
      tokens: st.current ? st.tokens : 0,
    });
  } catch (e) {  }
}

export async function updateTokenUsage() {
  const state = getState();
  if (!state.current) { setState({ tokens: 0 }); return; }
  try {
    const data = await api(ROUTES.session(state.current));
    if (getState().current !== data.session.session_id) return; 
    setState({ tokens: data.tokens || 0, usageTokens: data.usage_tokens || 0 });
  } catch (e) {  }
}

const LS_CACHEHIT_KEY = "mini_code.cache_hit";
function cacheHitStore() {
  try { return JSON.parse(localStorage.getItem(LS_CACHEHIT_KEY) || "{}"); } catch (e) { return {}; }
}
export function rememberCacheHit(sid, prompt, cached, reported) {
  if (!sid || !prompt || prompt <= 0) return;
  const m = cacheHitStore();
  m[sid] = { p: prompt, c: cached || 0, r: !!reported };
  const keys = Object.keys(m);
  if (keys.length > 50) for (const k of keys.slice(0, keys.length - 50)) delete m[k]; 
  try { localStorage.setItem(LS_CACHEHIT_KEY, JSON.stringify(m)); } catch (e) {  }
}
function restoreCacheHit(sid) {
  const e = sid && cacheHitStore()[sid];
  setState({ cacheHit: e && e.p > 0 ? { prompt: e.p, cached: e.c, reported: e.r } : null });
}

export async function loadUserAvatar() {
  try {
    const r = await api(ROUTES.avatar);
    setState({ userAvatar: r.avatar || "" });
  } catch (e) {  }
}
export async function loadAgentAvatar() {
  try {
    const r = await api(ROUTES.agentAvatar);
    setState({ agentAvatar: r.avatar || "" });
  } catch (e) {  }
}
export async function saveUserAvatar(dataURL) {
  await api(ROUTES.avatar, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ avatar: dataURL }),
  });
  setState({ userAvatar: dataURL });
}
export async function saveAgentAvatar(dataURL) {
  await api(ROUTES.agentAvatar, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ avatar: dataURL }),
  });
  setState({ agentAvatar: dataURL });
}

nav.selectSession = selectSession;

export const THINKING_PROVIDERS = new Set(["deepseek", "sglang", "vllm"]);
export const THINK_SCALE = {
  deepseek: [
    { label: "默认", thinking: "", effort: "" },
    { label: "off", thinking: "", effort: "off" },
    { label: "低", thinking: "", effort: "low" },
    { label: "中", thinking: "", effort: "high" },
    { label: "高", thinking: "", effort: "max" },
  ],
  sglang: [
    { label: "默认", thinking: "", effort: "" },
    { label: "off", thinking: "disabled", effort: "" },
    { label: "低", thinking: "", effort: "low" },
    { label: "中", thinking: "", effort: "medium" },
    { label: "高", thinking: "", effort: "xhigh" },
  ],
  vllm: [
    { label: "默认", thinking: "", effort: "" },
    { label: "off", thinking: "disabled", effort: "" },
    { label: "低", thinking: "", effort: "low" },
    { label: "中", thinking: "", effort: "medium" },
    { label: "高", thinking: "", effort: "xhigh" },
  ],
};

export function currentProvider() {
  const m = getState().models.find((x) => x.name === getState().model);
  return m ? m.provider : "";
}
export function thinkingSupported() { return THINKING_PROVIDERS.has(currentProvider()); }
export function thinkScale() { return THINK_SCALE[currentProvider()] || null; }
export function thinkIndex() {
  const scale = thinkScale();
  if (!scale) return 0;
  const st = getState();
  const i = scale.findIndex((s) => s.thinking === (st.thinking || "") && s.effort === (st.reasoningEffort || ""));
  return i < 0 ? 0 : i;
}
export function reasoningLabel() {
  const scale = thinkScale();
  if (!scale) return "";
  return "思考:" + scale[thinkIndex()].label;
}
