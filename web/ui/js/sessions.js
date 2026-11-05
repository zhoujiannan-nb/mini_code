/* ---------------- 会话列表 ---------------- */
// 列表按 worker(工作大类)分桶:GET /api/sessions?worker=<id>。
// state.worker: "*" = 全部 | "" = 未分配 | 其余 = worker_id(见 workers.js)。
async function loadSessions(select) {
  try {
    state.sessions = await api("/api/sessions?worker=" + encodeURIComponent(state.worker == null ? "*" : state.worker));
  } catch (e) { return; }
  renderSessions();
  syncRunning();
  if (state.current) loadChildren(); // 子任务状态随列表一起收敛
  if (typeof paintWorkerBar === "function") paintWorkerBar();
  if (select && select !== state.current) selectSession(select);
}
// SSE 事件是"有会话发生了变化"的信号:其他会话的事件不进入当前视图,
// 但触发一次防抖的列表刷新,让侧边栏状态在秒级收敛(而不是等 30s 轮询)。
let listRefreshTimer = null;
function scheduleListRefresh(delay) {
  if (listRefreshTimer) return;
  listRefreshTimer = setTimeout(() => {
    listRefreshTimer = null;
    loadSessions();
    if (typeof loadWorkers === "function") loadWorkers(); // 会话数徽标跟着收敛
  }, delay == null ? 1500 : delay);
}
// 用服务端状态校准当前会话的执行状态:
// - 页面刷新 / SSE 漏掉终结事件后,停止按钮与运行指示都能自动恢复或收敛;
// - 会话刚发出消息、session_created 还没回来时(sending),不做降级。
function syncRunning() {
  // 刚发送、task_started 还没回来:保持 running,防止在途的旧会话列表
  // (active 尚未置位)把 running 误降级——否则停止按钮会闪灭、
  // 状态迟迟变不成"运行中"。
  if (state.sending && state.running) return;
  if (!state.current) {
    // 消息刚发出、会话还没创建完:保持 running,等 session_created 接管
    if (!state.sending && !state.pendingTask) setRunning(false);
    return;
  }
  const s = state.sessions.find(x => x.session_id === state.current);
  if (!s) return;
  setRunning(!!s.active);
  // 兜底:SSE 漏掉终结事件(assistant/task_done)时,服务端已收敛为空闲、
  // 本地也没有流式块——把残留的"等待模型响应"转圈清掉(最多等一次 30s 轮询)
  if (!s.active && !state.sending && !state.live) pendingRemove();
}
function renderSessions() {
  const list = $("#sess-list");
  list.innerHTML = "";
  // 「会话 N」分组标题已删（计数在 worker 条的名字后面，不再重复一遍）
  if (state.sessions.length === 0) {
    list.innerHTML = '<div class="empty-hint">' + workerEmptyHint() + '</div>';
    return;
  }
  for (const s of state.sessions) {
    const el = document.createElement("div");
    el.className = "sess-item" + (s.session_id === state.current ? " active" : "");
    // 拖到侧边栏的群上 = 加入该群;拖到 worker 菜单里的分桶 = 移动到该 worker
    el.draggable = true;
    el.addEventListener("dragstart", (e) => {
      e.dataTransfer.setData("text/plain", JSON.stringify({ kind: "session", sid: s.session_id }));
      e.dataTransfer.effectAllowed = "move";
      setGroupCollapsed(false);
    });
    // 执行中只看服务端的 active 标志(本进程是否有活动任务):
    // 持久化的 status 在进程崩溃/重启后可能停留在 "running",不可信。
    const dotClass = s.active ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
    const statusText = s.active ? "执行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || "";
    el.innerHTML =
      '<div class="sess-title-row">' +
      agentAvatarBox("sess-ava", "span") +
      '<div class="sess-title">' + esc(s.title || "未命名会话") + '</div></div>' +
      '<div class="sess-meta"><span class="s ' + dotClass + '"></span>' +
      (s.mode === "cautious" ? '<span title="谨慎模式">🛡</span>' : "") +
      (statusText ? "<span>" + statusText + "</span>" : "") +
       "<span>" + relTime(s.updated_at) + "</span><span>" + s.message_count + " 条</span></div>" +
       '<button class="rename-btn" title="重命名会话">✎</button>' +
       '<button class="wk-btn" title="移动到 Worker">📁</button>' +
       '<button class="del-btn" title="删除会话">✕</button>';
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("del-btn") || e.target.classList.contains("rename-btn") || e.target.classList.contains("wk-btn")) return;
      selectSession(s.session_id);
    });
    el.querySelector(".rename-btn").addEventListener("click", (e) => {
      e.stopPropagation();
      renameSession(s.session_id, s.title);
    });
    el.querySelector(".wk-btn").addEventListener("click", (e) => {
      e.stopPropagation();
      openWorkerMoveMenu(s, e.target);
    });
    el.querySelector(".del-btn").addEventListener("click", async (e) => {
      e.stopPropagation();
      if (!confirm("确定删除该会话?(消息将一并删除)")) return;
      try {
        await api("/api/sessions/" + s.session_id, { method: "DELETE" });
        if (state.current === s.session_id) resetToNew();
        await loadSessions();
        toast("会话已删除", "ok");
      } catch (err) { toast("删除失败:" + err.message, "err"); }
    });
    list.appendChild(el);
  }
}

