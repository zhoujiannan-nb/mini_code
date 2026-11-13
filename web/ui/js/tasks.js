/* ---------------- 子任务(子代理)面板 ---------------- */
async function loadChildren() {
  if (!state.current) { state.children = []; renderTasksPanel(); return; }
  try {
    state.children = await api("/api/sessions/" + state.current + "/children");
  } catch (e) { state.children = []; }
  renderTasksPanel();
}
function renderTasksPanel() {
  const btn = $("#btn-tasks");
  const kids = state.children || [];
  const running = kids.filter(c => c.active).length;
  // 子任务面板是「当前会话的子任务」的权威视图:它一刷,停止按钮的后台那一半
  // 就跟着收敛(派出子任务后不必等会话列表那轮轮询)。
  if (typeof setChildrenActive === "function") setChildrenActive(running > 0);
  btn.classList.toggle("show", !!state.current && kids.length > 0);
  const cnt = $("#tasks-count");
  cnt.textContent = running > 0 ? running : kids.length;
  cnt.classList.toggle("hot", running > 0);
  const panel = $("#tasks-panel");
  if (!panel.classList.contains("open")) return;
  if (kids.length === 0) {
    panel.innerHTML = "<h4>子任务</h4><div class='empty-hint'>暂无子任务</div>";
    return;
  }
  const now = Date.now();
  panel.innerHTML = "<h4>子任务(点击打开子会话)</h4>" + kids.map(c => {
    const dot = c.active ? "running" : (["completed", "failed", "interrupted"].includes(c.status) ? c.status : "idle");
    const st = c.active ? "执行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[c.status] || "";
    const stop = c.active ? '<button class="task-stop">停止</button>' : "";
    // 右栏统计:全程 token 合计 + 用时(运行中从创建起算,已结束按最后更新)。
    const tok = fmtTok(c.tokens || 0);
    const dur = fmtTaskDur(c, now);
    const meta = (tok || dur) ? '<span class="t-meta"><span>' + (tok || "—") + "</span><span>" +
      (dur ? dur + (st ? " · " + st : "") : st) + "</span></span>" : "";
    const tip = (c.title || c.session_id) + (st ? "(" + st + ")" : "") + "\n" + c.session_id;
    return '<div class="task-item" data-id="' + c.session_id + '" title="' + esc(tip) + '">' +
      '<span class="s ' + dot + '"></span>' +
      '<span class="t1">' + esc(c.title || c.session_id.slice(0, 8)) +
      (c.agent_role && c.agent_role !== "build" ? " <i>" + esc(c.agent_role) + "</i>" : "") + "</span>" +
      '<span class="t-act">' + stop + meta + "</span></div>";
  }).join("");
  panel.querySelectorAll(".task-item").forEach(el => {
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("task-stop")) {
        e.stopPropagation();
        stopTask(el.dataset.id);
        return;
      }
      closeTasksPanel();
      selectSession(el.dataset.id);
    });
  });
}
// fmtTok: token 合计的紧凑写法(4.4M / 128k);0 返空串。
function fmtTok(n) {
  if (!n || n < 1) return "";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M tok";
  if (n >= 1e3) return Math.round(n / 1e3) + "k tok";
  return n + " tok";
}
// fmtTaskDur: 子任务用时。运行中 = now - 创建;已结束 = 最后更新 - 创建。
// created_at/updated_at 是带时区的 RFC3339,Date.parse 直接可吃。
function fmtTaskDur(c, now) {
  const t0 = Date.parse(c.created_at);
  if (isNaN(t0)) return "";
  const t1 = c.active ? now : (Date.parse(c.updated_at) || now);
  const sec = Math.max(0, Math.round((t1 - t0) / 1000));
  if (sec < 60) return sec + "秒";
  const m = Math.floor(sec / 60);
  if (m < 60) return m + "分" + (sec % 60) + "秒";
  return Math.floor(m / 60) + "小时" + (m % 60) + "分";
}
function toggleTasksPanel() {
  const p = $("#tasks-panel");
  if (p.classList.contains("open")) { closeTasksPanel(); return; }
  p.classList.add("open");
  loadChildren();
}
function closeTasksPanel() { $("#tasks-panel").classList.remove("open"); }
async function stopTask(id) {
  try {
    const r = await api("/api/sessions/" + id + "/cancel", { method: "POST" });
    toast(r.canceled ? "已发送停止请求" : "该子任务当前没有正在执行的任务", r.canceled ? "ok" : "");
    loadChildren();
  } catch (e) { toast("停止失败:" + e.message, "err"); }
}
function paintBackBtn(parentId) {
  const b = $("#btn-back-parent");
  b.classList.toggle("show", !!parentId);
  b.onclick = () => { if (parentId) selectSession(parentId); };
}
