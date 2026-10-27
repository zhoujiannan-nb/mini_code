/* ---------------- @ 文件夹选择器 ---------------- */
const FS_RECENT_KEY = "mini_code.recent_paths";
const fsState = { open: false, hits: [], sel: 0, timer: null, seq: 0, indexing: false };

function fsRecent() {
  try { return JSON.parse(localStorage.getItem(FS_RECENT_KEY) || "[]"); } catch (e) { return []; }
}
function fsRemember(path) {
  try {
    const r = fsRecent().filter(p => p !== path);
    r.unshift(path);
    localStorage.setItem(FS_RECENT_KEY, JSON.stringify(r.slice(0, 8)));
  } catch (e) {}
}
// 光标前紧邻的 "@token"(token 以空白或文本开头分隔),无则 null。
function fsTokenAt(text, caret) {
  const before = text.slice(0, caret);
  const m = before.match(/@([^\s@]*)$/);
  if (!m) return null;
  const start = caret - m[0].length;
  if (start > 0 && !/\s/.test(text[start - 1])) return null; // 邮箱等场景不触发
  return { query: m[1], start };
}
function fsOpen(query) {
  fsState.open = true;
  fsState.sel = 0;
  $("#fs-picker").hidden = false;
  $("#fs-q").textContent = "@" + query;
  clearTimeout(fsState.timer);
  fsState.timer = setTimeout(() => fsDoFetch(query), 150);
}
function fsClose() {
  fsState.open = false;
  clearTimeout(fsState.timer);
  fsState.seq++; // 丢弃在途请求
  $("#fs-picker").hidden = true;
}
function fsCheckInput() {
  const input = $("#input");
  const tok = fsTokenAt(input.value, input.selectionStart);
  if (tok) fsOpen(tok.query);
  else if (fsState.open) fsClose();
}
async function fsDoFetch(query) {
  const seq = ++fsState.seq;
  let data;
  try {
    data = await api("/api/fs/search?q=" + encodeURIComponent(query) + "&limit=20");
  } catch (e) {
    if (seq === fsState.seq && fsState.open) fsRender(null, "搜索失败:" + e.message);
    return;
  }
  if (seq !== fsState.seq || !fsState.open) return;
  fsState.indexing = !!data.indexing;
  let hits = data.dirs || [];
  // 群视图:@ 同时匹配群成员(置顶)——选成员 = 指定接收人
  if (state.currentGroup && state.groupData) {
    const q = (query || "").toLowerCase();
    const memberHits = state.groupData.members
      .filter(m => !q || m.name.toLowerCase().includes(q))
      .map(m => ({
        path: "@" + m.name,
        name: "💬 " + m.name + (m.status === "running" ? " (运行中)" : " (空闲)"),
        parent: "群成员 · 指定接收",
        _member: true,
      }));
    hits = memberHits.concat(hits);
  }
  if (query === "") {
    // 最近使用过的路径置顶
    const rec = fsRecent().filter(p => !hits.some(h => h.path === p));
    hits = rec.map(p => ({
      path: p,
      name: p.split(/[\\/]/).filter(Boolean).pop() || p,
      parent: "",
    })).concat(hits);
  }
  fsState.hits = hits;
  fsState.sel = 0;
  fsRender(hits, null);
  if (data.indexing) {
    // 目录索引还没建完:1.5s 后自动重查一次
    fsState.timer = setTimeout(() => { if (fsState.open) fsDoFetch(query); }, 1500);
  }
}
function fsRender(hits, errMsg) {
  const list = $("#fs-list");
  if (errMsg) {
    list.innerHTML = '<div class="fs-empty">' + esc(errMsg) + "</div>";
    return;
  }
  if (!hits || hits.length === 0) {
    list.innerHTML = '<div class="fs-empty">' +
      (fsState.indexing ? "🔍 正在建立目录索引,稍候…" : "没有匹配的文件夹") + "</div>";
    return;
  }
  list.innerHTML = hits.map((h, i) =>
    '<div class="fs-item' + (i === fsState.sel ? " sel" : "") + '" data-i="' + i + '">' +
    '<div class="fs-name">📁 ' + esc(h.name) + "</div>" +
    (h.parent && h.parent !== h.path ? '<div class="fs-path">' + esc(h.parent) + "</div>" : "") +
    "</div>"
  ).join("");
  list.querySelectorAll(".fs-item").forEach(el => {
    el.addEventListener("mousedown", (e) => {
      e.preventDefault(); // 保持输入框焦点,避免点击时先失焦
      fsInsert(Number(el.dataset.i));
    });
  });
}
function fsMove(d) {
  if (!fsState.hits.length) return;
  fsState.sel = (fsState.sel + d + fsState.hits.length) % fsState.hits.length;
  $("#fs-list").querySelectorAll(".fs-item").forEach((el, i) => {
    el.classList.toggle("sel", i === fsState.sel);
    if (i === fsState.sel) el.scrollIntoView({ block: "nearest" });
  });
}
// 把命中的完整路径替换掉输入框里的 "@查询词"
function fsInsert(i) {
  const h = fsState.hits[i];
  if (!h) return;
  const input = $("#input");
  const caret = input.selectionStart;
  const tok = fsTokenAt(input.value, caret);
  const start = tok ? tok.start : caret;
  const before = input.value.slice(0, start);
  const after = input.value.slice(caret);
  input.value = before + h.path + " " + after;
  const pos = (before + h.path + " ").length;
  input.setSelectionRange(pos, pos);
  if (!h._member) fsRemember(h.path); // 群成员名不记入最近路径
  fsClose();
  autoGrow();
  input.focus();
}

