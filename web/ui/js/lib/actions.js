// 所有"会打后端"的动作。组件只调用这里的函数,不直接 fetch ——
// 路径与协议集中在 lib/api.js,状态写回集中在 lib/store.js。
import { api, postJSON, ROUTES } from "./api.js";
import { getState, setState, update, dispatch, toast } from "./store.js";
import { historyRows } from "./transcript.js";
import { nav } from "./nav.js";
import { mcEmit } from "./plugin-runtime.js";

/* ---------------- 本地记住的偏好 ---------------- */
export const LS_SESSION_KEY = "mini_code.current_session";
export const LS_WORKER_KEY = "mini_code.worker";
export const WK_OTHER = "w-other";

export function readSavedSession() {
  try { return localStorage.getItem(LS_SESSION_KEY); } catch (e) { return null; }
}
export function saveSession(id) {
  try { id ? localStorage.setItem(LS_SESSION_KEY, id) : localStorage.removeItem(LS_SESSION_KEY); } catch (e) {}
}

/* ---------------- worker(工作大类) ---------------- */
// workerBuckets 是切换条的全部序列:各 worker(含固定的「其他」,它是服务端常驻的
// 一条)。没有"未分配"也没有"全部"这一档 —— 会话创建即有归属,兜底档就是「其他」。
// 内置分桶(旧版"已归档")不列出。
export function workerBuckets() {
  const state = getState();
  return (state.workers || []).filter((x) => !x.builtin).slice();
}

export function workerBucketId() {
  const id = getState().worker == null ? "" : getState().worker;
  // 老版本存过 ""(未分配)和 "*",统一映射到「其他」。
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
  } catch (e) { /* 隐私模式 / 禁用存储:留在初始桶 */ }
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
    // 服务未启用 worker(老服务端 / 未接线):切换条退化成只读提示
    got = false;
    const st = getState();
    setState({ workers: st.workers || [], unassigned: st.unassigned || 0, unassignedActive: 0 });
  }
  // 选中的 worker 被删了:回到固定的「其他」,别把列表卡成空。只在真的拿到列表
  // 时才校准,否则一次网络抖动就会把用户正在看的 worker 踢走。
  const id = workerBucketId();
  if (got && id !== WK_OTHER && !getState().workers.some((w) => w.worker_id === id)) {
    setState({ worker: WK_OTHER });
    rememberWorker(WK_OTHER);
  }
}

export function initWorkerBar() {
  restoreWorker();
}

/* ---------------- 会话列表 ---------------- */
// beginSessionsRefresh empties the list and paints the skeleton right away, so a
// slow round trip never leaves another worker's rows on screen.
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
    // 后台轮询失败不吓人(留着旧行,下一轮自己收敛);用户主动切换/删除时清空了,
    // 那就必须告诉他为什么是空的。
    if (fresh) setState({ sessionsLoading: false, sessionsError: (e && e.message) || String(e) });
    return;
  }
  setState({ sessions, sessionsLoading: false, sessionsError: "" });
  syncRunning();
  if (getState().current) loadChildren();
  if (select && select !== getState().current) selectSession(select);
}

// SSE 事件是"有会话发生了变化"的信号:其他会话的事件不进入当前视图,
// 但触发一次防抖的列表刷新,让侧边栏状态在秒级收敛(而不是等 30s 轮询)。
let listRefreshTimer = null;
export function scheduleListRefresh(delay) {
  if (listRefreshTimer) return;
  listRefreshTimer = setTimeout(() => {
    listRefreshTimer = null;
    loadSessions();
    loadWorkers(); // 会话数徽标跟着收敛
  }, delay == null ? 1500 : delay);
}

// 用服务端状态校准当前会话的执行状态:
// - 页面刷新 / SSE 漏掉终结事件后,停止按钮与运行指示都能自动恢复或收敛;
// - 会话刚发出消息、session_created 还没回来时(sending),不做降级。
export function syncRunning() {
  const state = getState();
  // 刚发送、task_started 还没回来:保持 running,防止在途的旧会话列表
  // (active 尚未置位)把 running 误降级。
  if (state.sending && state.running) return;
  if (!state.current) {
    // 消息刚发出、会话还没创建完:保持 running,等 session_created 接管
    if (!state.sending && !state.pendingTask) setState({ childrenActive: false, running: false });
    return;
  }
  const s = state.sessions.find((x) => x.session_id === state.current);
  if (!s) return;
  // active = 本会话自己有任务在跑 → 上锁输入框;children_active = 后台子 agent
  // 还在跑 → 只画停止按钮,输入框照常能用。
  setState({ childrenActive: !!s.children_active, running: !!s.active });
  // 兜底:SSE 漏掉终结事件时,服务端已收敛为空闲、本地也没有流式块 ——
  // 把残留的"等待模型响应"转圈清掉(最多等一次 30s 轮询)
  if (!s.active && !state.sending && !state.live && state.pending) setState({ pending: null });
}

