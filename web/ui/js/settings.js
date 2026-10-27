/* ---------------- 配置 ---------------- */
let modelDraft = []; // 设置弹窗里的模型草稿(编辑中,保存时才提交)
let modelEditing = -1; // -1 = 块列表;>= 0 = 正在编辑的 modelDraft 下标
function modelTemplate() {
  // 全部留空(0):尺寸和采样参数都不猜 —— 空 = 不发给模型,由框架用它自己的默认值;
  // 上下文窗口为空时不做自动压缩,点"一键获取参数"从服务端读。
  return { name: "", provider: "vllm", base_url: "", api_key: "", model_name: "",
            max_tokens: 0, context_window: 0, temperature: 0, top_p: 0, top_k: 0,
            thinking: "", reasoning_effort: "" };
}
// DeepSeek 官方地址固定,UI 里选了 deepseek 就不用填 base_url。
const DS_BASE_URL = "https://api.deepseek.com";
// 支持思考强度的 provider。
const THINKING_PROVIDERS = new Set(["deepseek", "sglang"]);
// 统一的思考强度刻度:最左「默认」,之后 off / 低 / 中 / 高;滑到 off 即关闭思考。
// 各 provider 底层取值不同:deepseek 用 reasoning_effort(off/low/high/max);
// sglang(Qwen3 系)关闭思考靠 thinking=disabled,低/中/高对应 low/medium/xhigh。
const THINK_SCALE = {
  deepseek: [
    { label: "默认", thinking: "",         effort: "" },
    { label: "off",  thinking: "",         effort: "off" },
    { label: "低",   thinking: "",         effort: "low" },
    { label: "中",   thinking: "",         effort: "high" },
    { label: "高",   thinking: "",         effort: "max" },
  ],
  sglang: [
    { label: "默认", thinking: "",         effort: "" },
    { label: "off",  thinking: "disabled", effort: "" },
    { label: "低",   thinking: "",         effort: "low" },
    { label: "中",   thinking: "",         effort: "medium" },
    { label: "高",   thinking: "",         effort: "xhigh" },
  ],
};

