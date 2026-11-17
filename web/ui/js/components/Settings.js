import { h } from "preact";
import { useState, useEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState, toast, getState } from "../lib/store.js";
import { loadStatus } from "../lib/actions.js";
import {
  loadConfig, saveConfig, probeModel as probeModelRequest, fetchModels as fetchModelsRequest,
  closeSettingsPanel, setSettingsPage, loadPluginCentre, bootLoadLocal,
} from "../lib/settings.js";
import { readThemeMode, setThemeMode } from "../lib/theme.js";
import { UserAvatar, AgentAvatar, fileToAvatarDataURL, validateAvatarFile } from "../lib/avatars.js";
import { saveUserAvatar, saveAgentAvatar } from "../lib/actions.js";
import { BootSummary } from "./BootAnim.js";
import { PluginCentre } from "./Plugins.js";

const html = htm.bind(h);

function setLS(key, value) {
  try { localStorage.setItem(key, value); } catch (e) {}
}

export const DS_BASE_URL = "https://api.deepseek.com";

export function modelTemplate() {
  return { name: "", provider: "vllm", base_url: "", api_key: "", model_name: "",
           max_tokens: 0, context_window: 0, temperature: 0, top_p: 0, top_k: 0,
           thinking: "", reasoning_effort: "" };
}

