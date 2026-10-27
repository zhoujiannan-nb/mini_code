/* ---------------- 设置:开机动画 ---------------- */
// 表单值只存在控件里,和模型区一样要点「保存配置」才写回服务端。
// bootState 记的是"目录里有什么",picked 是当前选中的主题 id。
const bootState = { dir: "", local: [], remote: null, picked: "" };

function bootForm() {
  return {
    enabled: $("#b-enabled").checked,
    theme: bootState.picked || "",
    mode: $("#b-mode").value || "session",
    speed: Number($("#b-speed").value) || 1,
    name: $("#b-name").value.trim(),
    interactive: $("#b-interactive").checked,
    mute: $("#b-mute").checked,
    dir: $("#b-dir").value.trim(),
    repo: $("#b-repo").value.trim(),
  };
}
function bootFillForm(ba) {
  ba = ba || {};
  bootState.picked = ba.theme || "";
  $("#b-enabled").checked = !!ba.enabled;
  $("#b-mode").value = ba.mode || "session";
  // 配置里可能是手写的任意倍速,下拉里没有就取最接近的一档
  const sp = Number(ba.speed) > 0 ? Number(ba.speed) : 1;
  const opts = Array.prototype.map.call($("#b-speed").options, (o) => Number(o.value));
  let best = opts[0] || 1;
  let dist = Infinity;
  opts.forEach((v) => {
    if (v <= 0) return;
    const dv = Math.abs(Math.log(v / sp));
    if (dv < dist) { dist = dv; best = v; }
  });
  $("#b-speed").value = String(best);
  $("#b-name").value = ba.name || "";
  $("#b-interactive").checked = !!ba.interactive;
  $("#b-mute").checked = !!ba.mute;
  $("#b-dir").value = ba.dir || "";
  $("#b-repo").value = ba.repo || "";
}
// 已安装主题卡片:点卡片选中,卡片上的按钮各自处理(停止冒泡交给 dataset 判断)。
function renderBootLocal() {
  const box = $("#b-local");
  box.innerHTML = "";
  if (!bootState.local.length) {
    box.innerHTML = '<div class="boot-empty">这个目录里还没有可用的主题。到下面「在线获取」下载一个,' +
      "或把主题文件夹放进:<br>" + esc(bootState.dir) + "</div>";
    return;
  }
  bootState.local.forEach((t) => {
    const bad = !!t.error;
    const sel = !bad && t.id === bootState.picked;
    const d = document.createElement("div");
    d.className = "boot-tile" + (bad ? " bad" : "") + (sel ? " sel" : "");
    d.innerHTML =
      '<div class="bt-name">' + esc(t.name || t.id) +
      (sel ? '<span class="bt-badge cur">当前</span>' : "") +
      (t.version ? '<span class="bt-badge">v' + esc(t.version) + "</span>" : "") +
      (!bad && t.durationMs ? '<span class="bt-badge">' + (Math.round(t.durationMs / 100) / 10) + "s</span>" : "") +
      (t.interactive ? '<span class="bt-badge">可交互</span>' : "") + "</div>" +
      '<div class="bt-desc">' + esc(t.description || t.id) + "</div>" +
      (bad
        ? '<div class="bt-err">无法播放:' + esc(t.error) + "</div>"
        : '<div class="bt-foot"><button class="mode-btn" data-act="preview">预览</button>' +
          '<button class="mode-btn" data-act="del">卸载</button></div>');
    if (!bad) {
      d.addEventListener("click", (e) => {
        const act = e.target && e.target.dataset ? e.target.dataset.act : "";
        if (act === "preview") { bootPreview(t); return; }
        if (act === "del") { bootDelete(t); return; }
        bootState.picked = t.id;
        renderBootLocal();
      });
    }
    box.appendChild(d);
  });
}
function renderBootRemote() {
  const box = $("#b-remote");
  box.innerHTML = "";
  if (!bootState.remote) return;
  const list = bootState.remote.themes || [];
  if (!list.length) {
    box.innerHTML = '<div class="boot-empty">插件仓库里没有可用的主题</div>';
    return;
  }
  list.forEach((t) => {
    const d = document.createElement("div");
    d.className = "boot-tile";
    d.innerHTML =
      '<div class="bt-name">' + esc(t.name || t.id) +
      (t.version ? '<span class="bt-badge">v' + esc(t.version) + "</span>" : "") +
      (t.durationMs ? '<span class="bt-badge">' + (Math.round(t.durationMs / 100) / 10) + "s</span>" : "") +
      (t.installed ? '<span class="bt-badge cur">' + (t.latest ? "已安装" : "已装旧版") + "</span>" : "") +
      (t.interactive ? '<span class="bt-badge">可交互</span>' : "") + "</div>" +
      '<div class="bt-desc">' + esc(t.description || t.id) +
      (t.author ? "<br>by " + esc(t.author) : "") + "</div>" +
      '<div class="bt-foot"><button class="mode-btn" data-act="ins">' + (t.installed ? "重新安装" : "下载") + "</button></div>";
    d.addEventListener("click", (e) => {
      const act = e.target && e.target.dataset ? e.target.dataset.act : "";
      if (act === "ins") bootInstall(t, e.target);
    });
    box.appendChild(d);
  });
}
async function bootLoadLocal(msg) {
  try {
    const r = await api("/api/bootanim");
    bootState.dir = r.dir || "";
    bootState.local = r.themes || [];
    $("#b-dir-now").textContent = bootState.dir;
    if (msg) $("#b-remote-msg").textContent = msg;
    // 选中的主题被删掉了:回落到第一个还能播的,别留一个空选择。
    if (bootState.picked && !bootState.local.some((t) => t.id === bootState.picked && !t.error)) {
      bootState.picked = "";
    }
    if (!bootState.picked) {
      const first = bootState.local.find((t) => !t.error);
      if (first) bootState.picked = first.id;
    }
    renderBootLocal();
  } catch (e) {
    $("#b-local").innerHTML = '<div class="boot-empty">读取主题目录失败:' + esc(e.message) + "</div>";
  }
}
async function bootFetchRemote() {
  const msg = $("#b-remote-msg");
  msg.textContent = "正在从插件仓库获取列表…";
  try {
    const r = await api("/api/bootanim/remote");
    bootState.remote = r;
    renderBootRemote();
    msg.textContent = "共 " + (r.themes || []).length + " 个主题 · " + (r.repo || "");
  } catch (e) {
    bootState.remote = null;
    renderBootRemote();
    msg.textContent = "获取失败:" + e.message;
  }
}
async function bootInstall(t, btn) {
  if (btn) { btn.disabled = true; btn.textContent = "下载中…"; }
  try {
    const r = await api("/api/bootanim/install", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: t.id }),
    });
    toast(r.message || "已安装", "ok");
    if (!bootState.picked) bootState.picked = t.id;
    await bootLoadLocal();
    if (bootState.remote) bootFetchRemote();
  } catch (e) {
    toast("安装失败:" + e.message, "err");
    if (btn) { btn.disabled = false; btn.textContent = "下载"; }
  }
}
async function bootDelete(t) {
  if (!confirm("卸载主题「" + (t.name || t.id) + "」?会删除 " + bootState.dir + "\\" + t.id)) return;
  try {
    await api("/api/bootanim/" + encodeURIComponent(t.id), { method: "DELETE" });
    if (bootState.picked === t.id) bootState.picked = "";
    toast("已卸载", "ok");
    await bootLoadLocal();
    if (bootState.remote) bootFetchRemote();
  } catch (e) {
    toast("卸载失败:" + e.message, "err");
  }
}
// 预览用当前表单值(还没保存的改动也立刻见效),无视播放策略与开关。
function bootPlay(t) {
  const f = bootForm();
  const ok = window.__mcBoot && __mcBoot.play({
    entry: "/bootanim/" + t.id + "/" + (t.entry || "index.html"),
    speed: f.speed,
    name: f.name,
    interactive: f.interactive && !!t.interactive,
    durationMs: t.durationMs,
    maxMs: t.maxMs,
    minViewport: t.minViewport,
  });
  if (!ok) toast("这个主题播不了(入口文件缺失)", "err");
}
function bootPreview(t) { bootPlay(t); }
function initBootSettings() {
  $("#b-reload").addEventListener("click", () => bootLoadLocal("已重新扫描"));
  $("#b-fetch").addEventListener("click", bootFetchRemote);
  $("#b-preview").addEventListener("click", () => {
    const t = bootState.local.find((x) => x.id === bootState.picked && !x.error);
    if (!t) { toast("先点一张卡片选一个已安装的主题", "err"); return; }
    bootPlay(t);
  });
}

