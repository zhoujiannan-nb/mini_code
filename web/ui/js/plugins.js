/* ---------------- 设置:插件中心 ----------------
    两个页签:已安装 / 插件市场。布局参考 VS Code 的扩展页 ——
    **左边一列紧凑列表,右边一个详情面板**,而不是一屏大卡片。

    为什么换:
      三类插件各一张大卡铺在网格里,每张卡都带着状态、按钮、徽标、详情折叠,
      一屏只能放三四张,而且"想看的那个"和"别的"一样重。VS Code 的做法更省:
      列表里一眼扫完装了什么(图标 + 名字 + 一行描述),选中的那个才在右栏
      展开全部信息与动作。同一种行(行首图标 + 名称 + 描述 + tag)给所有插件用,
      分类只是行尾/详情里的 tag —— 分类是标签,不是布局。

    开机动画不是"另一套东西":它就是一个外观插件多声明一个 `boot` 字段
    (见 plugin/boot.go),所以它的详情面板里有「预览」「设为开机动画」,
    没有单独的开机动画页签。

    数据全部现读后端(/api/plugins 每次现扫目录),前端不缓存任何"装了什么"。 */

const PLUGIN_CATEGORIES = [
  { key: "process", icon: "⚙️", title: "程序", hint: "mini_code 启动并保活的外部进程" },
  { key: "tool", icon: "🔧", title: "工具", hint: "追加给模型的工具" },
  { key: "appearance", icon: "🎨", title: "外观", hint: "皮肤 / 壁纸 / 插槽 / 动画 / 开机画面" },
];

const pluginCentre = {
  list: [],
  dir: "",
  slots: [],
  catalog: [],
  catalogMsg: "",
  repo: "",
  tab: "installed",   // installed | market
  filter: "",         // category filter ("" = all)
  catalogKind: "",
  busy: null,         // id currently being installed/uninstalled
  interval: null,     // process polling while the page is open
  sel: "",            // 右栏正在看哪个插件(已安装页签)
  mSel: "",           // 右栏正在看哪个市场条目
};

// plCatMeta 返回插件所属分类的元信息。
function plCatMeta(cat) {
  return PLUGIN_CATEGORIES.find((c) => c.key === cat) ||
    { key: cat || "appearance", icon: "🧩", title: cat || "其他", hint: "" };
}

// plBadge 渲染一个 kinds 徽标。
function plBadge(text) {
  return '<span class="bt-badge">' + esc(text) + "</span>";
}

// plStateClass 把进程状态映射成色点类名。
function plStateClass(st) {
  return ({ running: "ok", starting: "wait", failed: "bad", stopped: "" })[st] || "";
}

// plStateLabel 进程状态的中文名。
function plStateLabel(st) {
  return ({ running: "运行中", starting: "启动中", failed: "失败", stopped: "已停止" })[st] || (st || "未知");
}

// plIconOf 选一个行首图标:分类图标 + 有开机动画时带一点提示。
function plIconOf(p) {
  return plCatMeta(p.category).icon;
}

/* ---------- 已安装:卡片网格 ---------- */

// renderPluginCentre 渲染卡片网格;选中某个插件时,网格让位给详情那一屏。
function renderPluginCentre() {
  const box = $("#pl-list");
  if (!box) return;
  const dirNow = $("#pl-dir-now");
  if (dirNow) dirNow.textContent = pluginCentre.dir || "~/.mini_code/plugins";
  const count = $("#pl-count");
  if (count) count.textContent = String(pluginCentre.list.length);

  const list = pluginCentre.list.filter((p) => !pluginCentre.filter || p.category === pluginCentre.filter);
  if (!pluginCentre.list.length || !list.length) {
    box.innerHTML = "";
    box.hidden = false;
    plDetailHide();
    plDetailEmpty("", pluginCentre.list.length
      ? "这个分类下还没有插件 —— 把筛选项改回「全部」试试。"
      : "还没有装插件。去「插件市场」挑一个,或用「造一个插件」让 agent 写一个。");
    return;
  }
  // 看详情的那个必须还在当前列表里(换筛选、卸载之后都可能不在了)
  if (pluginCentre.sel && !list.some((p) => p.id === pluginCentre.sel)) pluginCentre.sel = "";

  box.innerHTML = "";
  list.forEach((p) => box.appendChild(pluginTile(p)));
  // 没选 = 网格;选了 = 详情。两者不同时出现,所以一屏永远装得下。
  if (pluginCentre.sel) {
    box.hidden = true;
    renderPluginDetail();
  } else {
    box.hidden = false;
    plDetailHide();
  }
}