// hostOf 把 base_url 收成"主机[:端口]/路径"短形,块卡片里够用
function hostOf(u) {
  if (!u) return "(未填 Base URL)";
  return String(u).replace(/^https?:\/\//, "").replace(/\/$/, "");
}

// renderModels 是设置弹窗里模型区的总入口:
// modelEditing = -1 → 块列表(点块进入编辑);>= 0 → 该模型的编辑面板。
function renderModels() {
  if (modelEditing < 0) renderModelGrid(); else renderModelEditor();
  paintDefaultModelSelect();
  const badge = $("#set-model-count"); // 左侧导航直接显示配了几个模型
  if (badge) badge.textContent = modelDraft.length;
}

function renderModelGrid() {
  const box = $("#model-list");
  $("#model-edit").innerHTML = "";
  $("#btn-add-model").style.display = "";
  box.innerHTML = "";
  const defName = $("#c-default-model").value;
  const val = (v) => (v === 0 || v === "0" || v === "" || v == null) ? "" : esc(v);
  modelDraft.forEach((m, i) => {
    const cell = (label, v, fb) =>
      '<span>' + label + ' <b class="' + (v ? "" : "auto") + '">' + (v || fb || "自动") + '</b></span>';
    const tile = document.createElement("div");
    tile.className = "model-tile";
    tile.title = "点击编辑该模型";
    tile.innerHTML =
      '<div class="mt-name">' + esc(m.name || "(未命名)") +
        '<span class="mt-badge">' + esc(m.provider || "?") + '</span>' +
        (m.name && m.name === defName ? '<span class="mt-badge mt-def">默认</span>' : '') +
      '</div>' +
      '<div class="mt-sub">' + esc(m.model_name || "(未填 Model Name)") + '</div>' +
      '<div class="mt-sub">' + esc(hostOf(m.base_url)) + '</div>' +
      '<div class="mt-stats">' +
        cell("上下文", val(m.context_window), "未设置") +
        cell("输出", val(m.max_tokens)) +
        cell("temp", val(m.temperature)) +
        cell("top_p", val(m.top_p)) +
        cell("top_k", val(m.top_k)) +
      '</div>' +
      '<div class="mt-go">编辑 ›</div>';
    tile.addEventListener("click", () => { modelEditing = i; renderModels(); });
    box.appendChild(tile);
  });
}

function renderModelEditor() {
  const i = modelEditing;
  const m = modelDraft[i];
  $("#model-list").innerHTML = "";
  $("#btn-add-model").style.display = "none";
  const card = document.createElement("div");
  card.className = "model-editor";
  const field = (k, label, ph, type) => {
    // 数字字段:0 就是"未设置",显示成空 —— 不把 0 当成一个填了的值。
    const v = m[k];
    const shown = v == null || (type === "number" && Number(v) === 0) ? "" : v;
    return '<div class="frow"><label>' + label + '</label><input type="' + (type || "text") + '" data-k="' + k +
      '" placeholder="' + (ph || "") + '" value="' + esc(shown) + '"></div>';
  };
  const selectField = (k, label, options) => {
    const cur = String(m[k] == null ? "" : m[k]);
    let opts = options;
    if (cur && !options.some(o => o[0] === cur)) opts = opts.concat([[cur, cur + " (自定义)"]]);
    const html = opts.map(o =>
      '<option value="' + esc(o[0]) + '"' + (cur === o[0] ? " selected" : "") + '>' + esc(o[1]) + '</option>').join("");
    return '<div class="frow"><label>' + label + '</label><select data-k="' + k + '">' + html + '</select></div>';
  };
  card.innerHTML =
    '<div class="me-head"><button class="btn me-back" data-act="back">← 模型列表</button>' +
      '<span class="me-title">' + esc(m.name || "新模型") + '</span>' +
      '<button class="btn danger me-rm" data-act="rm">删除该模型</button></div>' +
    '<div class="me-bar">' +
      '<button class="btn primary" data-act="probe">✨ 一键获取参数</button>' +
      '<span class="hint">能读到多少填多少:Model Name / 上下文窗口 / 最大输出 / temperature / top_p / top_k。读不到的保持留空 —— 留空即不发送,由框架用它自己的默认值</span>' +
    '</div>' +
    field("name", "显示名", "下拉框里显示的名字,唯一") +
    selectField("provider", "Provider", [
      ["vllm", "vllm"], ["sglang", "sglang"], ["deepseek", "deepseek (官方)"], ["ollama", "ollama"],
    ]) +
    field("base_url", "Base URL", "sglang 直连带 /v1;走代理则按代理要求") +
    field("api_key", "API Key") +
    '<div class="frow"><label>Model Name</label>' +
      '<input type="text" data-k="model_name" list="models-list-' + i + '" placeholder="服务端模型名" value="' + esc(m.model_name || "") + '">' +
      '<button class="mc-fetch" data-act="models" title="仅拉取模型列表">🔄</button></div>' +
    '<datalist id="models-list-' + i + '"></datalist>' +
     field("context_window", "Context Window", "留空/0 = 未知(不自动压缩),可一键获取", "number") +
     field("max_tokens", "Max Tokens", "留空/0 = 不发送,由框架定", "number") +
     field("temperature", "Temperature", "留空/0 = 不发送", "number") +
     field("top_p", "Top P", "留空/0 = 不发送", "number") +
     field("top_k", "Top K", "留空/0 = 不发送", "number") +

    '<div class="me-actions"><button class="btn primary" data-act="done">完成(返回模型列表)</button>' +
      '<button class="btn" data-act="default">设为默认</button>' +
      '<span class="spacer"></span><span class="hint">改完记得点最下面的"保存配置"</span></div>';
  card.querySelectorAll("input").forEach(inp => {
    inp.addEventListener("input", () => { m[inp.dataset.k] = inp.value; });
  });
  card.querySelectorAll("select").forEach(sel => {
    sel.addEventListener("change", () => {
      m[sel.dataset.k] = sel.value;
      if (sel.dataset.k === "provider") syncProvider(card, m);
    });
  });
  card.querySelector('[data-act="back"]').addEventListener("click", () => { modelEditing = -1; renderModels(); });
  card.querySelector('[data-act="done"]').addEventListener("click", () => {
    if (!(m.name || "").trim() || !(m.provider || "").trim() || !(m.base_url || "").trim() || !(m.model_name || "").trim()) {
      toast("显示名 / Provider / Base URL / Model Name 都要填", "err");
      return;
    }
    modelEditing = -1;
    renderModels();
  });
  card.querySelector('[data-act="default"]').addEventListener("click", () => {
    if (!(m.name || "").trim()) { toast("先填显示名", "err"); return; }
    m.name = m.name.trim();
    paintDefaultModelSelect();
    $("#c-default-model").value = m.name;
    toast("默认模型 = " + m.name, "ok");
  });
  card.querySelector('[data-act="rm"]').addEventListener("click", () => {
    if (modelDraft.length <= 1) { toast("至少要保留一个模型", "err"); return; }
    modelDraft.splice(i, 1);
    modelEditing = -1;
    renderModels();
  });
  card.querySelector('[data-act="models"]').addEventListener("click", () => fetchModels(card, i));
  card.querySelector('[data-act="probe"]').addEventListener("click", () => probeModel(card, i));
  syncProvider(card, m); // 初始同步:deepseek 固定地址 / 思考控件显隐 / effort 选项
  $("#model-edit").innerHTML = "";
  $("#model-edit").appendChild(card);
}

// probeModel 调后端 /api/llm/probe 问服务端要该模型的上下文窗口 / 最大输出 /
// 采样默认值,只填服务端真的报出来的字段;没报的保持原样(留空即不发送)。
async function probeModel(card, i) {
  const m = modelDraft[i];
  const baseUrl = (card.querySelector('input[data-k="base_url"]').value || "").trim();
  const apiKey = (card.querySelector("input[data-k=\"api_key\"]").value || "").trim();
  const btn = card.querySelector('[data-act="probe"]');
  if (!baseUrl) { toast("请先填写 Base URL", "err"); return; }
  const label = btn.textContent;
  btn.disabled = true; btn.textContent = "获取中…";
  const LABELS = { model_name: "Model Name", context_window: "上下文窗口", max_tokens: "最大输出",
                   temperature: "temperature", top_p: "top_p", top_k: "top_k" };
  try {
    const r = await fetch("/api/llm/probe", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ base_url: baseUrl, api_key: apiKey,
                             model_name: (m.model_name || "").trim(), provider: m.provider || "" }),
    });
    const d = await r.json();
    if (!r.ok) { toast("获取失败: " + (d.error || r.status), "err"); return; }
    // 模型列表 → datalist;Model Name 为空时顺带填上服务端报的那个
    const list = document.getElementById("models-list-" + i);
    const nameInput = card.querySelector('input[data-k="model_name"]');
    if (list && d.models && d.models.length) {
      list.innerHTML = "";
      for (const name of d.models) {
        const opt = document.createElement("option");
        opt.value = name;
        list.appendChild(opt);
      }
    }
    const filled = [], missed = [];
    if (d.model_name && !(nameInput.value || "").trim()) {
      nameInput.value = d.model_name; m.model_name = d.model_name; filled.push(LABELS.model_name);
    }
    for (const k of ["context_window", "max_tokens", "temperature", "top_p", "top_k"]) {
      const inp = card.querySelector('input[data-k="' + k + '"]');
      if (!inp) continue;
      if (d.found && d.found.indexOf(k) >= 0) {
        inp.value = d[k]; m[k] = d[k]; filled.push(LABELS[k]);
      } else {
        missed.push(LABELS[k]);
      }
    }
    if (filled.length) toast("已获取: " + filled.join("、"), "ok");
    if (missed.length) toast("服务端未报: " + missed.join("、") + " —— 留空即不发送", filled.length ? "ok" : "err");
    if (!filled.length && d.notes && d.notes.length) console.warn("probe notes:", d.notes);
  } catch (e) {
    toast("获取失败: " + e.message, "err");
  } finally {
    btn.disabled = false; btn.textContent = label;
  }
}

