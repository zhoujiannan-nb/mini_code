// 审批弹窗(谨慎模式)。只是"视图":关掉它不会撤掉待审批请求,徽标上的数字还在。
import { h } from "preact";
import { useState, useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, hideApproval } from "../../lib/store.js";
import { decideApproval } from "../../lib/actions.js";

const html = htm.bind(h);

export function ApprovalModal() {
  const approvals = useStore((s) => s.approvals);
  const shownId = useStore((s) => s.approvalShown);
  const p = shownId ? approvals[shownId] : null;
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    if (!p) return undefined;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [p]);

  let sess = "";
  let args = "";
  let left = 0;
  if (p) {
    sess = p.title || "会话 " + (p.session_id || "").slice(0, 8);
    if (p.parent_id) {
      sess += " · 子任务(父:" + (p.parent_title || String(p.parent_id).slice(0, 8)) + ")";
    }
    try { args = JSON.stringify(JSON.parse(p.args || ""), null, 2); } catch (e) { args = p.args || ""; }
    const deadline = p.deadline ? new Date(p.deadline).getTime() : 0;
    left = deadline ? Math.max(0, Math.round((deadline - now) / 1000)) : 0;
  }

  return html`<div class=${"modal-mask" + (p ? " open" : "")} id="approval-mask"
    onClick=${(e) => { if (e.target.id === "approval-mask") hideApproval(); }}>
    <div class="modal approval-modal">
      <div class="ap-head">⚠️ 谨慎模式:危险操作审批</div>
      <div class="ap-sess" id="ap-sess">${p ? "来源:" + sess : ""}</div>
      <div class="ap-tool" id="ap-tool">${p
        ? html`<span class="ap-tname">${p.tool_name}</span> ${args}`
        : ""}</div>
      <div class="ap-deadline" id="ap-deadline">${p
        ? "超时自动拒绝 · 剩余 " + Math.floor(left / 60) + " 分 " + (left % 60) + " 秒"
        : ""}</div>
      <div class="ap-actions">
        <button class="btn danger" id="ap-deny"
          onClick=${() => { if (shownId) decideApproval(shownId, "deny"); }}>拒绝</button>
        <button class="btn primary" id="ap-approve"
          onClick=${() => { if (shownId) decideApproval(shownId, "approve"); }}>批准执行</button>
      </div>
    </div>
  </div>`;
}
