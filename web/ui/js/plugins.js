/* ---------------- 设置:插件 ----------------
   插件是纯落盘的东西:~/.mini_code/plugins/<id>/plugin.json。
   这一页只做两件事:列出装了什么、提供一个「重新扫描」。
   没有安装/卸载按钮 —— 装 = 把目录拷进去,卸 = 把目录删掉,
   这比再发明一套安装协议更简单,也不会让 UI 变成半个包管理器。 */
const pluginState = { dir: "", list: [] };

// paintPlugins 渲染插件卡片。数据来自 GET /api/plugins(服务端每次现读目录)。
function renderPlugins() {
  const box = $("#pl-list");
  if (!box) return;
  box.innerHTML = "";
  if (!pluginState.list.length) {
    box.innerHTML = '<div class="boot-empty">还没有安装插件。把插件目录放进:<br>' +
      esc(pluginState.dir || "~/.mini_code/plugins") + "</div>";
    return;
  }
  pluginState.list.forEach((p) => {
    const d = document.createElement("div");
    d.className = "boot-tile";
    const kinds = (p.kinds || []).map((k) => '<span class="bt-badge">' + esc(k) + "</span>").join("");
    const tools = (p.tools || []).length
      ? '<div class="bt-sub">工具:' + esc(p.tools.join(", ")) + "</div>"
      : "";
    d.innerHTML =
      '<div class="bt-name">' + esc(p.name || p.id) +
      (p.version ? '<span class="bt-badge">v' + esc(p.version) + "</span>" : "") +
      kinds + "</div>" +
      '<div class="bt-desc">' + esc(p.description || p.id) +
      (p.author ? "<br>by " + esc(p.author) : "") + "</div>" +
      tools;
    d.title = p.dir || "";
    box.appendChild(d);
  });
}

// loadPlugins 重新读一遍插件目录(装完插件点这个,不用重启)。
async function loadPlugins(msg) {
  try {
    const r = await api("/api/plugins");
    pluginState.list = r.plugins || [];
    const dirNow = $("#pl-dir-now");
    if (dirNow) {
      // 目录本身来自后端:取第一个插件的父目录,没有插件就显示默认位置。
      const first = pluginState.list[0];
      pluginState.dir = first ? String(first.dir || "").replace(/[\\/][^\\/]+$/, "") : "~/.mini_code/plugins";
      dirNow.textContent = pluginState.dir;
    }
    renderPlugins();
    if (msg) toast(msg, "ok");
  } catch (e) {
    pluginState.list = [];
    renderPlugins();
    toast("读取插件列表失败:" + e.message, "err");
  }
}

function initPluginSettings() {
  const b = $("#pl-reload");
  if (b) b.addEventListener("click", () => loadPlugins("已重新扫描插件目录"));
}