/* ---------------- 会话重命名 ---------------- */
async function renameSession(id, currentTitle) {
  const t = prompt("重命名会话", currentTitle || "");
  if (t == null) return; // 取消
  const title = t.trim();
  if (!title) { toast("标题不能为空", "err"); return; }
  if ([...title].length > 60) { toast("标题过长(最多 60 字)", "err"); return; }
  try {
    const r = await api("/api/sessions/" + id + "/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    const s = state.sessions.find(x => x.session_id === id);
    if (s) s.title = r.title;
    if (state.current === id) setChatTitle(r.title, s ? (s.work_dir || "").slice(0, 60) : "");
    renderSessions();
    toast("已重命名", "ok");
  } catch (err) { toast("重命名失败:" + err.message, "err"); }
}

/* ---------------- 移动到 Worker ---------------- */
// 原"归档"按钮的位置。移动会改变会话的工作目录(变成该 worker 的共享目录),
// 下一条消息还会带上一条 worker 简报(共享目录 + 公约)。已有内容一个字都不动,
// 缓存照常命中 —— 但目录变了这件事要在确认框里说清,让用户自己选时机。
async function moveSessionToWorker(sid, workerID, label) {
  const target = workerLabel(workerID);
  if (target === label) { toast("会话已经在「" + label + "」", "ok"); return; }
  const hasMsg = (state.sessions.find(x => x.session_id === sid) || {}).message_count > 0;
  const tip = "把该会话移动到「" + target + "」?" +
    (hasMsg ? "\n\n注意:之后这个会话的读写都落在该 worker 的共享目录里,下一条消息还会带上一条 worker 简报(共享目录 + 公约)。已有对话不动。" : "");
  if (!confirm(tip)) return;
  try {
    await api("/api/sessions/" + sid + "/worker", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ worker_id: workerID || "" }),
    });
    toast("已移动到 " + target, "ok");
    await Promise.all([loadSessions(), loadWorkers()]);
    // 移动改变了工作目录:重新拉一次会话视图,聊天里的相对路径才会按新目录解析
    if (state.current === sid) await selectSession(sid);
  } catch (err) { toast("移动失败:" + err.message, "err"); }
}

// 拖拽载荷:新版为 JSON({kind:"session", sid}),兼容旧版纯 sid 文本(按会话处理)。
// kind 目前只有 session:群区分组用 sid 加入,worker 移动走会话卡上的 📁 菜单。
function readDragPayload(e) {
  const raw = e.dataTransfer.getData("text/plain");
  if (!raw) return null;
  try {
    const d = JSON.parse(raw);
    if (d && d.sid) return d;
  } catch (err) {}
  return { kind: "session", sid: raw };
}
