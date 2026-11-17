import { api, ROUTES } from "./api.js";
import { getState, setState, toast } from "./store.js";
import { resetToNew } from "./actions.js";

const LS_SETTAB_KEY = "mc-settab";

export function restoreSettingsPage() {
  try {
    const id = localStorage.getItem(LS_SETTAB_KEY);
    if (id) setState({ settingsPage: id });
  } catch (e) {}
}

export function openSettings() {
  restoreSettingsPage();
  setState({ settingsOpen: true });
}

export function closeSettingsPanel() {
  setState({ settingsOpen: false });
}

export function setSettingsPage(id) {
  setState({ settingsPage: id });
  try { localStorage.setItem(LS_SETTAB_KEY, id); } catch (e) {}
}

export function loadConfig() {
  return api(ROUTES.config);
}

export function saveConfig(cfg) {
  return api(ROUTES.config, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(cfg),
  });
}

export function probeModel(body) {
  return api(ROUTES.llmProbe, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function fetchModels(baseUrl, apiKey) {
  return api(ROUTES.llmModels, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ base_url: baseUrl, api_key: apiKey }),
  });
}

export function bootPlaySettings() {
  const p = getState().boot.play || {};
  return {
    enabled: !!p.enabled,
    mode: p.mode || "session",
    mute: !!p.mute,
    theme: p.theme || "",
  };
}

export async function bootLoadLocal(msg) {
  try {
    const r = await api(ROUTES.bootanim);
    const boot = {
      dir: r.dir || "",
      legacyDir: r.legacy_dir || "",
      themes: r.themes || [],
      play: r.play || null,
      picked: getState().boot.picked || "",
    };
    if (boot.play && boot.play.theme !== undefined) boot.picked = boot.play.theme || boot.picked;
    if (boot.picked && !boot.themes.some((t) => t.id === boot.picked && !t.error)) boot.picked = "";
    if (!boot.picked) {
      const first = boot.themes.find((t) => !t.error);
      if (first) boot.picked = first.id;
    }
    setState({ boot });
    if (msg) toast(msg, "ok");
  } catch (e) {
    setState({ boot: { dir: "", legacyDir: "", themes: [], play: null, picked: "" } });
    toast("读取开机动画失败:" + e.message, "err");
  }
}

export async function bootSavePlay(patch, msg) {
  try {
    const r = await api(ROUTES.bootanim, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch),
    });
    if (r && r.play) setState((s) => ({ ...s, boot: { ...s.boot, play: r.play } }));
    if (msg) toast(msg, "ok");
  } catch (e) {
    toast("保存失败:" + e.message, "err");
  }
  await bootLoadLocal();
}

export async function bootPick(id) {
  setState((s) => ({ ...s, boot: { ...s.boot, picked: id } }));
  await bootSavePlay({ theme: id, enabled: true }, "已设为开机动画");
}
export function bootSetEnabled(on) { return bootSavePlay({ enabled: !!on }, on ? "已启用" : "已关闭"); }
export function bootSetMode(mode) { return bootSavePlay({ mode }, "播放时机已更新"); }
export function bootSetMute(on) { return bootSavePlay({ mute: !!on }, on ? "已静音" : "已开启声音"); }

export function bootPlay(t) {
  const player = window.__mcBoot;
  if (!player || !t || t.error) return false;
  const play = bootPlaySettings();
  const ok = player.play({
    entry: "/bootanim/" + t.id + "/" + (t.entry || "index.html"),
    mute: play.mute,
    interactive: false,
    durationMs: t.durationMs,
    maxMs: t.maxMs,
    minViewport: t.minViewport,
  });
  if (!ok) toast("这个动画播不了(入口文件缺失)", "err");
  return ok;
}
export function bootPreview(id) {
  bootPlay(getState().boot.themes.find((t) => t.id === id));
}

export const PLUGIN_CATEGORIES = [
  { key: "process", icon: "⚙️", title: "程序", hint: "mini_code 启动并保活的外部进程" },
  { key: "tool", icon: "🔧", title: "工具", hint: "追加给模型的工具" },
  { key: "appearance", icon: "🎨", title: "外观", hint: "皮肤 / 壁纸 / 插槽 / 动画 / 开机画面" },
];