/* ---------------- 打开 / 新建会话 ---------------- */
export function resetToNew() {
  saveSession(null);
  dispatch({ type: "reset" });
}

export async function selectSession(id) {
  mcEmit("session");
  // jumpTick 与 current 同批自增:点谁(包括正在看的会话)都要跳回最新消息。
  setState({ current: id, live: null, pending: null, jumpTick: getState().jumpTick + 1 });
  try {
    const data = await api(ROUTES.session(id));
    if (getState().current !== id) return; // 等待期间又切走了,丢弃过期响应
    const s = data.session;
    let rows = historyRows(data.messages);
    const st = getState();
    if (s.active) {
      // 刷新不中断服务端任务:历史里已持久化的输出先回显,流式中的输出为累积下发。
      rows = rows.concat([{ key: "status-active", kind: "status", text: "⏳ 任务正在执行中 — 刷新前的已完成输出已回显,后续输出将实时显示" }]);
    }
    setState({
      // 工作目录:聊天里的相对路径要按它解析(不是服务进程的 cwd)
      workDir: s.work_dir || "",
      // 会话角色:"super" 时主区换成 Router 六页签面板。
      currentRole: s.agent_role || "",
      superOwned: false,   // 若目标确属 super,由点入方(SuperPanel)随后点亮
      superTab: "chat",
      superDrawer: null,   // 离开面板回到聊天视图时收起 worker 会话抽屉
      rows,
      run: { steps: [] },
      live: null,
      pending: null,
      mode: s.mode === "cautious" ? "cautious" : "full",
      // 模型:以服务端为准(空 = 默认模型);token 条窗口跟着会话模型走
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
    // 侧栏跟着会话走:从 mc-session 芯片跳进来的会话可能属于另一个 worker。
    // 不 await:侧栏慢半拍不该拖住聊天区的重绘。
    followSessionWorker(s.worker_id || "");
    loadChildren();
    // 缓存命中 + 输出速度:优先用数据库里持久化的最后一次 LLM 调用实测值;
    // 旧版本会话(无 last_usage)回退到 localStorage 里的最近值。
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

// 打开 Super-Agent 会话:后端惰性建会话(每个进程只一条),拿到 id 直接进。
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

// 新会话发送后的"接管":事件与 HTTP 响应谁先到都走这里。
export function adoptNewSession(sid) {
  // 这里改的是「接管新会话」那七个字段,别的都要原样留着:boot / models / plugins
  // 一旦被丢掉,设置弹窗和输入区下一帧渲染就会读 undefined.boot / undefined.models。
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

// resyncRunning:断线/刷新后的对账。读当前会话本轮的事件账本(tail_run),
// 用 applyResync 把"正在执行"的步骤回填。SSE 是发后即忘,一次性事件在断流窗口
// 里丢了就永远不回;账本是 append-only 的事实流水,这才是真相。
export async function resyncRunning() {
  const state = getState();
  if (!state.current || !state.running) return;
  const sid = state.current;
  let data;
  try { data = await api(ROUTES.sessionEvents(sid, true)); } catch (e) { return; }
  if (getState().current !== sid) return; // 等待期间已切走
  dispatch({ type: "resync", events: data.events || [], active: !!data.active });
}

/* ---------------- 侧栏跟着会话走 ---------------- */
// followSessionWorker:会话归属哪个 worker,切换条就切到哪个区。
// 和 switchWorker 的区别是它**不**去挑「新区的第一条会话」——要看的还是这一条。
export async function followSessionWorker(workerID) {
  const id = workerID || WK_OTHER; // 老数据可能还带着空归属,一律当「其他」
  // 已经在这个区就别折腾了:打开会话是高频动作,不该为它多打一次 /api/workers。
  if (id === workerBucketId() && (getState().workers || []).some((w) => w.worker_id === id)) return false;
  // 先拉一次 worker 列表:agent 可能在会话里刚 create 出一条新线,列表里没有它。
  await loadWorkers();
  if (!(getState().workers || []).some((w) => w.worker_id === id)) return false; // 已被删掉:别切
  if (id === workerBucketId()) return false;
  setState({ worker: id, workerMenuOpen: false });
  rememberWorker(id);
  await loadSessions(null, true);
  return true;
}

// resyncSessionWorker:agent 在会话里自己改了归属(workers 工具的 join/create/leave)。
// 服务端不为这个单独推事件,所以只能在工具跑完后把这条会话重新读一遍对齐。
export async function resyncSessionWorker(sid) {
  if (!sid) return;
  const state = getState();
  try {
    const data = await api(ROUTES.session(sid));
    // 读回来的这段时间用户可能已经切走了:别拿旧会话的归属去改现在的侧栏。
    if (state.current !== sid) return;
    await followSessionWorker((data && data.session && data.session.worker_id) || "");
  } catch (e) { /* 对齐失败就算了:下一次列表轮询照常收敛 */ }
}

export async function switchWorker(id) {
  const changed = id !== workerBucketId();
  setState({ worker: id, workerMenuOpen: false });
  rememberWorker(id);
  await loadSessions(null, true);
  if (!changed) return;
  // 换了区,右边展示的会话也跟着换:选中新列表的第一条(服务端按 updated_at 倒序)。
  // 空区回到"新会话"草稿 —— 否则还挂着上一个区的会话,看着像没切过去。
  const state = getState();
  const first = (state.sessions || [])[0];
  if (first) selectSession(first.session_id);
  else resetToNew();
}

// 顶部大「＋ 新建会话」:会话落在当前选中的区(创建即归属)。
export function detachWorkerForNewSession() {
  const id = workerBucketId();
  setState({ worker: id });
  rememberWorker(id);
}

/* ---------------- worker 的增删改 ---------------- */
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
  if (ans == null) return; // 取消:留原样(别把摘要当成空串写回去)
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
  if (ans == null) return; // 取消:什么都不写
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

// WK_DELETE_CONFIRM is what the user has to type, verbatim, before a worker goes
// away. Deleting one takes every session in it with it, so a stray click on 🗑
// must not be enough.
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
  // 展示区里那条可能正是这个区名下的 —— 删完得回草稿,别对着一条不存在的会话。
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
    // 没删成也得把列表拉回真实状态,别停在骨架或空列表上。
    await loadSessions(null, true);
  }
}

/* ---------------- 公约(存在 worker 记录里,带版本) ---------------- */
// 公约只有这一条写路径:人在这里改 = 授权本身。保存即升版本。
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

/* ---------------- 发送 / 停止 ---------------- */
let sendingTimer = null;

export async function send(text, images) {
  text = String(text == null ? "" : text).trim();
  images = (images || []).slice();
  const state = getState();
  if ((!text && images.length === 0) || state.running) return false;
  const st = state;
  dispatch({ type: "userMessage", text, images });
  mcEmit("send");
  // sending 保持到该任务的 task_started/error 事件回来:期间在途的旧列表
  // (active 尚未置位)不得把 running 降级。
  setState({ sending: true });
  clearTimeout(sendingTimer);
  sendingTimer = setTimeout(() => setState({ sending: false }), 8000);
  const body = { content: text };
  if (images.length) body.images = images;
  if (st.current) body.session_id = st.current;
  else {
    body.mode = st.mode;   // 新会话:按当前选择的权限模式创建
    body.model = st.model; // 新会话:按当前选择的模型创建
    body.thinking = st.thinking || "";               // 新会话:思考开关(空 = 模型默认)
    body.reasoning_effort = st.reasoningEffort || ""; // 新会话:思考深度(空 = 模型默认)
    // 新会话:侧边栏选中了哪个区就建在哪个区。
    if (state.worker) body.worker_id = state.worker;
  }
  try {
    const r = await api(ROUTES.messages, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!getState().current) {
      // 竞态兜底:后端先 emit SSE 再回 HTTP,session_created 可能比这个响应早到。
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

// 插话(steering):任务运行中追一句话。后端检测会话有活动任务时把消息注入
// 运行中的循环(下一轮生效),响应带 mode:"steer";如果任务恰好已收尾,后端会
// 按普通新任务受理(无 mode 字段),这里照实提示。
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
      // 主会话自己的回合早就结束、只有后台子任务在跑:取消父会话照样级联到它们。
      toast("已停止该会话的 " + r.children_canceled + " 个后台子任务", "ok");
      setState({ childrenActive: false });
      loadChildren();
    } else {
      // 服务端没有活动任务(状态可能已经收尾):同步本地,别再留停止按钮
      toast("该会话当前没有正在执行的任务", "");
      setState({ running: false, childrenActive: false });
    }
  } catch (e) { toast("停止失败:" + e.message, "err"); }
}

/* ---------------- 会话级设置 ---------------- */
export async function renameSession(id, currentTitle) {
  const t = prompt("重命名会话", currentTitle || "");
  if (t == null) return; // 取消
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
    // 新会话:token 条的窗口跟着所选模型走
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

// syncReasoningToModel 在切换模型后校正思考覆盖:新 provider 不支持思考则清空。
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

// 拉取会话的上下文快照(组成 / 累计用量 / 事件账本)。失败静默清空,由面板显示空态。
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

/* ---------------- 子任务 / 后台命令 ---------------- */
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

/* ---------------- 审批 ---------------- */
export async function decideApproval(id, decision) {
  try {
    await api(ROUTES.approval(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ decision }),
    });
  } catch (e) {
    toast("审批提交失败:" + e.message, "err");
    syncApprovals(); // 可能已超时:与服务端对账
  }
}

// 页面加载 / 漏事件后与服务端对账:服务端为准
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

/* ---------------- 状态 / token / 缓存 ---------------- */
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
      // 当前选择的模型若已不存在(配置被改),回落默认
      model: (st.model && !models.some((x) => x.name === st.model)) ? defaultModel : st.model,
      ctxWindow: st.current ? st.ctxWindow : (s.context_window || 0),
      tokens: st.current ? st.tokens : 0,
    });
  } catch (e) { /* 顶栏显示"状态获取失败"由组件按 port/models 自行降级 */ }
}