// pluginTile 是一张卡片,和「模型」页的 .model-tile 同一套尺寸与层级:
// 一屏能扫完装了什么,细节全在点进去之后。
function pluginTile(p) {
  const d = document.createElement("div");
  const isCur = !!p.boot && bootState.picked === p.id;
  d.className = "pl-tile" + (p.enabled ? "" : " is-off") + (isCur ? " has-cur" : "");
  d.dataset.id = p.id;
  d.dataset.category = p.category || "";
  d.setAttribute("role", "button");
  d.tabIndex = 0;

  const cat = plCatMeta(p.category);
  // 一行状态字:程序报运行状态,动画报"当前"(或什么都不说)。
  // 这一行**总是存在**(可能为空):少一行会让同排卡片高低不齐。
  let stat = "";
  if (p.process) {
    stat = '<span class="pl-dot ' + plStateClass(p.process.state) + '"></span>' +
      esc(plStateLabel(p.process.state));
  } else if (p.error) {
    stat = "加载失败";
  } else if (isCur) {
    stat = "<b>开机动画</b> · 正在使用";
  } else if (p.boot) {
    stat = "开机动画";
  }

  d.innerHTML =
    (isCur ? '<span class="pl-tile-cur">当前</span>' : "") +
    '<span class="pl-tile-top">' +
      '<span class="pl-tile-ico">' + cat.icon + "</span>" +
      '<span class="pl-tile-name">' + esc(p.name || p.id) + "</span>" +
      '<span class="pl-tagcat" title="' + esc(cat.hint) + '">' + esc(cat.title) + "</span>" +
    "</span>" +
    '<span class="pl-tile-desc">' + esc(p.description || p.id) + "</span>" +
    '<span class="pl-tile-stat"' + (p.error ? ' style="color:var(--red)"' : "") + ">" +
      stat + "</span>" +
    '<span class="pl-tile-go">详情 ›</span>';
  d.title = (p.description || p.id) + " —— 点击查看详情";
  return d;
}

/* ---------- 已安装:详情(点卡片进来的一屏) ---------- */

// plDetailHide 收起详情,让网格重新出现。
function plDetailHide() {
  const box = $("#pl-detail");
  if (!box) return;
  box.hidden = true;
  box.innerHTML = "";
  box.dataset.id = "";
}

// plDetailOpen 打开详情。market=true 时用市场条目,否则用已装插件。
function plDetailOpen(id) {
  pluginCentre.sel = id || "";
  renderPluginCentre();
}
function plMarketDetailOpen(id) {
  pluginCentre.mSel = id || "";
  renderPluginCatalog();
}

// plDetailEmpty 在指定容器里写一个空态(默认是详情那一块)。
function plDetailEmpty(title, sub, sel) {
  const box = $(sel || "#pl-detail");
  if (!box) return;
  box.hidden = false;
  box.innerHTML = '<div class="pl-empty">' +
    (title ? '<div class="pl-empty-title">' + esc(title) + "</div>" : "") +
    '<div class="pl-empty-sub">' + esc(sub) + "</div></div>";
}