// syncProvider 按当前 provider 调整卡片:deepseek 的 base_url 固定为官方地址
// 且不可编辑。思考模式(thinking / reasoning_effort)不再在设置里配置,而是由
// 输入框旁的模型按钮双击调整(见 openThinkingPop);模型卡片仍保留配置里的
// 默认值并原样回存。
function syncProvider(card, m) {
  const baseUrlInput = card.querySelector('input[data-k="base_url"]');
  if (m.provider === "deepseek") {
    baseUrlInput.value = DS_BASE_URL;
    baseUrlInput.disabled = true;
    m.base_url = DS_BASE_URL;
  } else {
    baseUrlInput.disabled = false;
  }
}

// fetchModels 调用后端 /api/llm/models 拉取该 base_url 的模型列表,
// 填充 Model Name 的 datalist;只有一个模型时直接填入。
async function fetchModels(card, i) {
  const m = modelDraft[i];
  const baseUrl = (card.querySelector('input[data-k="base_url"]').value || "").trim();
  const apiKey = (card.querySelector('input[data-k="api_key"]').value || "").trim();
  const btn = card.querySelector(".mc-fetch");
  const list = document.getElementById("models-list-" + i);
  if (!baseUrl) { toast("请先填写 Base URL", "err"); return; }
  btn.disabled = true; btn.textContent = "…";
  try {
    const r = await fetch("/api/llm/models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ base_url: baseUrl, api_key: apiKey }),
    });
    const d = await r.json();
    if (!r.ok) { toast("获取模型失败: " + (d.error || r.status), "err"); return; }
    const models = d.models || [];
    list.innerHTML = "";
    for (const name of models) {
      const opt = document.createElement("option");
      opt.value = name;
      list.appendChild(opt);
    }
    const nameInput = card.querySelector('input[data-k="model_name"]');
    if (models.length === 1 && !(nameInput.value || "").trim()) {
      // Only auto-fill an EMPTY model_name; never clobber a working one
      // (e.g. a proxy may report "/model" while the real name is "Qwen3.8-27B-FP8").
      nameInput.value = models[0];
      m.model_name = models[0];
      toast("已填入模型: " + models[0], "ok");
    } else if (models.length === 0) {
      toast("未找到模型", "err");
    } else {
      toast("已加载 " + models.length + " 个模型,点击 Model Name 输入框选择", "ok");
    }
  } catch (e) {
    toast("获取模型失败: " + e.message, "err");
  } finally {
    btn.disabled = false; btn.textContent = "🔄";
  }
}
function paintDefaultModelSelect() {
  const sel = $("#c-default-model");
  const cur = sel.value;
  sel.innerHTML = modelDraft.map(m =>
    '<option value="' + esc(m.name || "") + '">' + esc(m.name || "(未命名)") + '</option>').join("");
  if (modelDraft.some(m => m.name === cur)) sel.value = cur;
}
/* ---------------- 配置弹窗:左侧分类导航 ----------------
   一级 = .set-nav-item(data-page 指向某个 .set-page),二级 = 右侧内容。
   只切 .active,不搬动任何控件,所以 openSettings/saveSettings 的读写照旧。 */
