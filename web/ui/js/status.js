/* ---------------- 状态条 ---------------- */
// 顶部状态胶囊由 paintStatusPill 统一绘制:模型名跟着「当前会话」走。
// 以前这里是 loadStatus 直接 innerHTML 写死全局默认模型,切会话时没人重画它,
// 于是切到另一个模型的会话后顶部还挂着原来的模型名(看着就像"不跟着会话切")。
// 会话没设模型(空)时回落默认模型,与新会话视图一致。
let statusPort = 0;
function paintStatusPill() {
  const el = $("#status-pill");
  if (!el) return;
  const model = state.model || state.defaultModel || "未配置模型";
  el.innerHTML = "<b>" + esc(model) + "</b> · 端口 " + statusPort;
  el.title = "当前会话使用的模型 / 本地服务端口";
}
async function loadStatus() {
  try {
    const s = await api("/api/status");
    statusPort = s.web_port;
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