// renderPluginDetail 渲染详情那一屏:返回 + 标题区 + 动作 + 说明 + 各分区。
function renderPluginDetail() {
  const box = $("#pl-detail");
  if (!box) return;
  const p = pluginCentre.list.find((x) => x.id === pluginCentre.sel);
  if (!p) { plDetailHide(); return; }
  box.hidden = false;

  // ---- 标题区 ----
  let actions = "";
  if (p.process) {
    const st = p.process;
    actions += plBtn("start", "启动", st.state === "running");
    actions += plBtn("stop", "停止", st.state !== "running" && st.state !== "starting");
    actions += plBtn("restart", "重启");
    actions += plBtn("logs", "日志");
  }
  if (p.boot) {
    const isCur = bootState.picked === p.id;
    actions += plBtn("boot-preview", "预览");
    if (!isCur) actions += plBtn("boot-set", "设为开机动画", false, "primary-ghost");
  }

  const cat = plCatMeta(p.category);
  let html = '<button class="pl-back" data-act="back" type="button">‹ 全部插件</button>';
  html += plDetailHead(p, cat, actions);
  html += '<div class="pl-d-desc">' + esc(p.description || p.id) + "</div>";

  // ---- 错误:说清原因,这比"没出现"好 ----
  if (p.error) html += '<div class="pl-err">加载失败:' + esc(p.error) + "</div>";
  if (p.reason) html += '<div class="pl-note">' + esc(p.reason) + "</div>";

  // ---- 程序类:实时状态 ----
  if (p.process) {
    const st = p.process;
    html += '<div class="pl-d-sec">' +
      '<div class="pl-d-sec-title">状态</div>' +
      '<div class="pl-proc">' +
        '<span class="pl-dot ' + plStateClass(st.state) + '"></span>' +
        '<span class="pl-proc-state">' + esc(plStateLabel(st.state)) + "</span>" +
        (st.pid ? '<span class="hint">PID ' + esc(String(st.pid)) + "</span>" : "") +
        (st.uptime_seconds ? '<span class="hint">已运行 ' + esc(fmtUptime(st.uptime_seconds)) + "</span>" : "") +
        (st.restarts ? '<span class="hint">重启 ' + esc(String(st.restarts)) + " 次</span>" : "") +
      "</div>" +
      (st.endpoint ? '<div class="pl-endpoint">接口 <code>' + esc(st.endpoint) + "</code></div>" : "") +
      (st.last_error ? '<div class="pl-err">' + esc(st.last_error) + "</div>" : "") +
      "</div>";
  }

  // ---- 开机动画:播放设置就在这张卡上(它没有别的设置页了) ----
  if (p.boot) html += bootPlayPanel(p);

  // ---- 能力:这一节回答"它到底做了什么" ----
  const caps = [];
  if (p.ui) {
    if (p.ui.slots && p.ui.slots.length) caps.push(["插槽", p.ui.slots.join(" / ")]);
    if (p.ui.motion && p.ui.motion.length) caps.push(["动效", p.ui.motion.join(" / ")]);
    if (p.ui.accent) caps.push(["强调色", p.ui.accent]);
  }
  if (p.has_styles) caps.push(["皮肤", "CSS 注入"]);
  if (p.has_wallpaper) caps.push(["壁纸", "有"]);
  if (p.boot) {
    caps.push(["开机动画", (p.boot.entry || "index.html") +
      (p.boot.duration_ms ? " · " + (Math.round(p.boot.duration_ms / 100) / 10) + " 秒" : "")]);
  }
  if (p.tools && p.tools.length) caps.push(["工具", p.tools.join(", ")]);
  if (p.has_memory) caps.push(["记忆", "memory.md"]);
  if (caps.length) {
    html += '<div class="pl-d-sec">' +
      '<div class="pl-d-sec-title">能力</div><div class="pl-facts">' +
      caps.map(([k, v]) => '<div class="pl-fact"><span>' + esc(k) +
        "</span><code>" + esc(v) + "</code></div>").join("") +
      "</div></div>";
  }

  // ---- 详情:目录与原始字段,默认折叠(VS Code 的「细节」页签) ----
  const facts = [];
  if (p.category === "process" && p.process) {
    facts.push(["命令", p.process.command || "—"]);
    facts.push(["接口", p.process.endpoint || "—"]);
  }
  if (p.kinds && p.kinds.length) facts.push(["声明", p.kinds.join(", ")]);
  if (p.dir) facts.push(["目录", p.dir]);
  if (facts.length) {
    html += '<details class="pl-more pl-d-more"><summary>细节</summary>' +
      '<div class="pl-facts">' +
      facts.map(([k, v]) => '<div class="pl-fact"><span>' + esc(k) +
        "</span><code>" + esc(v) + "</code></div>").join("") +
      "</div></details>";
  }

  // ---- 底栏:启用开关 + 卸载 ----
  html += '<div class="pl-foot">' +
    '<label class="ck-row pl-toggle"><input type="checkbox" data-act="toggle"' +
    (p.enabled ? " checked" : "") + ">启用</label>" +
    '<button class="mode-btn danger-ghost" data-act="uninstall">卸载</button>' +
    "</div>";

  box.dataset.id = p.id;
  box.innerHTML = html;
}

