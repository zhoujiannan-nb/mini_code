// 单一 store:state + 订阅 + dispatch。所有 SSE 事件在这里归约成 state,
// 组件只读 state —— 没有哪段界面代码会再直接改 DOM。
//
// 为什么 reducer 写成纯函数(applyEvent / applyResync):流式协议最容易出错的地方是
// "累积 delta、工具配对、断线对账",把它们从渲染里剥离出来才能用单元测试钉住
// (见 test/store.test.mjs)。DOM 只负责把 state 画出来。
import { useState, useEffect } from "preact/hooks";
import { newKey } from "./keys.js";
import { makeThinkStep, makeToolStep, findOpenStep } from "./transcript.js";
import { toolTimeoutSec } from "./tools.js";

/* ---------------- 初始 state ---------------- */
export const initialState = {
  // ---- 会话列表 / worker ----
  sessions: [],
  sessionsLoading: false,
  sessionsError: "",
  workers: [],
  worker: "",            // 当前分桶;"" 由 workerBucketId() 映射到固定的「其他」
  workerMenuOpen: false,
  workerMenuAnchor: null, // {left, bottom, width} —— 下拉按视口夹紧用
  unassigned: 0,
  unassignedActive: 0,
  // ---- 当前会话 ----
  current: null,         // null = 新会话草稿
  mode: "full",          // full | cautious
  model: "",
  models: [],
  defaultModel: "",
  defaultCtxWindow: 0,
  ctxWindow: 0,
  port: 0,               // /api/status 的 web_port(顶栏状态胶囊)
  workDir: "",
  contextReminders: true,
  thinking: "",
  reasoningEffort: "",
  running: false,        // 本会话自己有任务在跑(只此项上锁输入框)
  childrenActive: false, // 后台子 agent 在跑(只画停止按钮)
  sending: false,        // 消息已发出、task_started 还没回来
  pendingTask: null,
  recentCreated: [],
  // ---- 消息(渲染行) ----
  rows: [],
  live: null,            // 正在生成的那一条 {content, reasoning, startedAt}
  pending: null,         // 等待首 token 的转圈 {startedAt}
  run: { steps: [] },    // 当前这一轮的过程步骤(渲染为折叠条 +「正在执行」卡)
  tokens: 0,
  cacheHit: null,        // {prompt, cached, reported}
  speed: 0,
  speedAccurate: false,
  // ---- 子任务 / 后台命令 ----
  children: [],
  tasksOpen: false,
  jobs: [],
  jobsOpen: false,
  // ---- 审批 ----
  approvals: {},
  approvalShown: null,
  // ---- 全局 UI ----
  userAvatar: "",
  agentAvatar: "",
  toast: null,
  themeMode: "system",
  settingsOpen: false,
  settingsPage: "page-appearance",
  avatarOpen: false,
  composerSeed: null,    // {id, text} —— 「造一个插件」把提示词塞进输入框
  workerNote: null,      // {id, name, ver, content} —— 公约编辑弹窗
  pluginLogs: null,      // {id, lines} —— 程序插件日志弹窗
  boot: { dir: "", legacyDir: "", themes: [], play: null, picked: "" },
  plugins: {
    list: [], dir: "", slots: [], catalog: [], catalogMsg: "", repo: "",
    tab: "installed", filter: "", catalogKind: "", busy: null, sel: "", mSel: "",
  },
};

/* ---------------- 订阅 ---------------- */
// `state` 以 live binding 导出:少数纯模块(markdown 的 openFilePath 要读 workDir)
// 直接 import { state } 就能看到最新值,不必为一个字段绕一层 selector。
export let state = initialState;
const listeners = new Set();

export function getState() { return state; }