const LS_SETTAB_KEY = "mc-settab"; // 记住上次停在哪个分类,下次打开直接回到这里

function settingsPages() {
  return Array.prototype.slice.call(document.querySelectorAll("#settings-mask .set-page"));
}
function setPage(id) {
  const pages = settingsPages();
  if (!pages.some((p) => p.id === id)) return;
  pages.forEach((p) => p.classList.toggle("active", p.id === id));
  document.querySelectorAll("#settings-mask .set-nav-item").forEach(
    (b) => b.classList.toggle("active", b.dataset.page === id));
  try { localStorage.setItem(LS_SETTAB_KEY, id); } catch (e) {}
  const box = document.querySelector("#settings-mask .set-scroll");
  if (box) box.scrollTop = 0; // 换分类从顶部看起,别带着上一个分类的滚动位置
}
function currentSettingsPage() {
  try {
    const id = localStorage.getItem(LS_SETTAB_KEY);
    if (id && settingsPages().some((p) => p.id === id)) return id;
  } catch (e) {}
  return "page-appearance";
}
function closeSettings() { $("#settings-mask").classList.remove("open"); }
function initSettingsNav() {
  document.querySelectorAll("#settings-mask .set-nav-item").forEach(
    (b) => b.addEventListener("click", () => setPage(b.dataset.page)));
  $("#btn-settings-close").addEventListener("click", closeSettings);
  // 外观页的"去插件中心选/装动画"按钮:动画的选择和安装都住在插件中心。
  const gotoBoot = $("#a-boot-goto");
  if (gotoBoot) {
    gotoBoot.addEventListener("click", () => {
      setPage("page-plugins");
      if (typeof plShowTab === "function") plShowTab("installed");
    });
  }
  // 端口一改就把地址行刷新,别让人对着旧地址重启。
  const port = $("#w-port");
  if (port) port.addEventListener("input", paintWebURL);
  // Esc 关配置弹窗;但头像选择/审批这些叠在上面的弹窗优先(审批绝不能被 Esc 顺手关掉)
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    const mask = $("#settings-mask");
    if (!mask.classList.contains("open")) return;
    const upper = ["avatar-mask", "user-avatar-mask", "approval-mask"];
    for (let i = 0; i < upper.length; i++) {
      const el = document.getElementById(upper[i]);
      if (el && el.classList.contains("open")) return;
    }
    closeSettings();
  });
}