// plBtn 生成详情里的一个动作按钮。disabled 只是视觉/行为上的禁用,
// 事件仍走页面上的统一委托(禁用的按钮本来就不发 click)。
function plBtn(act, label, disabled, extra) {
  return '<button class="mode-btn ' + (extra || "") + '" data-act="' + act + '"' +
    (disabled ? " disabled" : "") + ">" + esc(label) + "</button>";
}

// plDetailHead 是左右分栏右栏的标题区,已装与市场共用(同一套视觉)。
function plDetailHead(item, cat, actions) {
  const id = esc(item.id || "");
  return '<div class="pl-d-head">' +
      '<span class="pl-d-icon">' + cat.icon + "</span>" +
      '<div class="pl-d-title">' +
        '<div class="pl-d-name">' + esc(item.name || item.id) + "</div>" +
        '<div class="pl-d-meta">' +
          '<span class="pl-tagcat pl-tagcat-lg">' + esc(cat.title) + "</span>" +
          '<span class="pl-d-id">' + id + "</span>" +
        "</div>" +
      "</div>" +
      (actions ? '<div class="pl-d-actions">' + actions + "</div>" : "") +
    "</div>";
}

// bootPlayPanel 是开机动画的播放设置。它住在动画插件的详情里 ——
// 外观页那边只剩一句摘要,所以这里得回答清楚"现在是不是它在播"。
function bootPlayPanel(p) {
  const play = bootPlaySettings();
  const isCur = bootState.picked === p.id;
  const mode = play.mode;
  const modes = [
    ["session", "每个会话一次"],
    ["always", "每次都播"],
    ["daily", "每天一次"],
  ];
  return '<div class="pl-d-sec pl-play">' +
    '<div class="pl-d-sec-title">开机播放</div>' +
    (isCur
      ? '<div class="pl-play-state on"><span class="pl-dot ' + (play.enabled ? "ok" : "") + '"></span>' +
        (play.enabled ? "正在使用" : "已关闭") + "</div>"
      : '<div class="pl-play-state">当前用的是别的动画 —— 点「设为开机动画」切过来</div>') +
    '<div class="pl-seg" role="group" aria-label="播放时机">' +
      modes.map(([v, label]) =>
        '<button class="pl-seg-btn' + (v === mode ? " on" : "") + '" data-act="boot-mode" ' +
        'data-mode="' + v + '" type="button">' + esc(label) + "</button>").join("") +
    "</div>" +
    '<div class="pl-play-row">' +
      '<label class="ck-row"><input type="checkbox" data-act="boot-enabled"' +
        (play.enabled ? " checked" : "") + ">启用</label>" +
      '<label class="ck-row"><input type="checkbox" data-act="boot-mute"' +
        (play.mute ? " checked" : "") + ">静默</label>" +
    "</div>" +
    "</div>";
}

/* ---------- 市场:同一套卡片 + 详情 ---------- */

// renderPluginCatalog 渲染市场卡片网格;选中某个条目时让位给它的详情。
function renderPluginCatalog() {
  const box = $("#pl-market-list");
  if (!box) return;
  const items = pluginCentre.catalogKind
    ? pluginCentre.catalog.filter((e) => e.kind === pluginCentre.catalogKind)
    : pluginCentre.catalog;

  if (!pluginCentre.catalog.length || !items.length) {
    box.innerHTML = "";
    box.hidden = false;
    plDetailHide2();
    plDetailEmpty("", pluginCentre.catalog.length
      ? "这个分类下没有可下载的插件 —— 把筛选项改回「全部」试试。"
      : (pluginCentre.catalogMsg || "还没有读取插件仓库") +
        "(点上面的「刷新」重新读取;" +
        (pluginCentre.repo || "zhoujiannan-nb/mini_code-plugins") + ")",
    "#pl-market-detail");
    return;
  }
  if (pluginCentre.mSel && !items.some((e) => e.id === pluginCentre.mSel)) pluginCentre.mSel = "";

  box.innerHTML = "";
  items.forEach((e) => box.appendChild(marketTile(e)));
  if (pluginCentre.mSel) {
    box.hidden = true;
    renderMarketDetail();
  } else {
    box.hidden = false;
    plDetailHide2();
  }
}

