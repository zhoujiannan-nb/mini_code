/* ---------------- 会话列表 ---------------- */
async function loadSessions(select) {
  try {
    state.sessions = await api("/api/sessions");
  } catch (e) { return; }
  renderSessions();
  syncRunning();
  loadArchived(); // 归档列表随主列表一起收敛(轻量查询)
  if (state.current) loadChildren(); // 子任务状态随列表一起收敛
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
  const cnt = $("#sess-count");
  if (cnt) cnt.textContent = state.sessions.length ? String(state.sessions.length) : "";
  if (state.sessions.length === 0) {
    list.innerHTML = '<div class="empty-hint">暂无会话<br>发送第一条消息开始</div>';
    return;
  }
  for (const s of state.sessions) {
    const el = document.createElement("div");
    el.className = "sess-item" + (s.session_id === state.current ? " active" : "");
    // 拖到侧边栏的群上 = 加入该群;拖到"已归档"区 = 归档(不在会话列表显示)
    el.draggable = true;
    el.addEventListener("dragstart", (e) => {
      e.dataTransfer.setData("text/plain", JSON.stringify({ kind: "session", sid: s.session_id }));
      e.dataTransfer.effectAllowed = "move";
      // 拖会话入群/归档的落点必须可见:分区若折叠则临时展开(remember=false,
      // 不写 localStorage,否则会永远记住展开态)
      setGroupCollapsed(false);
      setArchiveCollapsed(false, false);
    });
    el.addEventListener("dragend", () => {
      // 无论松手在哪(归档成功/恢复/取消拖拽),都回到用户自己的折叠偏好(默认折叠)
      setArchiveCollapsed(userArchiveCollapsed(), false);
    });
    // 执行中只看服务端的 active 标志(本进程是否有活动任务):
    // 持久化的 status 在进程崩溃/重启后可能停留在 "running",不可信。
    const dotClass = s.active ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
    const statusText = s.active ? "执行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || "";
    el.innerHTML =
      '<div class="sess-title-row">' +
      '<span class="sess-ava" style="background:' + hashColor(s.session_id) + '">' + esc(firstChar(s.title)) + '</span>' +
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
    if (state.current === id) setChatTitle(r.title, s ? (s.work_dir || "").slice(0, 60) : "");
    renderSessions();
    toast("已重命名", "ok");
  } catch (err) { toast("重命名失败:" + err.message, "err"); }
}

