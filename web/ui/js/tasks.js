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
  panel.innerHTML = "<h4>子任务(点击打开子会话)</h4>" + kids.map(c => {
    const dot = c.active ? "running" : (["completed", "failed", "interrupted"].includes(c.status) ? c.status : "idle");
    const st = c.active ? "执行中" : { completed: "已完成", failed: "失败", interrupted: "已中断" }[c.status] || "";
    const stop = c.active ? '<button class="task-stop">停止</button>' : "";
    return '<div class="task-item" data-id="' + c.session_id + '">' +
      '<div class="t1"><span class="s ' + dot + '"></span>' + esc(c.title || c.session_id.slice(0, 8)) +
      (st ? " <span style='color:var(--text3);font-weight:400'>" + st + "</span>" : "") +
      '<span class="t-act">' + stop + "</span></div>" +
      '<div class="t2">' + esc(c.session_id.slice(0, 12)) + " · " + relTime(c.updated_at) + "</div></div>";
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