export function plCatMeta(cat) {
  return PLUGIN_CATEGORIES.find((c) => c.key === cat) ||
    { key: cat || "appearance", icon: "🧩", title: cat || "其他", hint: "" };
}

export function pluginState() { return getState().plugins; }
function patchPlugins(patch) {
  setState((s) => ({ ...s, plugins: { ...s.plugins, ...patch } }));
}

export async function loadPluginCentre(msg) {
  try {
    const r = await api(ROUTES.plugins);
    patchPlugins({ list: r.plugins || [], dir: r.dir || getState().plugins.dir, slots: r.slots || [] });
    await bootLoadLocal();
    if (msg) toast(msg, "ok");
  } catch (e) {
    patchPlugins({ list: [], sel: "" });
    toast("读取插件列表失败:" + e.message, "err");
  }
}

export async function plFetchCatalog() {
  try {
    const r = await api(ROUTES.pluginCatalog);
    patchPlugins({
      repo: r.repo || getState().plugins.repo,
      catalog: r.entries || [],
      catalogMsg: (r.entries || []).length ? "" : "插件仓库里还没有可下载的插件",
    });
  } catch (e) {
    patchPlugins({ catalog: [], catalogMsg: "读取失败:" + e.message });
  }
}

export async function plToggle(id, on) {
  try {
    await api(ROUTES.pluginToggle(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: on }),
    });
    await loadPluginCentre(on ? "已启用,刷新页面后外观全生效" : "已停用");
  } catch (e) {
    toast("操作失败:" + e.message, "err");
    await loadPluginCentre();
  }
}

export async function plProcess(id, action) {
  patchPlugins({ busy: id });
  try {
    const r = await api(ROUTES.pluginProcess(id), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action }),
    });
    toast(r.message || "完成", "ok");
  } catch (e) {
    toast("操作失败:" + e.message, "err");
  } finally {
    patchPlugins({ busy: null });
    await loadPluginCentre();
  }
}

export async function plLogs(id) {
  try {
    const r = await api(ROUTES.pluginLogs(id, 80));
    setState({ pluginLogs: { id, lines: r.lines || [] } });
  } catch (e) {
    toast("读取日志失败:" + e.message, "err");
  }
}

export async function plInstall(id, kind) {
  patchPlugins({ busy: id });
  try {
    const r = await api(ROUTES.pluginInstall, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, kind }),
    });
    toast(r.message || "已安装", "ok");
    await loadPluginCentre();
    await plFetchCatalog();
  } catch (e) {
    toast("安装失败:" + e.message, "err");
  } finally {
    patchPlugins({ busy: null });
  }
}

export async function plUninstall(id, name) {
  if (!confirm("卸载「" + (name || id) + "」?\n\n这会删除插件目录,不可恢复。")) return;
  try {
    await api(ROUTES.plugin(id), { method: "DELETE" });
    if (getState().plugins.sel === id) patchPlugins({ sel: "" });
    await loadPluginCentre("已卸载");
  } catch (e) {
    toast("卸载失败:" + e.message, "err");
  }
}

export async function plMakePlugin(kind) {
  let prompt;
  try {
    const r = await api(ROUTES.pluginPrompt(kind));
    prompt = r.prompt || "";
  } catch (e) {
    toast("取提示词失败:" + e.message, "err");
    return;
  }
  closeSettingsPanel();
  resetToNew();
  setState({ composerSeed: { id: Date.now(), text: prompt } });
  toast("提示词已填好,按发送让 agent 开始写插件", "ok");
}

let pluginPollTimer = null;
export function plStartPoll() {
  plStopPoll();
  pluginPollTimer = setInterval(() => {
    if (!getState().settingsOpen) { plStopPoll(); return; }
    if (!getState().plugins.list.some((p) => p.process)) return;
    loadPluginCentre();
  }, 3000);
}
export function plStopPoll() {
  if (pluginPollTimer) { clearInterval(pluginPollTimer); pluginPollTimer = null; }
}
