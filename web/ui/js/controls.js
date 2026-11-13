/* ---------------- 权限模式切换 ---------------- */
function paintMode() {
  const b = $("#btn-mode");
  const cautious = state.mode === "cautious";
  b.textContent = (cautious ? "🛡 谨慎模式" : "⚡ 完整模式") + (state.current ? "" : "(新会话)");
  b.title = state.current
    ? "点击切换权限模式:完整=直接执行;谨慎=危险操作(删文件/危险命令)需审批,立即生效"
    : "新会话的权限模式:完整=直接执行;谨慎=危险操作需审批";
  b.classList.toggle("cautious", cautious);
}
async function toggleMode() {
  const next = state.mode === "cautious" ? "full" : "cautious";
  if (!state.current) {
    state.mode = next;
    paintMode();
    return;
  }
  try {
    const r = await api("/api/sessions/" + state.current + "/mode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode: next }),
    });
    state.mode = r.mode;
    paintMode();
    toast(next === "cautious" ? "已切换为谨慎模式:危险操作将请求审批" : "已切换为完整模式:直接执行", "ok");
  } catch (e) { toast("切换模式失败:" + e.message, "err"); }
}

/* ---------------- 上下文占用提醒(实验开关) ---------------- */
function paintContextReminders() {
  const cb = $("#ctx-reminders");
  const label = $("#ctx-toggle");
  if (!cb) return;
  cb.checked = state.contextReminders;
  label.classList.toggle("off", !state.contextReminders);
}
async function toggleContextReminders() {
  const next = $("#ctx-reminders").checked;
  state.contextReminders = next;
  paintContextReminders();
  if (!state.current) return; // 新会话:仅记录偏好,创建会话时随消息带上
  try {
    await api("/api/sessions/" + state.current + "/context-reminders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: next }),
    });
    toast(next ? "上下文占用提醒已开启" : "上下文占用提醒已关闭", "ok");
  } catch (e) {
    toast("切换失败:" + e.message, "err");
    state.contextReminders = !next;
    paintContextReminders();
  }
}