async function openSettings() {
  let cfg;
  try { cfg = await api("/api/config"); } catch (e) { toast("读取配置失败:" + e.message, "err"); return; }
  // 主题(纯客户端偏好,立即生效,不随"保存配置"提交)
  $("#c-theme").value = readThemeMode();
  // 用户头像以服务端配置为准(个人信息区同步显示)
  state.userAvatar = cfg.user_avatar || "";
  paintSettingsUserAvatar();
  // 模型列表草稿(服务端加载时已保证 models 非空)
  modelDraft = (cfg.models && cfg.models.length ? cfg.models : [cfg.model || {}]).map(m =>
    Object.assign(modelTemplate(), m));
  if (!modelDraft.length) modelDraft = [modelTemplate()];
  modelEditing = -1; // 打开设置总是先看块列表
  // 先把默认模型下拉填好并选定,块卡片才知道该给哪一块打"默认"标记
  paintDefaultModelSelect();
  $("#c-default-model").value = cfg.default_model || modelDraft[0].name || "";
  renderModels();
  $("#w-port").value = cfg.web_port || "";
  paintWebURL();
  // 开机动画的播放设置不再有表单,全部由 bootLoadLocal 读回来(含 play 段),
  // 再由插件中心的详情渲染;外观页只留一句摘要。
  bootLoadLocal(); // 不 await:目录里没有主题/权限有问题都不能挡住配置弹窗打开
  loadPluginCentre(); // 同上:插件列表读失败也不挡弹窗
  setPage(currentSettingsPage()); // 回到上次停着的分类
  $("#settings-mask").classList.add("open");
}

