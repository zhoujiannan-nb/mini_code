/* ---------------- 状态条 ---------------- */
async function loadStatus() {
  try {
    const s = await api("/api/status");
    $("#status-pill").innerHTML = "<b>" + esc(s.model_name || "未配置模型") + "</b> · 端口 " + s.web_port;
    // 模型列表 + 默认模型(下拉框数据源)
    state.models = s.models || [];
    state.defaultModel = s.default_model || (state.models[0] && state.models[0].name) || "";
    state.defaultCtxWindow = s.context_window || 0;
    // 当前选择的模型若已不存在(配置被改),回落默认
    if (state.model && !state.models.some(x => x.name === state.model)) state.model = state.defaultModel;
    if (!state.current) {
      state.ctxWindow = state.defaultCtxWindow;
      paintTokens(0);
    }
    paintModel();
  } catch (e) {
    $("#status-pill").textContent = "状态获取失败";
  }
}