/* ---------------- 模型选择(composer 下拉) ---------------- */
function paintModel() {
  const m = state.models.find(x => x.name === state.model);
  $("#model-name").textContent = m ? m.name : (state.model || "默认");
  $("#btn-model").title = (state.current
    ? "当前会话模型:" + (m ? m.name : "默认") + "(切换后下次任务生效)"
    : "新会话使用的模型(下一条消息生效)") + " · 单击切换模型,双击调整思考强度";
  paintReasoning();
}
// 当前所选模型的 provider(用于判断是否支持思考强度)。
function currentProvider() {
  const m = state.models.find(x => x.name === state.model);
  return m ? m.provider : "";
}
// 当前模型是否支持思考强度(deepseek / sglang / vllm)。
function thinkingSupported() { return THINKING_PROVIDERS.has(currentProvider()); }
// 当前 provider 的思考强度刻度(不支持则 null)。
function thinkScale() { return THINK_SCALE[currentProvider()] || null; }
// 当前 (thinking, effort) 在刻度上的下标;匹配不到则回「默认」(0)。
function thinkIndex() {
  const scale = thinkScale();
  if (!scale) return 0;
  const i = scale.findIndex(s => s.thinking === (state.thinking || "") && s.effort === (state.reasoningEffort || ""));
  return i < 0 ? 0 : i;
}
// 思考强度的人类可读标签(画在模型按钮的徽标上)。
function reasoningLabel() {
  const scale = thinkScale();
  if (!scale) return "";
  return "思考:" + scale[thinkIndex()].label;
}
// paintReasoning 刷新模型按钮上的思考徽标。
function paintReasoning() {
  const badge = $("#think-badge");
  if (!badge) return;
  if (!thinkingSupported()) { badge.hidden = true; return; }
  badge.hidden = false;
  badge.textContent = reasoningLabel();
}
// updateThinkFill 把滑轨已选部分染成主色(--tp-pct 驱动渐变),拖动时更顺滑。
function updateThinkFill() {
  const s = $("#tp-effort");
  if (!s) return;
  const max = parseInt(s.max, 10) || 1;
  const pct = Math.max(0, Math.min(100, (parseInt(s.value, 10) || 0) / max * 100));
  s.style.setProperty("--tp-pct", pct + "%");
}
// openThinkingPop 打开思考强度浮层(双击模型按钮):最左默认→off→低→中→高。
function openThinkingPop() {
  closeModelMenu();
  const pop = $("#think-pop");
  const prov = currentProvider();
  const m = state.models.find(x => x.name === state.model);
  $("#tp-model").textContent = (m ? m.name : (state.model || "默认")) + (prov ? " · " + prov : "");
  const scale = thinkScale();
  if (!scale) {
    $("#tp-body").style.display = "none";
    $("#tp-note").textContent = "当前模型(" + (prov || "未知 provider") + ")不支持思考强度调整,仅 deepseek / sglang / vllm 支持。";
    pop.classList.add("open");
    return;
  }
  $("#tp-body").style.display = "";
  $("#tp-note").textContent = state.current ? "调整后从下次任务生效。" : "新会话将按此思考强度创建。";
  const slider = $("#tp-effort");
  slider.max = String(scale.length - 1);
  slider.value = String(thinkIndex());
  $("#tp-scale").innerHTML = scale.map(s => "<span>" + esc(s.label) + "</span>").join("");
  updateThinkFill();
  pop.classList.add("open");
}
function closeThinkingPop() { const p = $("#think-pop"); if (p) p.classList.remove("open"); }
// persistReasoning 把思考强度写到当前会话(新会话只存本地,创建时随消息带上)。
async function persistReasoning() {
  if (!state.current) return;
  try {
    await api("/api/sessions/" + state.current + "/reasoning", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ thinking: state.thinking || "", reasoning_effort: state.reasoningEffort || "" }),
    });
    toast("思考强度已更新(下次任务生效)", "ok");
  } catch (e) {
    toast("更新思考强度失败:" + e.message, "err");
  }
}
// syncReasoningToModel 在切换模型后校正思考覆盖:新 provider 不支持思考则清空,
// 当前刻度对新 provider 不存在也回默认;已有会话同步到服务端。
function syncReasoningToModel() {
  const scale = thinkScale();
  let changed = false;
  if (!scale) {
    if (state.thinking || state.reasoningEffort) { state.thinking = ""; state.reasoningEffort = ""; changed = true; }
  } else if (!scale.some(s => s.thinking === (state.thinking || "") && s.effort === (state.reasoningEffort || ""))) {
    state.thinking = ""; state.reasoningEffort = ""; changed = true;
  }
  paintReasoning();
  if (changed && state.current) persistReasoning();
}
function toggleModelMenu() {
  const menu = $("#model-menu");
  if (menu.classList.contains("open")) { closeModelMenu(); return; }
  menu.innerHTML = state.models.map(m =>
    '<div class="mm-item' + (m.name === state.model ? " cur" : "") + '" data-name="' + esc(m.name) + '">' +
    esc(m.name) +
    (m.name === state.defaultModel ? '<span class="mm-sub">默认</span>' : '<span class="mm-sub">' + fmtK(m.context_window) + '</span>') +
    '</div>').join("");
  if (!state.models.length) {
    menu.innerHTML = '<div class="mm-item" style="cursor:default;color:var(--text3)">未配置模型(设置里添加)</div>';
  }
  menu.querySelectorAll(".mm-item[data-name]").forEach(el => {
    el.addEventListener("click", () => pickModel(el.dataset.name));
  });
  menu.classList.add("open");
}
function closeModelMenu() { $("#model-menu").classList.remove("open"); }
async function pickModel(name) {
  closeModelMenu();
  if (name === state.model) return;
  const prev = state.model;
  state.model = name;
  paintModel();
  syncReasoningToModel();
  if (!state.current) {
    // 新会话:token 条的窗口跟着所选模型走
    const m = state.models.find(x => x.name === name);
    if (m) state.ctxWindow = m.context_window || 0;
    paintTokens(0);
    toast("新会话将使用模型 " + name, "");
    return;
  }
  try {
    const r = await api("/api/sessions/" + state.current + "/model", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: name }),
    });
    if (r.context_window) state.ctxWindow = r.context_window;
    paintTokens(0);
    updateTokenUsage();
    toast("已切换模型 " + name + ",下次任务生效", "ok");
  } catch (e) {
    state.model = prev;
    paintModel();
    toast("切换模型失败:" + e.message, "err");
  }
}