/* ---------------- 已归档 ---------------- */
// 已归档对话:与正常会话的唯一区别是不显示在会话列表(侧边栏"已归档"
// 分区,平常收起)。归档的会话照常可打开、继续发消息。
// 归档 = 会话 metadata 里的一个标记,不改变发给模型的任何内容(前缀缓存安全)。
// 归档状态标记:拖会话到"已归档"区 = 归档;拖回会话区或点 ↩ = 恢复。
// v2:旧版 key 曾被"拖动时自动展开"污染(误写展开态),换键重置回默认折叠
const LS_ARCHIVE_COLLAPSED_KEY = "mini_code.archive_collapsed.v2";
// remember=false:临时态(拖会话时自动展开落点);只有手动切换才写 localStorage
function setArchiveCollapsed(c, remember) {
  const sec = $("#archive-sec"), t = $("#archive-toggle");
  if (!sec) return;
  sec.classList.toggle("collapsed", c);
  if (t) t.textContent = c ? "▸" : "▾";
  if (remember) { try { localStorage.setItem(LS_ARCHIVE_COLLAPSED_KEY, c ? "1" : "0"); } catch (e) {} }
}
// 用户自己的偏好:默认折叠;拖动的自动展开/收起不修改它
function userArchiveCollapsed() {
  try { return localStorage.getItem(LS_ARCHIVE_COLLAPSED_KEY) !== "0"; } catch (e) { return true; }
}
async function loadArchived() {
  try {
    state.archived = await api("/api/sessions?archived=1");
  } catch (e) { state.archived = []; }
  renderArchiveList();
}
function renderArchiveList() {
  const list = $("#archive-list");
  if (!list) return;
  const cnt = $("#archive-count");
  if (cnt) cnt.textContent = state.archived.length ? String(state.archived.length) : "";
  list.innerHTML = "";
  if (!state.archived.length) {
    list.innerHTML = '<div class="empty-hint" style="padding:10px 8px">暂无已归档对话<br>拖会话到此处归档</div>';
    return;
  }
  for (const s of state.archived) {
    const el = document.createElement("div");
    el.className = "sess-item arch-item" + (s.session_id === state.current ? " active" : "");
    // 拖回会话区 = 恢复
    el.draggable = true;
    el.addEventListener("dragstart", (e) => {
      e.dataTransfer.setData("text/plain", JSON.stringify({ kind: "archived", sid: s.session_id }));
      e.dataTransfer.effectAllowed = "move";
    });
    const dotClass = s.active ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
    const statusText = s.active ? "执行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || "";
    el.innerHTML =
      '<div class="sess-title-row">' +
      '<span class="sess-ava" style="background:' + hashColor(s.session_id) + '">' + esc(firstChar(s.title)) + '</span>' +
      '<div class="sess-title">' + esc(s.title || "未命名会话") + '</div></div>' +
      '<div class="sess-meta"><span class="s ' + dotClass + '"></span>' +
      (statusText ? "<span>" + statusText + "</span>" : "") +
      "<span>" + relTime(s.updated_at) + "</span><span>" + s.message_count + " 条</span></div>" +
      '<button class="arch-btn" title="恢复到会话列表">↩</button>' +
      '<button class="rename-btn" title="重命名会话">✎</button>' +
      '<button class="del-btn" title="删除会话">✕</button>';
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("arch-btn") || e.target.classList.contains("del-btn") || e.target.classList.contains("rename-btn")) return;
      selectSession(s.session_id);
    });
    el.querySelector(".arch-btn").addEventListener("click", (e) => {
      e.stopPropagation();
      setArchived(s.session_id, false);
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
        await loadSessions(); // loadSessions 会连带刷新归档列表
        toast("会话已删除", "ok");
      } catch (err) { toast("删除失败:" + err.message, "err"); }
    });
    list.appendChild(el);
  }
}
async function setArchived(sid, on) {
  try {
    await api("/api/sessions/" + sid + "/archive", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ archived: on }),
    });
    toast(on ? "已归档(不在会话列表显示)" : "已恢复到会话列表", "ok");
    await loadSessions(); // 主列表 + 归档列表一起收敛
  } catch (err) { toast("操作失败:" + err.message, "err"); }
}
// 拖拽载荷:新版为 JSON({kind:"session"|"archived", sid}),
// 兼容旧版纯 sid 文本(按会话处理)。
function readDragPayload(e) {
  const raw = e.dataTransfer.getData("text/plain");
  if (!raw) return null;
  try {
    const d = JSON.parse(raw);
    if (d && d.sid) return d;
  } catch (err) {}
  return { kind: "session", sid: raw };
}
function initArchiveDragDrop() {
  // 注意:HTML5 DnD 里 dataTransfer.getData() 只在 drop 事件能读到值,
  // dragover 期间恒为空(安全限制)——所以 dragover 只能查 types 放行,
  // drop 里再读真实载荷按 kind 过滤(否则 drop 永不触发,拖动无效)。
  const hasText = (e) => e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "text/plain") >= 0;
  const zone = $("#archive-sec");
  zone.addEventListener("dragover", (e) => {
    if (hasText(e)) { e.preventDefault(); e.dataTransfer.dropEffect = "move"; zone.classList.add("drop-hint"); }
  });
  zone.addEventListener("dragleave", (e) => {
    if (!zone.contains(e.relatedTarget)) zone.classList.remove("drop-hint");
  });
  zone.addEventListener("drop", async (e) => {
    zone.classList.remove("drop-hint");
    const p = readDragPayload(e);
    if (!p || p.kind !== "session") return;
    e.preventDefault();
    await setArchived(p.sid, true);
  });
  // 恢复落点:会话列表(含"会话"分区头)
  const list = $("#sess-list");
  const attachRestore = (target) => {
    target.addEventListener("dragover", (e) => {
      if (hasText(e)) { e.preventDefault(); e.dataTransfer.dropEffect = "move"; list.classList.add("drop-hint"); }
    });
    target.addEventListener("dragleave", (e) => {
      if (!target.contains(e.relatedTarget)) list.classList.remove("drop-hint");
    });
    target.addEventListener("drop", async (e) => {
      list.classList.remove("drop-hint");
      const p = readDragPayload(e);
      if (!p || p.kind !== "archived") return;
      e.preventDefault();
      await setArchived(p.sid, false);
    });
  };
  attachRestore(list);
  const head = list.previousElementSibling;
  if (head && head.classList.contains("sec-head")) attachRestore(head);
}