// marketTile 是市场里的一张卡:名字 + 分类 tag + 一行描述 + 装/未装。
function marketTile(e) {
  const d = document.createElement("div");
  d.className = "pl-tile";
  d.dataset.id = e.id;
  d.dataset.category = e.category || "";
  d.setAttribute("role", "button");
  d.tabIndex = 0;
  const cat = plCatMeta(e.category);
  const installed = e.installed && e.up_to_date;
  d.innerHTML =
    '<span class="pl-tile-top">' +
      '<span class="pl-tile-ico">' + cat.icon + "</span>" +
      '<span class="pl-tile-name">' + esc(e.name || e.id) + "</span>" +
      '<span class="pl-tagcat">' + esc(cat.title) + "</span>" +
    "</span>" +
    '<span class="pl-tile-desc">' + esc(e.description || e.id) + "</span>" +
    '<span class="pl-tile-stat">' +
      (installed ? "<b>已安装</b>"
        : e.installed ? "<b>可更新</b> · v" + esc(e.version || "?")
        : "v" + esc(e.version || "?")) +
    "</span>" +
    '<span class="pl-tile-go">详情 ›</span>';  d.title = (e.description || e.id) + " —— 点击查看详情";
  return d;
}

// plDetailHide2 收起市场详情。
function plDetailHide2() {
  const box = $("#pl-market-detail");
  if (!box) return;
  box.hidden = true;
  box.innerHTML = "";
  box.dataset.id = "";
}

// renderMarketDetail 渲染市场详情那一屏。
function renderMarketDetail() {
  const box = $("#pl-market-detail");
  if (!box) return;
  const e = pluginCentre.catalog.find((x) => x.id === pluginCentre.mSel);
  if (!e) { plDetailHide2(); return; }
  box.hidden = false;
  const cat = plCatMeta(e.category);
  let btn;
  if (pluginCentre.busy === e.id) btn = '<button class="mode-btn" disabled>下载中…</button>';
  else if (e.installed && e.up_to_date) btn = '<button class="mode-btn" disabled>已安装</button>';
  else if (e.installed) btn = '<button class="mode-btn primary-ghost" data-act="install">更新到 v' + esc(e.version || "?") + "</button>";
  else btn = '<button class="mode-btn primary-ghost" data-act="install">安装</button>';

  const tags = (e.tags || []).map(plBadge).join("");
  const facts = [];
  if (e.license) facts.push(["许可", e.license]);
  if (e.bytes) facts.push(["大小", fmtBytes(e.bytes) + " · " + e.files + " 个文件"]);
  if (e.author) facts.push(["作者", e.author]);
  box.dataset.id = e.id;
  box.innerHTML =
    '<button class="pl-back" data-act="back-market" type="button">‹ 全部插件</button>' +
    plDetailHead(e, cat, btn) +
    '<div class="pl-d-desc">' + esc(e.description || "") + "</div>" +
    (facts.length
      ? '<div class="pl-d-sec"><div class="pl-d-sec-title">信息</div><div class="pl-facts">' +
        facts.map(([k, v]) => '<div class="pl-fact"><span>' + esc(k) +
          "</span><code>" + esc(v) + "</code></div>").join("") +
        "</div></div>"
      : "") +
    (tags ? '<div class="pl-look">' + tags + "</div>" : "");
}

// fmtBytes 字节数转人话。
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

// fmtUptime 把秒数写成人话。
function fmtUptime(s) {
  s = Number(s) || 0;
  if (s < 60) return s + " 秒";
  if (s < 3600) return Math.floor(s / 60) + " 分钟";
  if (s < 86400) return Math.floor(s / 3600) + " 小时";
  return Math.floor(s / 86400) + " 天";
}

