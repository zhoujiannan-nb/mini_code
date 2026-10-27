/* ---------------- 审批(谨慎模式) ---------------- */
function approvalBadge() {
  const n = Object.keys(state.approvals).length;
  $("#btn-approval-badge").classList.toggle("show", n > 0);
  $("#approval-count").textContent = n;
}
function onApprovalRequest(p) {
  if (!p || !p.id || state.approvals[p.id]) return;
  state.approvals[p.id] = p;
  approvalBadge();
  if (!state.approvalShown) showApproval(p.id);
}
function onApprovalResolved(p) {
  if (!p || !p.id) return;
  const had = !!state.approvals[p.id];
  delete state.approvals[p.id];
  approvalBadge();
  if (state.approvalShown === p.id) {
    hideApproval();
    toast(p.approved ? "已批准:" + (p.reason || "") : "已拒绝:" + (p.reason || ""), p.approved ? "ok" : "");
  } else if (had) {
    toast((p.approved ? "已批准 " : "已拒绝 ") + (p.reason || ""), "");
  }
  if (!state.approvalShown && Object.keys(state.approvals).length) {
    showApproval(Object.keys(state.approvals)[0]);
  }
}
function showApproval(id) {
  const p = state.approvals[id];
  if (!p) return;
  state.approvalShown = id;
  let sess = esc(p.title || "会话 " + (p.session_id || "").slice(0, 8));
  if (p.parent_id) {
    sess += " <span style='color:var(--text3)'>· 子任务(父:" + esc(p.parent_title || p.parent_id.slice(0, 8)) + ")</span>";
  }
  $("#ap-sess").innerHTML = "来源:" + sess;
  let args = p.args || "";
  try { args = JSON.stringify(JSON.parse(args), null, 2); } catch (e) {}
  $("#ap-tool").innerHTML = '<span class="ap-tname">' + esc(p.tool_name) + "</span>\n" + esc(args);
  $("#approval-mask").classList.add("open");
  clearInterval(state.approvalTimer);
  const tick = () => {
    if (state.approvalShown !== id) { clearInterval(state.approvalTimer); return; }
    const left = Math.max(0, Math.round((new Date(p.deadline) - Date.now()) / 1000));
    $("#ap-deadline").textContent = "超时自动拒绝 · 剩余 " + Math.floor(left / 60) + " 分 " + (left % 60) + " 秒";
    if (left <= 0) clearInterval(state.approvalTimer);
  };
  tick();
  state.approvalTimer = setInterval(tick, 1000);
}
function hideApproval() {
  state.approvalShown = null;
  clearInterval(state.approvalTimer);
  $("#approval-mask").classList.remove("open");
}
async function decideApproval(id, decision) {
  try {
    await api("/api/approvals/" + id + "/decision", {
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
async function syncApprovals() {
  try {
    const list = await api("/api/approvals");
    const serverIds = new Set(list.map(p => p.id));
    for (const id of Object.keys(state.approvals)) {
      if (!serverIds.has(id)) {
        delete state.approvals[id];
        if (state.approvalShown === id) hideApproval();
      }
    }
    for (const p of list) onApprovalRequest(p);
    approvalBadge();
  } catch (e) {}
}