function hostOf(u) {
  if (!u) return "(未填 Base URL)";
  return String(u).replace(/^https?:\/\//, "").replace(/\/$/, "");
}

function numOf(v) {
  return (v === "" || v == null || isNaN(Number(v)) ? 0 : Number(v));
}

export function renderModelGrid({ models, defaultModel, onOpen }) {
  const cell = (label, v, fb) =>
    html`<span>${label} <b class=${v ? "" : "auto"}>${v || fb || "自动"}</b></span>`;
  return models.map((m, i) => html`<div class="model-tile" key=${i} title="点击编辑该模型"
    onClick=${() => onOpen(i)}>
    <div class="mt-name">${m.name || "(未命名)"}
      <span class="mt-badge">${m.provider || "?"}</span>
      ${m.name && m.name === defaultModel ? html`<span class="mt-badge mt-def">默认</span>` : null}
    </div>
    <div class="mt-sub">${m.model_name || "(未填 Model Name)"}</div>
    <div class="mt-sub">${hostOf(m.base_url)}</div>
    <div class="mt-stats">
      ${cell("上下文", numOf(m.context_window), "未设置")}
      ${cell("输出", numOf(m.max_tokens))}
      ${cell("temp", numOf(m.temperature))}
      ${cell("top_p", numOf(m.top_p))}
      ${cell("top_k", numOf(m.top_k))}
    </div>
    <div class="mt-go">编辑 ›</div>
  </div>`);
}

async function probeModel(body) {
  return probeModelRequest(body);
}

export function renderModelEditor({ model, index, onChange, onBack, onDone, onRemove, onDefault, onProbe, onFetchModels, datalistId, datalist }) {
  const field = (k, label, ph, type) => {
    const v = model[k];
    const shown = v == null || (type === "number" && Number(v) === 0) ? "" : v;
    return html`<div class="frow"><label>${label}</label>
      <input type=${type || "text"} data-k=${k} placeholder=${ph || ""} value=${shown}
        onInput=${(e) => onChange(k, e.target.value)} /></div>`;
  };
  const selectField = (k, label, options) => {
    const cur = String(model[k] == null ? "" : model[k]);
    let opts = options;
    if (cur && !options.some((o) => o[0] === cur)) opts = opts.concat([[cur, cur + " (自定义)"]]);
    return html`<div class="frow"><label>${label}</label>
      <select data-k=${k} value=${cur} onChange=${(e) => onChange(k, e.target.value)}>
        ${opts.map((o) => html`<option value=${o[0]} key=${o[0]}>${o[1]}</option>`)}
      </select></div>`;
  };
  return html`<div class="model-editor">
    <div class="me-head">
      <button class="btn me-back" data-act="back" onClick=${onBack}>← 模型列表</button>
      <span class="me-title">${model.name || "新模型"}</span>
      <button class="btn danger me-rm" data-act="rm" onClick=${onRemove}>删除该模型</button>
    </div>
    <div class="me-bar">
      <button class="btn primary" data-act="probe" onClick=${onProbe}>✨ 一键获取参数</button>
      <span class="hint">能读到多少填多少:Model Name / 上下文窗口 / 最大输出 / temperature / top_p / top_k。读不到的保持留空 —— 留空即不发送,由框架用它自己的默认值</span>
    </div>
    ${field("name", "显示名", "下拉框里显示的名字,唯一")}
    ${selectField("provider", "Provider", [
      ["vllm", "vllm"], ["sglang", "sglang"], ["deepseek", "deepseek (官方)"], ["ollama", "ollama"],
    ])}
    ${field("base_url", "Base URL", "sglang 直连带 /v1;走代理则按代理要求", "text")}
    ${field("api_key", "API Key", "", "text")}
    <div class="frow"><label>Model Name</label>
      <input type="text" data-k="model_name" list=${datalistId} placeholder="服务端模型名"
        value=${model.model_name || ""} onInput=${(e) => onChange("model_name", e.target.value)} />
      <button class="mc-fetch" data-act="models" title="仅拉取模型列表" onClick=${onFetchModels}>🔄</button>
    </div>
    <datalist id=${datalistId}>
      ${(datalist || []).map((n) => html`<option value=${n} key=${n} />`)}
    </datalist>
    ${field("context_window", "Context Window", "留空/0 = 未知(不自动压缩),可一键获取", "number")}
    ${field("max_tokens", "Max Tokens", "留空/0 = 不发送,由框架定", "number")}
    ${field("temperature", "Temperature", "留空/0 = 不发送", "number")}
    ${field("top_p", "Top P", "留空/0 = 不发送", "number")}
    ${field("top_k", "Top K", "留空/0 = 不发送", "number")}
    <div class="me-actions">
      <button class="btn primary" data-act="done" onClick=${onDone}>完成(返回模型列表)</button>
      <button class="btn" data-act="default" onClick=${onDefault}>设为默认</button>
      <span class="spacer"></span><span class="hint">改完记得点最下面的"保存配置"</span>
    </div>
  </div>`;
}

export function Settings() {
  const open = useStore((s) => s.settingsOpen);
  const page = useStore((s) => s.settingsPage);
  const themeMode = useStore((s) => s.themeMode);
  const sendMode = useStore((s) => s.sendMode);
  const inputTip = useStore((s) => s.inputTip);
  const userAva = useStore((s) => s.userAvatar);
  const agentAva = useStore((s) => s.agentAvatar);

  const [models, setModels] = useState([]);
  const [defaultModel, setDefaultModel] = useState("");
  const [webPort, setWebPort] = useState("");
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(-1);
  const [datalist, setDatalist] = useState([]);
  const agentFileRef = useRef(null);

  useEffect(() => {
    if (!open) return;
    setError("");
    loadConfig().then((cfg) => {
      const list = (cfg.models && cfg.models.length ? cfg.models : [cfg.model || {}]).map((m) => Object.assign(modelTemplate(), m));
      setModels(list);
      setDefaultModel(cfg.default_model || (list[0] && list[0].name) || "");
      setWebPort(cfg.web_port || "");
      setState({ userAvatar: cfg.user_avatar || "", agentAvatar: cfg.agent_avatar || "" });
      setEditing(-1);
      bootLoadLocal();
      loadPluginCentre();
    }).catch((e) => setError("读取配置失败:" + e.message));
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const onKey = (e) => {
      if (e.key !== "Escape") return;
      const st = getState();
      if (st.avatarOpen || st.approvalShown) return;
      closeSettingsPanel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  const patchModel = (i, k, v) => {
    setModels((list) => list.map((m, idx) => idx === i ? Object.assign({}, m, { [k]: v }) : m));
  };

  const onDoneEditing = () => {
    const m = models[editing];
    if (!m) return;
    if (!(m.name || "").trim() || !(m.provider || "").trim() || !(m.base_url || "").trim() || !(m.model_name || "").trim()) {
      toast("显示名 / Provider / Base URL / Model Name 都要填", "err");
      return;
    }
    setEditing(-1);
  };

  const onAdd = () => {
    const next = models.concat([modelTemplate()]);
    setModels(next);
    setEditing(next.length - 1);
  };

  const doProbe = async () => {
    const m = models[editing];
    if (!m) return;
    if (!(m.base_url || "").trim()) { toast("请先填写 Base URL", "err"); return; }
    try {
      const d = await probeModel({
        base_url: m.base_url.trim(),
        api_key: (m.api_key || "").trim(),
        model_name: (m.model_name || "").trim(),
        provider: m.provider || "",
      });
      if (d.models && d.models.length) setDatalist(d.models);
      const filled = [], missed = [];
      const LABELS = { model_name: "Model Name", context_window: "上下文窗口", max_tokens: "最大输出", temperature: "temperature", top_p: "top_p", top_k: "top_k" };
      const patch = {};
      if (d.model_name && !(m.model_name || "").trim()) { patch.model_name = d.model_name; filled.push(LABELS.model_name); }
      for (const k of ["context_window", "max_tokens", "temperature", "top_p", "top_k"]) {
        if (d.found && d.found.indexOf(k) >= 0) { patch[k] = d[k]; filled.push(LABELS[k]); }
        else missed.push(LABELS[k]);
      }
      setModels((list) => list.map((x, idx) => idx === editing ? Object.assign({}, x, patch) : x));
      if (filled.length) toast("已获取: " + filled.join("、"), "ok");
      if (missed.length) toast("服务端未报: " + missed.join("、") + " —— 留空即不发送", filled.length ? "ok" : "err");
      if (!filled.length && d.notes && d.notes.length) console.warn("probe notes:", d.notes);
    } catch (e) {
      toast("获取失败: " + e.message, "err");
    }
  };

  const doFetchModels = async () => {
    const m = models[editing];
    if (!m) return;
    if (!(m.base_url || "").trim()) { toast("请先填写 Base URL", "err"); return; }
    try {
      const d = await fetchModelsRequest(m.base_url.trim(), (m.api_key || "").trim());
      if (!d.models) { toast("获取模型失败", "err"); return; }
      setDatalist(d.models || []);
      if ((d.models || []).length === 1 && !(m.model_name || "").trim()) {
        patchModel(editing, "model_name", d.models[0]);
        toast("已填入模型: " + d.models[0], "ok");
      } else if ((d.models || []).length === 0) {
        toast("未找到模型", "err");
      } else {
        toast("已加载 " + d.models.length + " 个模型,点击 Model Name 输入框选择", "ok");
      }
    } catch (e) {
      toast("获取模型失败: " + e.message, "err");
    }
  };

  const onSave = async () => {
    const clean = models.map((m) => ({
      name: (m.name || "").trim(),
      provider: (m.provider || "").trim(),
      base_url: (m.base_url || "").trim(),
      api_key: (m.api_key || "").trim(),
      model_name: (m.model_name || "").trim(),
      max_tokens: numOf(m.max_tokens),
      context_window: numOf(m.context_window),
      temperature: numOf(m.temperature),
      top_p: numOf(m.top_p),
      top_k: numOf(m.top_k),
      thinking: (m.thinking || "").trim(),
      reasoning_effort: (m.reasoning_effort || "").trim(),
    }));
    for (const m of clean) {
      if (!m.name || !m.provider || !m.base_url || !m.model_name) {
        toast("每个模型都要填:显示名 / Provider / Base URL / Model Name", "err");
        setSettingsPage("page-model");
        return;
      }
    }
    const names = clean.map((m) => m.name);
    if (new Set(names).size !== names.length) { toast("模型显示名不能重复", "err"); setSettingsPage("page-model"); return; }
    let defModel = defaultModel;
    if (!names.includes(defModel)) defModel = names[0];
    const st = getState();
    const cfg = {
      web_port: numOf(webPort),
      models: clean,
      default_model: defModel,
      user_avatar: st.userAvatar, 
      agent_avatar: st.agentAvatar, 
    };
    try {
      const r = await saveConfig(cfg);
      let msg = r.message || "已保存";
      if (r.notes && r.notes.length) msg += "(" + r.notes.join(";") + ")";
      toast(msg, "ok");
      closeSettingsPanel();
      loadStatus();
    } catch (e) {
      toast("保存失败:" + e.message, "err");
    }
  };

  const onAgentFile = async (e) => {
    const f = (e.target.files || [])[0];
    e.target.value = "";
    const bad = validateAvatarFile(f);
    if (bad) { if (f) toast(bad, "err"); return; }
    try {
      await saveAgentAvatar(await fileToAvatarDataURL(f));
      toast("agent 头像已更新", "ok");
    } catch (err) { toast("保存失败:" + err.message, "err"); }
  };

  const pages = [
    ["page-appearance", "🎨", "外观与启动"],
    ["page-model", "🧠", "模型"],
    ["page-plugins", "🧩", "插件中心"],
  ];
  const theme = themeMode || readThemeMode();
  const modelDraft = models[editing] || null;

  return html`<div class=${"modal-mask" + (open ? " open" : "")} id="settings-mask"
    onClick=${(e) => { if (e.target.id === "settings-mask") closeSettingsPanel(); }}>
    <div class="modal settings-modal">
      <div class="set-head">
        <h2>配置</h2>
        <button class="set-close" id="btn-settings-close" title="关闭(Esc)" aria-label="关闭"
          onClick=${closeSettingsPanel}>✕</button>
      </div>
      <div class="set-body">
        <nav class="set-nav" id="set-nav">
          ${pages.map(([id, icon, label]) => html`<button class=${"set-nav-item" + (page === id ? " active" : "")}
            data-page=${id} key=${id} onClick=${() => setSettingsPage(id)}>
            <span class="ni">${icon}</span>${label}
            ${id === "page-model" ? html`<span class="set-nav-count" id="set-model-count">${models.length}</span>` : null}
          </button>`)}
        </nav>
        <div class="set-main">
          <div class="set-scroll">
            ${error ? html`<div class="pl-err">${error}</div>` : null}
            <section class=${"set-page" + (page === "page-appearance" ? " active" : "")} id="page-appearance">
              <h3>外观与启动</h3>
              <div class="pl-sec">
                <div class="pl-sec-title">主题</div>
                <div class="frow hinted"><label>主题</label>
                  <select id="c-theme" value=${theme} onChange=${(e) => {
                    setThemeMode(e.target.value);
                    setState({ themeMode: e.target.value });
                    toast(e.target.value === "light" ? "已切换为浅色主题" : e.target.value === "dark" ? "已切换为深色主题" : "已切换为跟随系统主题", "ok");
                  }}>
                    <option value="system">跟随系统</option>
                    <option value="light">浅色</option>
                    <option value="dark">深色</option>
                  </select>
                  <span class="hint">立即生效,不用保存</span>
                </div>
                <div class="frow hinted"><label>用户头像</label>
                  <div class="u-flex u-gap10">
                    <span id="c-user-ava"><${UserAvatar} cls="c-avatar" /></span>
                    <button class="mode-btn" id="btn-user-ava" onClick=${() => setState({ avatarOpen: true })}>上传 / 更换头像</button>
                  </div>
                  <span class="hint">jpg / png / webp,≤4MB,裁成方形</span>
                </div>
                <div class="frow hinted"><label>agent 头像</label>
                  <div class="u-flex u-gap10">
                    <span id="c-agent-ava"><${AgentAvatar} cls="c-avatar" /></span>
                    <button class="mode-btn" id="btn-agent-ava" onClick=${() => agentFileRef.current && agentFileRef.current.click()}>上传 / 更换头像</button>
                    <button class="mode-btn" id="btn-agent-ava-clear" onClick=${async () => {
                      if (!agentAva) { toast("当前已经是默认头像", ""); return; }
                      try { await saveAgentAvatar(""); toast("已恢复默认头像", "ok"); }
                      catch (e) { toast("恢复失败:" + e.message, "err"); }
                    }}>恢复默认</button>
                  </div>
                  <span class="hint">jpg / png / webp,≤4MB,裁成方形;默认 = 墨色 + M</span>
                  <input type="file" id="agent-ava-file" accept="image/jpeg,image/png,image/webp" hidden
                    ref=${agentFileRef} onChange=${onAgentFile} />
                </div>
              </div>
              <div class="pl-sec">
                <div class="pl-sec-title">输入</div>
                <div class="frow hinted"><label>发送快捷键</label>
                  <select id="c-send-mode" value=${sendMode} onChange=${(e) => {
                    setLS("mc-send-mode", e.target.value);
                    setState({ sendMode: e.target.value });
                    toast(e.target.value === "shift" ? "已切换: Shift+Enter 发送, Enter 换行" : "已切换: Enter 发送, Shift+Enter 换行", "ok");
                  }}>
                    <option value="enter">Enter 发送,Shift+Enter 换行</option>
                    <option value="shift">Shift+Enter 发送,Enter 换行</option>
                  </select>
                  <span class="hint">立即生效,不用保存</span>
                </div>
                <div class="frow hinted"><label>输入框提示语</label>
                  <input type="text" id="c-input-tip" value=${inputTip} maxlength="60"
                    placeholder="留空 = 默认「输入任务…」"
                    onInput=${(e) => { setLS("mc-input-tip", e.target.value); setState({ inputTip: e.target.value }); }} />
                  <span class="hint">输入框里的占位文案,清空恢复默认</span>
                </div>
              </div>
              <div class="pl-sec">
                <div class="pl-sec-title">启动</div>
                <div class="frow hinted"><label>Web 端口</label>
                  <input type="number" id="w-port" value=${webPort} onInput=${(e) => setWebPort(e.target.value)} />
                  <span class="hint">改完需重启</span>
                </div>
                <div id="w-url-line" class="pl-url-line">当前地址
                  <code>${"http://127.0.0.1:" + (webPort || "?")}</code> · 桌面窗口也是通过它加载界面
                </div>
              </div>
              <div class="pl-sec">
                <div class="pl-sec-title">开机动画</div>
                <div class="frow"><label>当前</label><${BootSummary} /></div>
                <button class="mode-btn" id="a-boot-goto" onClick=${() => {
                  setSettingsPage("page-plugins");
                  setState((s) => ({ ...s, plugins: { ...s.plugins, tab: "installed" } }));
                }}>去插件中心设置 →</button>
              </div>
            </section>

            <section class=${"set-page" + (page === "page-model" ? " active" : "")} id="page-model">
              <h3>模型</h3>
              <p class="set-desc">可配多个,会话里下拉切换;点卡片进入编辑,新增/删除都要点「保存配置」才生效。</p>
              <div id="model-list" class="model-grid">
                ${editing < 0 ? html`<${renderModelGrid} models=${models} defaultModel=${defaultModel} onOpen=${setEditing} />` : null}
              </div>
              <button class="add-model-btn" id="btn-add-model" style=${editing >= 0 ? "display:none" : ""}
                onClick=${onAdd}>＋ 新增模型</button>
              <div id="model-edit">
                ${editing >= 0 && modelDraft ? html`<${renderModelEditor}
                  model=${modelDraft} index=${editing} datalistId=${"models-list-" + editing}
                  datalist=${datalist}
                  onChange=${(k, v) => patchModel(editing, k, v)}
                  onBack=${() => setEditing(-1)}
                  onDone=${onDoneEditing}
                  onRemove=${() => {
                    if (models.length <= 1) { toast("至少要保留一个模型", "err"); return; }
                    setModels((list) => list.filter((_, i) => i !== editing));
                    setEditing(-1);
                  }}
                  onDefault=${() => {
                    const m = models[editing];
                    if (!m || !(m.name || "").trim()) { toast("先填显示名", "err"); return; }
                    setDefaultModel(m.name.trim());
                    toast("默认模型 = " + m.name.trim(), "ok");
                  }}
                  onProbe=${doProbe}
                  onFetchModels=${doFetchModels} />` : null}
              </div>
              <div class="frow"><label>默认模型</label>
                <select id="c-default-model" value=${defaultModel} onChange=${(e) => setDefaultModel(e.target.value)}>
                  ${models.map((m) => html`<option value=${m.name || ""} key=${m.name || Math.random()}>${m.name || "(未命名)"}</option>`)}
                </select>
              </div>
            </section>

            <section class=${"set-page" + (page === "page-plugins" ? " active" : "")} id="page-plugins">
              <h3>插件中心</h3>
              ${page === "page-plugins" ? html`<${PluginCentre} />` : null}
            </section>
          </div>
          <div class="modal-actions">
            <button class="btn" id="btn-cancel" onClick=${closeSettingsPanel}>取消</button>
            <button class="btn primary" id="btn-save" onClick=${onSave}>保存配置</button>
          </div>
        </div>
      </div>
    </div>
  </div>`;
}