// plFetchCatalog 读远程目录。
async function plFetchCatalog() {
  const msg = $("#pl-market-msg");
  if (msg) msg.textContent = "读取中…";
  try {
    const r = await api("/api/plugins/catalog");
    pluginCentre.repo = r.repo || pluginCentre.repo;
    pluginCentre.catalog = r.entries || [];
    pluginCentre.catalogMsg = pluginCentre.catalog.length
      ? "" : "插件仓库里还没有可下载的插件";
    if (msg) {
      msg.textContent = pluginCentre.catalog.length
        ? "来自 " + (r.repo || "插件仓库") + " · " + pluginCentre.catalog.length + " 个"
        : pluginCentre.catalogMsg;
    }
    // 分类下拉按实际出现的 kind 填充
    const sel = $("#pl-market-kind");
    if (sel) {
      const kinds = Array.from(new Set(pluginCentre.catalog.map((e) => e.kind))).sort();
      const cur = sel.value;
      sel.innerHTML = '<option value="">全部</option>' +
        kinds.map((k) => '<option value="' + esc(k) + '">' + esc(k) + "</option>").join("");
      sel.value = kinds.includes(cur) ? cur : "";
      pluginCentre.catalogKind = sel.value;
    }
    renderPluginCatalog();
  } catch (e) {
    pluginCentre.catalog = [];
    pluginCentre.catalogMsg = "读取失败:" + e.message;
    if (msg) msg.textContent = pluginCentre.catalogMsg;
    renderPluginCatalog();
  }
}

// plInstall 下载安装一个市场插件。
async function plInstall(id, kind) {
  pluginCentre.busy = id;
  renderMarketDetail();
  try {
    const r = await api("/api/plugins/install", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: id, kind: kind }),
    });
    toast(r.message || "已安装", "ok");
    await loadPluginCentre();
    await plFetchCatalog();
  } catch (e) {
    toast("安装失败:" + e.message, "err");
  } finally {
    pluginCentre.busy = null;
    renderPluginCatalog();
  }
}

// loadPluginCentre 重新读一遍后端状态并重绘。
async function loadPluginCentre(msg) {
  try {
    const r = await api("/api/plugins");
    pluginCentre.list = r.plugins || [];
    pluginCentre.dir = r.dir || pluginCentre.dir;
    pluginCentre.slots = r.slots || [];
    // 开机动画也现读一次:详情里的"当前/设为开机动画"要靠它,而且开关插件
    // 会直接决定播不播,两份视图必须说同一件事。
    if (typeof bootLoadLocal === "function") await bootLoadLocal();
    renderPluginCentre();
    if (msg) toast(msg, "ok");
  } catch (e) {
    pluginCentre.list = [];
    pluginCentre.sel = "";
    const box = $("#pl-list");
    if (box) { box.hidden = false; box.innerHTML = ""; }
    plDetailHide();
    plDetailEmpty("读取插件列表失败", e.message);
    toast("读取插件列表失败:" + e.message, "err");
  }
}

/* ---------- 动作 ---------- */