export async function updateTokenUsage() {
  const state = getState();
  if (!state.current) { setState({ tokens: 0 }); return; }
  try {
    const data = await api(ROUTES.session(state.current));
    if (getState().current !== data.session.session_id) return; // 已切走
    setState({ tokens: data.tokens || 0, usageTokens: data.usage_tokens || 0 });
  } catch (e) { /* 忽略:会话可能刚被删除 */ }
}

// 缓存命中是"上一轮 LLM 调用"的瞬态指标:刷新/切会话后不会重发。
// 把每个会话最近一次 usage 记进 localStorage,切回或刷新时回显最后一次的值。
const LS_CACHEHIT_KEY = "mini_code.cache_hit";
function cacheHitStore() {
  try { return JSON.parse(localStorage.getItem(LS_CACHEHIT_KEY) || "{}"); } catch (e) { return {}; }
}
export function rememberCacheHit(sid, prompt, cached, reported) {
  if (!sid || !prompt || prompt <= 0) return;
  const m = cacheHitStore();
  m[sid] = { p: prompt, c: cached || 0, r: !!reported };
  const keys = Object.keys(m);
  if (keys.length > 50) for (const k of keys.slice(0, keys.length - 50)) delete m[k]; // 防无限增长
  try { localStorage.setItem(LS_CACHEHIT_KEY, JSON.stringify(m)); } catch (e) { /* 忽略配额/隐私模式 */ }
}
function restoreCacheHit(sid) {
  const e = sid && cacheHitStore()[sid];
  setState({ cacheHit: e && e.p > 0 ? { prompt: e.p, cached: e.c, reported: e.r } : null });
}

/* ---------------- 头像 ---------------- */
export async function loadUserAvatar() {
  try {
    const r = await api(ROUTES.avatar);
    setState({ userAvatar: r.avatar || "" });
  } catch (e) { /* 头像加载失败不阻塞:用回退 */ }
}
export async function loadAgentAvatar() {
  try {
    const r = await api(ROUTES.agentAvatar);
    setState({ agentAvatar: r.avatar || "" });
  } catch (e) { /* 用默认 */ }
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

// nav 桥:markdown 里的会话芯片与"打开路径"不需要 import 整个 actions。
nav.selectSession = selectSession;

/* ---------------- 思考强度 ---------------- */
// 支持思考强度的 provider 与刻度。放在 actions 里是因为
// Composer(画控件)、设置(存默认值)和模型切换共用同一份定义。
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