export function subscribe(fn) {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

function notify() {
  // 复制一份:监听者在回调里退订是常见操作(组件卸载),直接遍历 Set 会漏项。
  for (const fn of Array.from(listeners)) {
    try { fn(state); } catch (e) { console.error("store listener failed", e); }
  }
}

export function setState(patch) {
  state = typeof patch === "function" ? patch(state) : Object.assign({}, state, patch);
  notify();
  return state;
}

export function update(fn) {
  const next = fn(state);
  if (next) state = next;
  notify();
  return state;
}

/* ---------------- 组件订阅 hook ---------------- */
// selector 必须返回稳定引用(数组/对象只在真正变化时换新),否则每次 notify 都会重渲。
export function useStore(selector) {
  const [snap, setSnap] = useState(() => selector(state));
  useEffect(() => {
    const read = () => selector(state);
    // 订阅前先补一次:render 与 effect 之间 state 可能已经变了。
    setSnap((prev) => (Object.is(prev, read()) ? prev : read()));
    const off = subscribe(() => {
      setSnap((prev) => {
        const next = read();
        return Object.is(prev, next) ? prev : next;
      });
    });
    return off;
    // selector 只捕获 state(约定),不随渲染变化。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return snap;
}

/* ---------------- toast(全局唯一) ---------------- */
export function toast(msg, kind) {
  setState({ toast: { id: newKey("toast"), msg: String(msg == null ? "" : msg), kind: kind || "" } });
}

export function dismissToast(id) {
  if (state.toast && state.toast.id === id) setState({ toast: null });
}

/* ---------------- reducer ---------------- */
export function reduce(st, action) {
  switch (action.type) {
    case "sse":
      return applyEvent(st, action.msg);
    case "resync":
      return applyResync(st, action.events || [], !!action.active);
    case "set":
      return Object.assign({}, st, action.patch);
    case "update":
      return action.fn(st);
    case "userMessage": {
      const row = { key: newKey("user"), kind: "user", text: action.text || "", images: action.images || [] };
      return Object.assign({}, st, {
        rows: st.rows.concat([row]),
        live: null,
        pending: { startedAt: Date.now() },
        running: true,
        sending: true,
      });
    }
    case "reset": {
      return Object.assign({}, st, {
        current: null,
        workDir: "",
        pendingTask: null,
        sending: false,
        running: false,
        childrenActive: false,
        children: [],
        rows: [],
        live: null,
        pending: null,
        run: { steps: [] },
        tokens: 0,
        cacheHit: null,
        speed: 0,
        speedAccurate: false,
        mode: "full",
        model: st.defaultModel || "",
        contextReminders: true,
        thinking: "",
        reasoningEffort: "",
        ctxWindow: st.defaultCtxWindow,
        tasksOpen: false,
      });
    }
    case "foldHeld": {
      const now = action.now || Date.now();
      const steps = st.run.steps.map((s) => {
        if (!s.folded && s.holdUntil && s.holdUntil <= now) return Object.assign({}, s, { folded: true, holdUntil: 0 });
        return s;
      });
      return Object.assign({}, st, { run: { steps } });
    }
    case "speed":
      return Object.assign({}, st, { speed: action.value || 0, speedAccurate: !!action.accurate });
    case "cacheHit":
      return Object.assign({}, st, { cacheHit: action.value || null });
    case "toast":
      return Object.assign({}, st, { toast: action.value });
    default:
      return st;
  }
}

export function dispatch(action) {
  const next = reduce(state, action);
  if (next !== state) { state = next; notify(); }
  return state;
}

/* ---------------- 步骤/行的局部更新 ---------------- */
function runOf(st) {
  return st.run && Array.isArray(st.run.steps) ? st.run : { steps: [] };
}

function withRun(st, steps, extra) {
  return Object.assign({}, st, { run: { steps } }, extra || {});
}

// patchStep 按 key 改一步:先找当前过程,再找已经并进消息卡片的步骤
// (工具回执可能晚于"回复正文"到达,那一步此刻住在 assistant 行里)。
export function patchStep(st, key, patch) {
  const run = runOf(st);
  let hit = false;
  const steps = run.steps.map((s) => {
    if (s.key !== key) return s;
    hit = true;
    return Object.assign({}, s, patch);
  });
  if (hit) return withRun(st, steps);
  let rowHit = false;
  const rows = st.rows.map((r) => {
    if (!r.steps) return r;
    let local = false;
    const ns = r.steps.map((s) => {
      if (s.key !== key) return s;
      local = true;
      return Object.assign({}, s, patch);
    });
    if (!local) return r;
    rowHit = true;
    return Object.assign({}, r, { steps: ns });
  });
  return rowHit ? Object.assign({}, st, { rows }) : st;
}

function stepsWithKey(st, key, fallbackName) {
  const run = runOf(st);
  for (const s of run.steps) if (s.key === key) return s;
  for (let i = st.rows.length - 1; i >= 0; i--) {
    const row = st.rows[i];
    if (!row.steps) continue;
    for (const s of row.steps) if (s.key === key) return s;
  }
  if (!fallbackName) return null;
  const hit = findOpenStep(run.steps, fallbackName);
  if (hit) return hit;
  for (let i = st.rows.length - 1; i >= 0; i--) {
    const row = st.rows[i];
    if (!row.steps) continue;
    const found = findOpenStep(row.steps, fallbackName);
    if (found) return found;
  }
  return null;
}

// adoptOrPushToolStep:tool_start 时优先复用"参数还在生成"的那一步,
// 否则追加一步执行中的。同一张卡转正,界面不闪。
function startToolStep(st, msg) {
  const name = msg.tool_name || "tool";
  const args = msg.tool_args == null ? "" : String(msg.tool_args);
  const now = Date.now();
  const run = runOf(st);
  const prep = [...run.steps].reverse().find((s) => s.kind !== "think" && s.phase === "prep" && s.name === name);
  if (prep) {
    const steps = run.steps.map((s) => s.key === prep.key ? Object.assign({}, s, {
      phase: "run", args, t0: now, timeout: toolTimeoutSec(args), folded: false, holdUntil: 0,
    }) : s);
    return withRun(st, steps);
  }
  const step = makeToolStep({
    name,
    args,
    phase: "run",
    t0: now,
    timeout: toolTimeoutSec(args),
    folded: false,
  });
  // 新步骤一到,已经跑完的那几步立刻收进折叠条(旧 liveHoldFlush 的语义)。
  const folded = run.steps.map((s) => (s.phase === "done" ? Object.assign({}, s, { folded: true, holdUntil: 0 }) : s));
  return withRun(st, folded.concat([step]));
}

// appendToolArgs:模型还在生成参数时刷新"预备步骤"。推来的是累计快照,每次整体重算。
function upsertPrepStep(st, msg) {
  const name = msg.tool_name || "tool";
  const raw = msg.content == null ? "" : String(msg.content);
  const run = runOf(st);
  const steps = run.steps.slice();
  let prep = null;
  for (let i = steps.length - 1; i >= 0; i--) {
    if (steps[i].phase === "prep" && steps[i].name === name) { prep = steps[i]; break; }
  }
  if (!prep) {
    // 同名的已经在跑:不另开一张"预备"卡
    if (steps.some((s) => s.kind !== "think" && s.name === name && s.phase === "run")) return st;
    // 兜底:别攒出一堆空卡
    if (steps.filter((s) => s.phase === "prep").length >= 8) return st;
    prep = makeToolStep({ name, args: "", phase: "prep", folded: false });
    steps.push(prep);
  }
  const next = steps.map((s) => s.key === prep.key ? Object.assign({}, s, { args: raw, phase: "prep" }) : s);
  return withRun(st, next);
}

function foldRun(st) {
  const now = Date.now();
  const steps = runOf(st).steps.map((s) => {
    const next = Object.assign({}, s, { folded: true, holdUntil: 0 });
    if (next.phase === "prep") next.phase = "notrun";
    else if (next.phase === "run" || next.phase === "soft") next.phase = "lost";
    if (next.t0 && next.phase === "done" && !next.durationSec) {
      next.durationSec = Math.max(0, Math.round((now - next.t0) / 1000));
    }
    return next;
  });
  return steps;
}

// pushBatchEnd:一轮任务的收尾。步骤全部折叠;有正文的回复吸收这些步骤,
// 没正文(纯过程)时折叠条独立成一条卡。
function finishRun(st, opts) {
  const steps = foldRun(st);
  const rows = st.rows.slice();
  let rest = steps;
  const text = opts && opts.text != null ? String(opts.text) : "";
  const reasoning = opts && opts.reasoning != null ? String(opts.reasoning) : "";
  if (text) {
    rows.push({ key: newKey("ai"), kind: "assistant", text, reasoning, steps });
    rest = [];
  } else if (reasoning) {
    rest = steps.concat([makeThinkStep(reasoning, true)]);
  }
  if (rest.length) rows.push({ key: newKey("steps"), kind: "steps", steps: rest });
  return Object.assign({}, st, {
    rows,
    run: { steps: [] },
    live: null,
    pending: null,
  });
}

/* ---------------- SSE 事件归约 ---------------- */
export function applyEvent(st, m) {
  if (!m || !m.kind) return st;

  // 审批事件跨会话(子代理/其他会话的危险操作也要用户拍板),先于会话过滤处理。
  if (m.kind === "approval_request") {
    let p = null;
    try { p = JSON.parse(m.content); } catch (e) { return st; }
    if (!p || !p.id || st.approvals[p.id]) return st;
    const approvals = Object.assign({}, st.approvals, { [p.id]: p });
    return Object.assign({}, st, {
      approvals,
      approvalShown: st.approvalShown || p.id,
    });
  }
  if (m.kind === "approval_resolved") {
    let p = null;
    try { p = JSON.parse(m.content); } catch (e) { return st; }
    if (!p || !p.id) return st;
    if (!st.approvals[p.id]) return st;
    const approvals = Object.assign({}, st.approvals);
    delete approvals[p.id];
    const patch = { approvals };
    if (st.approvalShown === p.id) {
      const rest = Object.keys(approvals);
      patch.approvalShown = rest.length ? rest[0] : null;
    }
    return Object.assign({}, st, patch, {
      toast: {
        id: newKey("toast"),
        msg: (p.approved ? "已批准:" : "已拒绝:") + (p.reason || ""),
        kind: p.approved ? "ok" : "",
      },
    });
  }

  // session_created:新会话的接管。事件可能比 POST /api/messages 的响应早到,
  // 所以按 task_id 记进 recentCreated,响应回来时还能精确匹配。
  if (m.kind === "status" && m.status === "session_created") {
    const sessions = st.sessions.slice();
    if (!sessions.some((s) => s.session_id === m.session_id)) {
      sessions.unshift({ session_id: m.session_id, title: "新会话", status: "created", message_count: 0 });
    }
    let recentCreated = st.recentCreated;
    if (m.task_id) {
      recentCreated = st.recentCreated.concat([{ task_id: m.task_id, session_id: m.session_id }]).slice(-8);
    }
    const patch = { sessions, recentCreated };
    if (m.task_id && st.pendingTask && m.task_id === st.pendingTask) {
      patch.current = m.session_id;
      patch.pendingTask = null;
      // sending 不清:要等 task_started 才结束发送保护期(与旧 adoptNewSession 一致)。
      patch.tokens = 0;
      patch.cacheHit = null;
      patch.speed = 0;
      patch.speedAccurate = false;
    }
    return Object.assign({}, st, patch);
  }

  // 严格按会话隔离:其他会话的事件不写入当前视图,只当作列表变化信号(由 sse 层刷新)。
  if (m.session_id && m.session_id !== st.current) return st;

  switch (m.kind) {
    case "reasoning_delta": {
      const live = st.live || { content: "", reasoning: "", startedAt: Date.now() };
      const text = m.reasoning != null ? m.reasoning : (m.content || "");
      return Object.assign({}, st, { live: Object.assign({}, live, { reasoning: text }), pending: null });
    }
    case "content_delta": {
      const live = st.live || { content: "", reasoning: "", startedAt: Date.now() };
      return Object.assign({}, st, {
        live: Object.assign({}, live, { content: m.content == null ? "" : String(m.content) }),
        pending: null,
      });
    }
    case "tool_start": {
      // 工具调用之前已经生成的那段正文先固化成一条回复(旧 liveCommit)。
      let s = st;
      const live = s.live;
      if (live && live.content) {
        const row = { key: newKey("ai"), kind: "assistant", text: live.content, reasoning: live.reasoning, steps: runOf(s).steps };
        s = Object.assign({}, s, { rows: s.rows.concat([row]), run: { steps: [] }, live: null });
      } else if (live && live.reasoning) {
        const steps = runOf(s).steps.concat([makeThinkStep(live.reasoning, false)]);
        s = Object.assign({}, s, { run: { steps }, live: null });
      }
      s = Object.assign({}, s, { pending: null });
      return startToolStep(s, m);
    }
    case "tool_args":
      return upsertPrepStep(st, m);
    case "tool_output": {
      const chunk = m.content == null ? "" : String(m.content);
      if (!chunk) return st;
      const run = runOf(st);
      const liveSteps = run.steps.filter((s) => s.phase === "run" || s.phase === "prep");
      if (!liveSteps.length) return st;
      let target = null;
      for (let i = liveSteps.length - 1; i >= 0; i--) if (liveSteps[i].name === (m.tool_name || "")) { target = liveSteps[i]; break; }
      if (!target) target = liveSteps[liveSteps.length - 1];
      const next = run.steps.map((s) => s.key === target.key ? Object.assign({}, s, {
        output: tail(s.output + chunk),
        outputIsReal: true,
      }) : s);
      return withRun(st, next);
    }
    case "tool_end": {
      const name = m.tool_name || "tool";
      const target = stepsWithKey(st, armKey(m), name);
      if (!target) return st;
      const result = m.tool_result == null ? "" : String(m.tool_result);
      const now = Date.now();
      const durationSec = target.t0 ? Math.max(0, Math.round((now - target.t0) / 1000)) : 0;
      const elapsed = target.t0 ? now - target.t0 : 900;
      const folded = elapsed >= 900;
      return patchStep(st, target.key, {
        result,
        phase: "done",
        durationSec,
        folded,
        holdUntil: folded ? 0 : now + (900 - elapsed),
        outputIsReal: true,
      });
    }
    case "context_usage":
      return Object.assign({}, st, { tokens: m.tokens || 0 });
    case "usage": {
      const patch = {
        cacheHit: { prompt: m.prompt_tokens || 0, cached: m.cached_tokens || 0, reported: !!m.cache_reported },
      };
      if (m.speed > 0) {
        patch.speed = m.speed;
        patch.speedAccurate = true;
      }
      return Object.assign({}, st, patch);
    }
    case "assistant": {
      const text = m.content != null ? String(m.content) : (st.live ? st.live.content : "");
      const reasoning = m.reasoning != null ? String(m.reasoning) : (st.live ? st.live.reasoning : "");
      const s = finishRun(st, { text, reasoning });
      return Object.assign({}, s, { running: false, sending: false });
    }
    case "error": {
      const s = finishRun(st, { text: "" });
      const rows = s.rows.concat([{ key: newKey("error"), kind: "error", text: String(m.content || "") }]);
      return Object.assign({}, s, { rows, running: false, sending: false });
    }
    case "status":
      if (m.status === "task_started") {
        return Object.assign({}, st, { sending: false, running: true });
      }
      if (m.status === "interrupted") {
        const s = finishRun(st, { text: "" });
        const rows = s.rows.concat([{ key: newKey("status"), kind: "status", text: "⏹ 任务已中断(已保留进度,可继续发送消息)" }]);
        return Object.assign({}, s, { rows, running: false, sending: false });
      }
      if (m.status === "task_done") {
        const s = finishRun(st, { text: "" });
        return Object.assign({}, s, { running: false, sending: false });
      }
      return st;
    default:
      return st;
  }
}

function tail(s) {
  const str = String(s == null ? "" : s);
  return str.length > 4000 ? "…" + str.slice(-3999) : str;
}

// 兼容旧调用:有些事件不带 call_id,只能按工具名配对。
function armKey(m) {
  return m.call_id || "";
}

/* ---------------- 断线对账(resync) ----------------
   SSE 是发后即忘:tool_end 这类一次性事件在断流窗口里丢了就永远不回,
   「正在执行」卡会挂在界面里继续计时。对账读的是会话事件账本(tail_run),
   是断流窗口可能藏掉的那一段:
   - tool/result 事件按工具名配给"待返回/运行中"的步骤(每份结果只配一次);
   - 没有回执的调用若任务还在跑:按 tool/call 的 created_at 恢复计时起点;
   - 配不上也认不出时刻的不动它;任务其实已结束(active=false)→ 剩余步骤停表收条。
   只填已发生的、不发明未发生的:对账本身幂等,多跑一次只是多一次 GET。 */
function parseEvTime(s) {
  const t = Date.parse(String(s || "").replace(/(\.\d{3})\d+/, "$1"));
  return isFinite(t) ? t : 0;
}

// 收集 state 里所有还没回执的步骤(id 级),供对账配对。
function openStepsOf(st) {
  const out = [];
  for (const s of runOf(st).steps) if (isOpen(s)) out.push(s);
  for (const row of st.rows) {
    if (!row.steps) continue;
    for (const s of row.steps) if (isOpen(s)) out.push(s);
  }
  return out;
}

function isOpen(s) {
  return s && s.kind !== "think" && s.result == null && s.phase !== "notrun" && s.phase !== "lost";
}

export function applyResync(st, events, active) {
  // tool/result 修订折叠:同 call_id 后到的行覆盖先到的(全文行被修剪行取代)。
  const lastByCall = {};
  events.forEach((ev, i) => {
    if (ev.type === "tool/result" && ev.payload && ev.payload.call_id) lastByCall[ev.payload.call_id] = i;
  });
  const done = [];
  const lastCall = {};
  const lastRes = {};
  events.forEach((ev, i) => {
    const p = ev.payload || {};
    const nm = String(p.name || "tool");
    if (ev.type === "tool/result") {
      if (p.call_id && lastByCall[p.call_id] !== i) return; // 已被后到修订行覆盖
      done.push({ name: nm, text: String(p.result != null ? p.result : (p.error || "")) });
      lastRes[nm] = i;
    } else if (ev.type === "tool/call") {
      lastCall[nm] = { i: i, t: parseEvTime(ev.created_at), args: p.args };
    }
  });

  const used = {};
  let s = st;
  for (const step of openStepsOf(st)) {
    const name = step.name || "tool";
    let filled = false;
    for (let i = 0; i < done.length; i++) {
      if (used[i] || done[i].name !== name) continue;
      used[i] = true;
      s = patchStep(s, step.key, {
        result: done[i].text,
        phase: "done",
        folded: true,
        holdUntil: 0,
        outputIsReal: true,
      });
      filled = true;
      break;
    }
    if (filled || !active) continue;
    // 结果还没回来、任务确实还在跑:把这一步恢复成"正在执行"的样子。
    // 计时起点从事件账本的 tool/call 时刻来 —— 刷新不该把已经跑了 260s 的调用归零。
    const c = lastCall[name];
    if (!c) continue; // 账本里压根没有这次调用:无从判断,保持原样
    const pending = lastRes[name] === undefined || c.i > lastRes[name];
    if (!pending) continue;
    if (c.t) {
      s = patchStep(s, step.key, {
        t0: c.t,
        phase: "run",
        folded: false,
        timeout: step.timeout || toolTimeoutSec(c.args),
        args: step.phase === "soft" && !step.args && c.args != null ? String(c.args) : step.args,
      });
    }
  }
  if (!active) {
    for (const step of openStepsOf(s)) {
      const phase = step.phase === "prep" ? "notrun" : "lost";
      s = patchStep(s, step.key, { phase, folded: true, holdUntil: 0 });
    }
  }
  return s;
}

/* ---------------- 便捷 action ---------------- */
export function applySse(m) { return dispatch({ type: "sse", msg: m }); }

// 审批弹窗只是"视图":关掉它不会撤掉待审批请求,徽标上的数字还在。
export function showApproval(id) { setState({ approvalShown: id || null }); }
export function hideApproval() { setState({ approvalShown: null }); }
