/* ---------------- 会话列表 ---------------- */
// 列表按 worker(工作大类)分桶:GET /api/sessions?worker=<id>。
// state.worker: 当前分桶 id,「其他」=w-other 兜底(见 workers.js;没有"未分配"也没有"全部"这一档)。
// 列表刷新中的两个状态:切换 worker / 删 worker 会先清空再拉(fresh),那几百毫秒
// 摆骨架而不是留着上一个 worker 的旧行;拉失败要说出来,静默 return 会让旧行一直
// 挂在界面上,谁也不知道其实没刷出来。
let sessionsLoading = false;
let sessionsError = "";

// beginSessionsRefresh empties the list and paints the skeleton right away, so a
// slow round trip never leaves another worker's rows on screen.
function beginSessionsRefresh() {
  sessionsError = "";
  state.sessions = [];
  sessionsLoading = true;
  renderSessions();
}

// sessSkeleton is the "拉列表中" placeholder: three ghost rows in the shape of a
// session row, so the panel does not jump when the real rows land.
function sessSkeleton() {
  let html = "";
  for (let i = 0; i < 3; i++) {
    html += '<div class="sess-item sess-skeleton" aria-hidden="true">' +
      '<div class="sk sk-title"></div><div class="sk sk-meta"></div></div>';
  }
  return html;
}

async function loadSessions(select, fresh) {
  const bucket = typeof workerBucketId === "function" ? workerBucketId() : (state.worker || "");
  if (fresh) beginSessionsRefresh();
  try {
    state.sessions = await api("/api/sessions?worker=" + encodeURIComponent(bucket));
    sessionsError = "";
  } catch (e) {
    sessionsLoading = false;
    // 后台轮询失败不吓人(留着旧行,下一轮自己收敛);用户主动切换/删除时清空了,
    // 那就必须告诉他为什么是空的。
    if (fresh) { sessionsError = (e && e.message) || String(e); renderSessions(); }
    return;
  }
  sessionsLoading = false;
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
    if (!state.sending && !state.pendingTask) { setChildrenActive(false); setRunning(false); }
    return;
  }
  const s = state.sessions.find(x => x.session_id === state.current);
  if (!s) return;
  // active = 本会话自己有任务在跑 → 上锁输入框;children_active = 后台子 agent
  // 还在跑 → 只画停止按钮,输入框照常能用(见 send.js paintRunButtons)。
  setChildrenActive(!!s.children_active);
  setRunning(!!s.active);
  // 兜底:SSE 漏掉终结事件(assistant/task_done)时,服务端已收敛为空闲、
  // 本地也没有流式块——把残留的"等待模型响应"转圈清掉(最多等一次 30s 轮询)
  if (!s.active && !state.sending && !state.live) pendingRemove();
}
function renderSessions() {
  // 后台命令入口徽标跟着列表刷新收敛(它只依赖当前会话的摘要字段)。
  if (typeof paintJobsBtn === "function") paintJobsBtn();
  const list = $("#sess-list");
  list.innerHTML = "";
  if (sessionsLoading) { list.innerHTML = sessSkeleton(); return; }
  if (sessionsError) {
    list.innerHTML = '<div class="empty-hint">会话列表没刷出来：' + esc(sessionsError) +
      '<br>30s 轮询会自动重试,切一下 worker 也能立刻重来</div>';
    return;
  }
  // 「会话 N」分组标题已删（计数在 worker 条的名字后面，不再重复一遍）
  if (state.sessions.length === 0) {
    list.innerHTML = '<div class="empty-hint">' + workerEmptyHint() + '</div>';
    return;
  }
  for (const s of state.sessions) {
    const el = document.createElement("div");
    el.className = "sess-item" + (s.session_id === state.current ? " active" : "");
    // 拖到侧边栏的群上 = 加入该群(会话归属创建即固定,不再能移动 worker)
    el.draggable = true;
    el.addEventListener("dragstart", (e) => {
      e.dataTransfer.setData("text/plain", JSON.stringify({ kind: "session", sid: s.session_id }));
      e.dataTransfer.effectAllowed = "move";
      setGroupCollapsed(false);
    });
    // 执行中只看服务端的实时标志(本进程是否有活动任务):
    // 持久化的 status 在进程崩溃/重启后可能停留在 "running",不可信。
    // 后台子 agent 在跑也算执行中(侧边栏是"这个会话有没有活在干"的口径;
    // 输入框是否上锁另说,由 active 决定)。会话自己的后台命令(exec 后台化)
    // 还活着时按"后台运行中"显示:agent 空闲等通知,但活确实还在干。
    const busy = s.active || !!s.children_active || (s.jobs_running > 0);
    const dotClass = busy ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
    const statusText = s.active ? "执行中" : (s.children_active ? "子任务中" : (s.jobs_running > 0 ? "后台运行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || ""));
    el.innerHTML =
      '<div class="sess-title-row">' +
      agentAvatarBox("sess-ava", "span") +
      '<div class="sess-title">' + esc(s.title || "未命名会话") + '</div></div>' +
      '<div class="sess-meta"><span class="s ' + dotClass + '"></span>' +
      (s.mode === "cautious" ? '<span title="谨慎模式">🛡</span>' : "") +
      (statusText ? "<span>" + statusText + "</span>" : "") +
       "<span>" + relTime(s.updated_at) + "</span><span>" + s.message_count + " 条</span></div>" +
       '<button class="rename-btn" title="重命名会话">✎</button>' +
       '<button class="del-btn" title="删除会话">✕</button>';
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("del-btn") || e.target.classList.contains("rename-btn")) return;
      selectSession(s.session_id);
    });
    el.querySelector(".rename-btn").addEventListener("click", (e) => {
      e.stopPropagation();
      renameSession(s.session_id, s.title);
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
    if (state.current === id) setChatTitle(r.title, "");
    renderSessions();
    toast("已重命名", "ok");
  } catch (err) { toast("重命名失败:" + err.message, "err"); }
}

// (removed) 移动到 Worker:会话归属在创建时就固定,worker 的共享目录与会话工作
// 目录从出生起就是同一个,不存在"搬家"了。
// 拖拽载荷:新版为 JSON({kind:"session", sid}),兼容旧版纯 sid 文本(按会话处理)。
// kind 目前只有 session:群区分组用 sid 加入。
function readDragPayload(e) {
  const raw = e.dataTransfer.getData("text/plain");
  if (!raw) return null;
  try {
    const d = JSON.parse(raw);
    if (d && d.sid) return d;
  } catch (err) {}
  return { kind: "session", sid: raw };
}