// plToggle 开关一个插件。进程类被关掉时后端会顺手停进程。
async function plToggle(id, on, el) {
  if (el) el.disabled = true;
  try {
    await api("/api/plugins/" + encodeURIComponent(id) + "/toggle", {
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

// plProcess 启动/停止/重启一个程序插件。
async function plProcess(id, action) {
  pluginCentre.busy = id;
  try {
    const r = await api("/api/plugins/" + encodeURIComponent(id) + "/process", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action }),
    });
    toast(r.message || "完成", "ok");
  } catch (e) {
    toast("操作失败:" + e.message, "err");
  } finally {
    pluginCentre.busy = null;
    await loadPluginCentre();
  }
}

// plLogs 拉一个程序插件的日志尾巴。
async function plLogs(id) {
  try {
    const r = await api("/api/plugins/" + encodeURIComponent(id) + "/logs?n=80");
    showPluginLogs(id, r.lines || []);
  } catch (e) {
    toast("读取日志失败:" + e.message, "err");
  }
}

// showPluginLogs 用一个小模态显示日志。
function showPluginLogs(id, lines) {
  let mask = $("#pl-log-mask");
  if (!mask) {
    mask = document.createElement("div");
    mask.id = "pl-log-mask";
    mask.className = "modal-mask";
    mask.innerHTML = '<div class="modal modal-620">' +
      '<div class="set-head"><h2 id="pl-log-title">日志</h2>' +
      '<button class="set-close" id="pl-log-close" aria-label="关闭">&#10005;</button></div>' +
      '<pre class="pl-log" id="pl-log-body"></pre></div>';
    document.body.appendChild(mask);
    $("#pl-log-close").addEventListener("click", () => mask.classList.remove("open"));
    mask.addEventListener("click", (e) => { if (e.target === mask) mask.classList.remove("open"); });
  }
  $("#pl-log-title").textContent = "日志 · " + id;
  $("#pl-log-body").textContent = lines.length ? lines.join("\n") : "(还没有输出)";
  mask.classList.add("open");
}

// plUninstall 卸载:确认后删目录。
async function plUninstall(id, name) {
  if (!confirm("卸载「" + (name || id) + "」?\n\n这会删除插件目录,不可恢复。")) return;
  try {
    await api("/api/plugins/" + encodeURIComponent(id), { method: "DELETE" });
    if (pluginCentre.sel === id) pluginCentre.sel = "";
    await loadPluginCentre("已卸载");
  } catch (e) {
    toast("卸载失败:" + e.message, "err");
  }
}

/* ---------- 造插件:跳去会话 ---------- */

// plMakePlugin 取标准提示词 → 关设置 → 新会话 → 填进输入框。
async function plMakePlugin(kind) {
  let prompt;
  try {
    const r = await api("/api/plugins/prompt?kind=" + encodeURIComponent(kind));
    prompt = r.prompt || "";
  } catch (e) {
    toast("取提示词失败:" + e.message, "err");
    return;
  }
  // 关掉设置面板,回到会话视图
  closeSettings();
  // 新建一个空白会话:插件应该在一个干净上下文里写
  if (state.currentGroup) exitGroupView();
  resetToNew();
  const input = $("#input");
  if (!input) return;
  input.value = prompt;
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
  if (typeof autoGrow === "function") autoGrow();
  toast("提示词已填好,按发送让 agent 开始写插件", "ok");
}

/* ---------- 页签 ---------- */

// plShowTab 切换"已安装 / 插件市场"。
function plShowTab(name) {
  pluginCentre.tab = name;
  document.querySelectorAll("#page-plugins .pl-tab").forEach((b) => {
    b.classList.toggle("active", b.dataset.tab === name);
  });
  ["installed", "market"].forEach((k) => {
    const el = $("#pl-pane-" + k);
    if (el) el.classList.toggle("active", name === k);
  });
  if (name === "market") {
    // 进入市场页签就自动执行一次读取:点"插件市场"的人想看的是目录,
    // 不是一句"点刷新试试"。后端有 5 分钟缓存,重复进入不会反复打 GitHub。
    renderPluginCatalog(); // 先用上一次的结果顶上(首次是空态)
    plFetchCatalog();
  } else {
    // 切回"已安装"总是回到网格:上次点开的详情不该在换页签之后还留着。
    pluginCentre.sel = "";
    renderPluginCentre();
  }
}

/* ---------- 初始化 ---------- */

function initPluginSettings() {
  // 「重新扫描」
  const reload = $("#pl-reload");
  if (reload) reload.addEventListener("click", () => loadPluginCentre("已重新扫描插件目录"));

  // 页签
  document.querySelectorAll("#page-plugins .pl-tab").forEach((b) => {
    b.addEventListener("click", () => plShowTab(b.dataset.tab));
  });

  // 分类筛选
  const filter = $("#pl-filter");
  if (filter) {
    filter.addEventListener("change", () => {
      pluginCentre.filter = filter.value;
      pluginCentre.sel = ""; // 换筛选:右栏跟着列表重选
      renderPluginCentre();
    });
  }

  // 「造一个」三个按钮
  document.querySelectorAll("#page-plugins [data-make]").forEach((b) => {
    b.addEventListener("click", () => plMakePlugin(b.dataset.make));
  });

  // 市场
  const fetchBtn = $("#pl-market-fetch");
  if (fetchBtn) fetchBtn.addEventListener("click", plFetchCatalog);
  const kindSel = $("#pl-market-kind");
  if (kindSel) {
    kindSel.addEventListener("change", () => {
      pluginCentre.catalogKind = kindSel.value;
      pluginCentre.mSel = "";
      renderPluginCatalog();
    });
  }

  // 事件委托:点卡片进详情 + 详情里的动作
  const page = $("#page-plugins");
  if (!page) return;
  page.addEventListener("click", (e) => {
    // 1) 点一张卡 = 进它的详情(卡片里没有按钮,所以不会误触)
    const tile = e.target.closest(".pl-tile");
    if (tile && !e.target.closest("button")) {
      const id = tile.dataset.id;
      if (tile.closest("#pl-market-list")) plMarketDetailOpen(id);
      else plDetailOpen(id);
      return;
    }
    // 2) 详情里的按钮(含「返回」)
    const btn = e.target.closest("button[data-act]");
    if (!btn) return;
    const act = btn.dataset.act;
    // 返回不依赖 data-id:它是把"看详情"切回"看网格"的开关。
    if (act === "back") { pluginCentre.sel = ""; renderPluginCentre(); return; }
    if (act === "back-market") { pluginCentre.mSel = ""; renderPluginCatalog(); return; }
    const host = btn.closest("[data-id]");
    const id = host && host.dataset.id;
    if (!id) return;
    if (act === "start") plProcess(id, "start");
    else if (act === "stop") plProcess(id, "stop");
    else if (act === "restart") plProcess(id, "restart");
    else if (act === "logs") plLogs(id);
    else if (act === "boot-preview") bootPreview(id);
    else if (act === "boot-set") bootPick(id);
    else if (act === "boot-mode") bootSetMode(btn.dataset.mode);
    else if (act === "uninstall") {
      const p = pluginCentre.list.find((x) => x.id === id);
      plUninstall(id, p ? p.name : id);
    } else if (act === "install") {
      const it = pluginCentre.catalog.find((x) => x.id === id);
      plInstall(id, it ? it.kind : "");
    }
  });
  // 键盘:卡片可以用回车/空格进去(它是一个可聚焦的方块)
  page.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const tile = e.target.closest && e.target.closest(".pl-tile");
    if (!tile) return;
    e.preventDefault();
    tile.click();
  });
  page.addEventListener("change", (e) => {
    const t = e.target;
    const host = t.closest("[data-id]");
    // 开机动画的播放开关(启用/静默)住在动画插件的详情里,不是插件的 enabled。
    if (t.matches('input[data-act="boot-enabled"]')) { bootSetEnabled(t.checked); return; }
    if (t.matches('input[data-act="boot-mute"]')) { bootSetMute(t.checked); return; }
    if (!t.matches('input[data-act="toggle"]')) return;
    if (!host) return;
    plToggle(host.dataset.id, t.checked, t);
  });

  // 播放时机的分段按钮不是 input,走 click 委托(和上面那些 data-act 一起处理)。

  // 打开设置页时轮询进程状态:程序插件的状态会自己变(崩溃/重启),
  // 用户在看这一页时应该能看见,不用手点刷新。
  const nav = $("#set-nav");
  if (nav) {
    nav.addEventListener("click", (e) => {
      const item = e.target.closest(".set-nav-item");
      if (!item) return;
      if (item.dataset.page === "page-plugins") {
        loadPluginCentre();
        plStartPoll();
      } else {
        plStopPoll();
      }
    });
  }
}

// plStartPoll 每 3 秒刷一次插件列表,仅在有进程插件时。
function plStartPoll() {
  plStopPoll();
  pluginCentre.interval = setInterval(() => {
    if (!$("#settings-mask").classList.contains("open")) { plStopPoll(); return; }
    if (!pluginCentre.list.some((p) => p.process)) return;
    loadPluginCentre();
  }, 3000);
}

function plStopPoll() {
  if (pluginCentre.interval) {
    clearInterval(pluginCentre.interval);
    pluginCentre.interval = null;
  }
}