// paintWebURL 把"浏览器该访问哪个地址"直接写出来。
// 桌面端窗口本身就是用这个地址加载界面的(desktop/desktop.go),所以这里只让改
// 端口、不提供"关闭 Web 服务":关掉它等于让桌面窗口没有内容可加载。
function paintWebURL() {
  const el = $("#w-url-line");
  if (!el) return;
  const port = $("#w-port").value || "?";
  el.innerHTML = "当前地址 <code>http://127.0.0.1:" + esc(String(port)) + "</code>" +
    ' · 桌面窗口也是通过它加载界面';
}

// paintBootSummary 在外观页写一句开机动画的现状(选了哪个、装了几个)。
// 选择和播放设置都在插件中心那张卡的详情里,这里只回答"现在是什么状态"。
function paintBootSummary() {
  const el = $("#a-boot-summary");
  if (!el) return;
  if (typeof bootState === "undefined") { el.textContent = "—"; return; }
  const ok = (bootState.themes || []).filter((t) => !t.error);
  const play = (typeof bootPlaySettings === "function")
    ? bootPlaySettings() : { enabled: false, mode: "session" };
  const cur = ok.find((t) => t.id === bootState.picked);
  if (!ok.length) {
    el.textContent = "没有装 —— 去插件市场挑一个";
    return;
  }
  const modes = { session: "每个会话一次", always: "每次都播", daily: "每天一次" };
  el.textContent = (play.enabled ? "已启用" : "已关闭") +
    " · " + (cur ? cur.name || cur.id : "未选择") +
    (play.enabled ? " · " + (modes[play.mode] || play.mode) : "") +
    " · 已装 " + ok.length + " 个";
}
async function saveSettings() {
  // 空 / 非数字一律按 0 提交,0 在服务端表示"未设置" → 请求里不带该字段
  const num = (v) => (v === "" || v == null || isNaN(Number(v)) ? 0 : Number(v));
  const models = modelDraft.map(m => ({
    name: (m.name || "").trim(),
    provider: (m.provider || "").trim(),
    base_url: (m.base_url || "").trim(),
    api_key: (m.api_key || "").trim(),
    model_name: (m.model_name || "").trim(),
    max_tokens: num(m.max_tokens),
    context_window: num(m.context_window),
    temperature: num(m.temperature),
    top_p: num(m.top_p),
    top_k: num(m.top_k),
    thinking: (m.thinking || "").trim(),
    reasoning_effort: (m.reasoning_effort || "").trim(),
  }));
  for (const m of models) {
    if (!m.name || !m.provider || !m.base_url || !m.model_name) {
      toast("每个模型都要填:显示名 / Provider / Base URL / Model Name", "err");
      setPage("page-model");
      return;
    }
  }
  const names = models.map(m => m.name);
  if (new Set(names).size !== names.length) { toast("模型显示名不能重复", "err"); setPage("page-model"); return; }
  let defModel = $("#c-default-model").value;
  if (!names.includes(defModel)) defModel = names[0];
  // boot_anim 故意不在这份 body 里:它的开关由插件详情单独走 PUT /api/bootanim,
  // 而这个弹窗从来没有展示过那些设置。服务端对"零值 boot_anim"的处理是
  // 保留当前值,所以不传 = 不动它(见 web.handleSaveConfig)。
  const cfg = {
    web_port: num($("#w-port").value),
    models,
    default_model: defModel,
    user_avatar: state.userAvatar, // 透传用户头像,避免整份配置保存时丢失
  };
  try {
    const r = await api("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    });
    let msg = r.message || "已保存";
    if (r.notes && r.notes.length) msg += "(" + r.notes.join(";") + ")";
    toast(msg, "ok");
    $("#settings-mask").classList.remove("open");
    loadStatus();
  } catch (e) {
    toast("保存失败:" + e.message, "err");
  }
}

