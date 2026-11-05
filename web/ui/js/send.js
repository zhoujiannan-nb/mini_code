/* ---------------- 发送 / 停止 ---------------- */
function clearSending() {
  state.sending = false;
  clearTimeout(state.sendingTimer);
}
function setRunning(on) {
  state.running = on;
  // btn-stop is hidden by CSS (display:none), so showing it requires an
  // explicit display value — clearing the inline style would fall back to
  // the CSS rule and keep it hidden. 群视图没有单一会话,按钮状态不适用。
  if (!state.currentGroup) {
    $("#btn-send").style.display = on ? "none" : "";
    $("#btn-stop").style.display = on ? "inline-block" : "none";
  }
  compactBtnState();
  renderSessions();
}
function compactBtnState() {
  const b = $("#btn-compact");
  b.disabled = !state.current || state.running || !!b._busy;
}
async function send() {
  // 群视图:消息进群频道,而不是某个会话
  if (state.currentGroup) { groupSend(); return; }
  const input = $("#input");
  const text = input.value.trim();
  const images = state.pendingImages.slice();
  if ((!text && images.length === 0) || state.running) return;
  input.value = "";
  autoGrow();
  liveCommit();
  addUser(text, images);
  mcEmit("send");
  state.pendingImages = [];
  renderImgPreviews();
  pendingStart(); // 等待模型首 token 的转圈提示(vllm 吞长上下文时可见)
  setRunning(true);
  // sending 保持到该任务的 task_started/error 事件回来:期间在途的旧列表
  // (active 尚未置位)不得把 running 降级。SSE 断连时靠 8s 兜底超时收敛。
  state.sending = true;
  clearTimeout(state.sendingTimer);
  state.sendingTimer = setTimeout(() => { state.sending = false; }, 8000);
  try {
    const body = { content: text };
    if (images.length) body.images = images;
    if (state.current) body.session_id = state.current;
    else {
      body.mode = state.mode;   // 新会话:按当前选择的权限模式创建
      body.model = state.model; // 新会话:按当前选择的模型创建
      body.context_reminders = state.contextReminders; // 新会话:按当前开关创建
      body.thinking = state.thinking || "";               // 新会话:思考开关(空 = 模型默认)
      body.reasoning_effort = state.reasoningEffort || ""; // 新会话:思考深度(空 = 模型默认)
      // 新会话:侧边栏选中了某个 worker 就直接建在它里面(第一条消息即带 worker
      // 上下文,不会中途换前缀);"未分配"下留空,由模型自己判断归属。
      if (state.worker) body.worker_id = state.worker;
    }
    const r = await api("/api/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!state.current) {
      state.pendingTask = r.task_id;
      // 竞态兜底:后端先 emit SSE 再回 HTTP,session_created 可能比这个响应早到。
      // 若事件已在 recentCreated 里,此刻按 task_id 精确确认并接管。
      const hit = state.recentCreated.find(e => e.task_id === r.task_id);
      if (hit) {
        state.pendingTask = null;
        adoptNewSession(hit.session_id);
      }
    }
  } catch (e) {
    addError("发送失败:" + e.message);
    clearSending();
    setRunning(false);
  }
}
async function stop() {
  if (!state.current) { toast("会话尚未创建,无法停止", "err"); return; }
  try {
    const r = await api("/api/sessions/" + state.current + "/cancel", { method: "POST" });
    if (r.canceled) {
      toast("已发送停止请求", "ok");
    } else {
      // 服务端没有活动任务(状态可能已经收尾):同步本地,别再留停止按钮
      toast("该会话当前没有正在执行的任务", "");
      setRunning(false);
    }
  } catch (e) { toast("停止失败:" + e.message, "err"); }
}

