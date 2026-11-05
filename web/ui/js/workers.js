/* ---------------- Worker(工作大类)----------------
 * 一个 worker = 一条工作线:一个共享工作目录 + 一份共享公约(worker.md)。
 * 这里的职责:切换条(‹ worker ›)、按 worker 过滤会话列表、把会话移动到
 * worker(原"归档"按钮的位置)、以及公约的查看/编辑/刷新。
 *
 * 会话侧的注入规则在服务端(session/worker.go):会话加入时把目录和公约快照写
 * 进会话记录,之后磁盘上的公约再怎么改都不会动这个会话;生效方式是往对话尾部
 * 追加一条"worker 简报"消息(和用量提醒、压缩摘要同一套路),所以前缀缓存不会被
 * 击穿。这里的"刷新公约"也只是让它追加一条新简报,最新的为准(不动已有内容)。
 */
const LS_WORKER_KEY = "mini_code.worker";

function workerBucketId() {
  return state.worker == null ? "*" : state.worker;
}

function restoreWorker() {
  try {
    const v = localStorage.getItem(LS_WORKER_KEY);
    if (v != null) state.worker = v;
  } catch (e) { /* 隐私模式 / 禁用存储:留在"全部" */ }
}
function rememberWorker(id) {
  try { localStorage.setItem(LS_WORKER_KEY, id); } catch (e) {}
}

// workerBuckets 是切换条的完整序列:全部 → 未分配 → 各 worker(内置分桶排在
// 最后,它们平常不可见,但必须够得着 —— 旧版"已归档"的会话就在那里)。
function workerBuckets() {
  const real = (state.workers || []).filter(w => !w.builtin);
  const builtin = (state.workers || []).filter(w => w.builtin);
  const out = [
    { worker_id: "*", name: "全部", sessions: real.reduce((n, w) => n + (w.sessions || 0), 0) + (state.unassigned || 0) },
    { worker_id: "", name: "未分配", sessions: state.unassigned || 0 },
  ];
  for (const w of real) out.push(w);
  for (const w of builtin) out.push(w);
  return out;
}

function workerLabel(id) {
  if (id === "*" || id == null) return "全部";
  if (id === "") return "未分配";
  const w = (state.workers || []).find(x => x.worker_id === id);
  return w ? w.name : "未知 worker";
}

function workerEmptyHint() {
  const id = workerBucketId();
  if (id === "*") return '暂无会话<br>发送第一条消息开始';
  if (id === "") return '暂无未分配会话<br>新建会话时选「未分配」即由 agent 自己判断归属';
  return '这个 worker 还没有会话<br>点「＋ 新建会话」直接在这里开工,或把别的会话 📁 移进来';
}

async function loadWorkers() {
  let got = true;
  try {
    const r = await api("/api/workers");
    state.workers = r.workers || [];
    state.unassigned = r.unassigned || 0;
  } catch (e) {
    // 服务未启用 worker(老服务端 / 未接线):切换条退化成只读提示
    got = false;
    state.workers = state.workers || [];
    state.unassigned = state.unassigned || 0;
  }
  // 选中的 worker 被删了:回到"全部",别把列表卡成空。只在真的拿到列表时才校准,
  // 否则一次网络抖动就会把用户正在看的 worker 踢回"全部"。
  const id = workerBucketId();
  if (got && id !== "*" && id !== "" && !state.workers.some(w => w.worker_id === id)) {
    state.worker = "*";
    rememberWorker("*");
  }
  paintWorkerBar();
}

function paintWorkerBar() {
  const name = $("#wk-name");
  if (!name) return;
  const id = workerBucketId();
  const w = (state.workers || []).find(x => x.worker_id === id);
  const count = id === "*"
    ? workerBuckets()[0].sessions
    : (id === "" ? (state.unassigned || 0) : (w ? w.sessions || 0 : 0));
  name.textContent = (w ? w.name : workerLabel(id)) + (count ? " · " + count : "");

  const sub = $("#worker-sub");
  if (!sub) return;
  if (!w || !w.work_dir) {
    // "全部"/"未分配"没有共享目录,原来那行解释性小字("全部:不区分 worker…"、
    // "未分配:新会话会自己判断…")是纯噪音,还老跟下拉菜单打架 —— 删了。
    // 空的 .worker-sub 由 CSS 的 :empty 折叠,不留空隙。
    sub.innerHTML = "";
  } else {
    sub.innerHTML =
      '<span class="wk-sub-dir" title="' + esc(w.work_dir) + '\n共享工作目录">📂 ' + esc(shortPath(w.work_dir, 34)) + '</span>' +
      '<button class="wk-sub-btn" id="wk-note-btn" title="查看/编辑公约(共享给这个 worker 的全部会话)">📄 公约</button>' +
      '<button class="wk-sub-btn" id="wk-summary-btn" title="改写摘要(新会话靠它决定加入哪个 worker)">✎ 摘要</button>' +
      (state.current ? '<button class="wk-sub-btn" id="wk-refresh-btn" title="把最新公约装进当前会话:下一条消息追加一条新简报,已有内容不动">↻ 刷新</button>' : "") +
      '<button class="wk-sub-btn wk-sub-danger" id="wk-del-btn" title="删除 worker 及其全部会话(目录保留)">🗑</button>';
    const bind = (sel, fn) => { const el = $(sel); if (el) el.addEventListener("click", fn); };
    bind("#wk-note-btn", () => openNoteEditor(w.worker_id));
    bind("#wk-summary-btn", () => editSummary(w.worker_id));
    bind("#wk-refresh-btn", () => refreshCurrentNote());
    bind("#wk-del-btn", () => deleteWorker(w.worker_id));
  }
}

// shortPath 只影响显示:长路径中间省略,鼠标悬停看全文。
function shortPath(p, keep) {
  if (!p) return "";
  const runes = [...p];
  if (runes.length <= keep) return p;
  return "…" + runes.slice(runes.length - keep).join("");
}

async function switchWorker(id) {
  state.worker = id;
  rememberWorker(id);
  closeWorkerMenu();
  await loadSessions();
}

/* ---------------- worker 菜单(切换 / 新建)---------------- */
function closeWorkerMenu() {
  const m = $("#worker-menu");
  if (m) { m.classList.remove("open"); m.innerHTML = ""; }
  const note = $("#wk-note-box");
  if (note) note.remove();
}

function openWorkerMenu(anchor) {
  const menu = $("#worker-menu");
  if (!menu) return;
  if (menu.classList.contains("open")) { closeWorkerMenu(); return; }
  const id = workerBucketId();
  let html = '<div class="wk-menu-head">切换 Worker</div>';
  for (const w of workerBuckets()) {
    html += '<div class="wk-menu-item' + (w.worker_id === id ? " cur" : "") + '" data-id="' + esc(w.worker_id) + '">' +
      '<span class="wk-menu-name">' + esc(w.name) + '</span>' +
      '<span class="wk-menu-n">' + (w.sessions || 0) + '</span></div>';
  }
  menu.innerHTML = html;
  menu.classList.add("open");
  if (anchor) {
    const r = anchor.getBoundingClientRect ? anchor.getBoundingClientRect() : { bottom: 120, left: 16 };
    menu.style.top = (r.bottom + 4) + "px";
    menu.style.left = Math.max(8, r.left) + "px";
  }
  menu.querySelectorAll(".wk-menu-item").forEach(el => {
    el.addEventListener("click", () => switchWorker(el.dataset.id));
  });
}

// 会话卡上的 📁:把这条会话移动到某个 worker(原归档按钮的位置)。
function openWorkerMoveMenu(sess, anchor) {
  const menu = $("#worker-menu");
  if (!menu) return;
  const cur = sess.worker_id || "";
  let html = '<div class="wk-menu-head">移动到 Worker</div>';
  for (const w of workerBuckets()) {
    if (w.builtin && w.worker_id !== cur) continue; // 内置分桶只给已经在里面的会话看到
    html += '<div class="wk-menu-item' + (w.worker_id === cur ? " cur" : "") + '" data-id="' + esc(w.worker_id) + '">' +
      '<span class="wk-menu-name">' + esc(w.name) + '</span>' +
      '<span class="wk-menu-n">' + (w.worker_id === cur ? "当前" : "") + '</span></div>';
  }
  menu.innerHTML = html;
  menu.classList.add("open");
  const r = anchor.getBoundingClientRect ? anchor.getBoundingClientRect() : { bottom: 160, left: 16 };
  menu.style.top = Math.max(8, Math.min(window.innerHeight - 260, r.bottom + 4)) + "px";
  menu.style.left = Math.max(8, r.left) + "px";
  menu.querySelectorAll(".wk-menu-item").forEach(el => {
    el.addEventListener("click", () => {
      closeWorkerMenu();
      moveSessionToWorker(sess.session_id, el.dataset.id, cur);
    });
  });
}

/* ---------------- 新建 / 改名 / 删除 ---------------- */
async function createWorker() {
  const name = (prompt("新建 Worker(工作大类):名称\n会在工作区里创建 worker-<名字> 目录,里面的 worker.md 是共享公约", "") || "").trim();
  if (!name) return;
  const summary = (prompt("这个 worker 在做什么?(新会话判断归属时看这段)", "") || "").trim();
  try {
    const r = await api("/api/workers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, summary }),
    });
    toast("已创建 worker:" + r.name, "ok");
    await loadWorkers();
    await switchWorker(r.worker_id);
  } catch (err) { toast("创建失败:" + err.message, "err"); }
}

async function editSummary(workerID) {
  const w = (state.workers || []).find(x => x.worker_id === workerID);
  const cur = w ? w.summary || "" : "";
  const ans = prompt("Worker 摘要(会注入到判断归属的会话里,保持一两句)", cur);
  if (ans == null) return; // 取消:留原样(prompt 返回 null,别把摘要当成空串写回去)
  const s = ans.trim();
  if (s === cur) return;
  try {
    await api("/api/workers/" + workerID + "/summary", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ summary: s.trim() }),
    });
    toast("摘要已更新", "ok");
    await loadWorkers();
  } catch (err) { toast("更新失败:" + err.message, "err"); }
}

async function deleteWorker(workerID) {
  const w = (state.workers || []).find(x => x.worker_id === workerID);
  const n = w ? w.sessions || 0 : 0;
  if (!confirm("删除 worker「" + (w ? w.name : workerID) + "」?\n里面的 " + n + " 个会话会一并删除(共享目录里的文件保留在磁盘上)。")) return;
  try {
    const r = await api("/api/workers/" + workerID, { method: "DELETE" });
    toast("已删除(" + (r.deleted_sessions || 0) + " 个会话)", "ok");
    if (workerBucketId() === workerID) { state.worker = "*"; rememberWorker("*"); }
    await loadWorkers();
    await loadSessions();
  } catch (err) { toast("删除失败:" + err.message, "err"); }
}

/* ---------------- 公约(worker.md)---------------- */
// 人编辑 = 授权本身;agent 想改必须由审批弹窗确认(session/gate.go)。
// 保存后正在跑的会话仍用自己那份快照 —— 这是刻意的(不让磁盘改动影响在跑的会话);
// 要让某个会话立刻换版就点 ↻,它会追加一条新简报。
async function openNoteEditor(workerID) {
  let data;
  try { data = await api("/api/workers/" + workerID + "/note"); }
  catch (e) { toast("读取公约失败:" + e.message, "err"); return; }
  closeWorkerMenu();
  const old = $("#wk-note-box");
  if (old) old.remove();
  const box = document.createElement("div");
  box.id = "wk-note-box";
  box.className = "wk-note";
  box.innerHTML =
    '<div class="wk-note-head">公约 · ' + esc(workerLabel(workerID)) + '</div>' +
    '<div class="wk-note-path" title="' + esc(data.path || "") + '">' + esc(data.path || "(该 worker 没有目录)") + '</div>' +
    '<textarea class="wk-note-area" spellcheck="false"></textarea>' +
    '<div class="wk-note-foot">' +
    '<span class="wk-note-tip">保存后运行中的会话不变;点 ↻ 刷新才装进当前会话</span>' +
    '<button class="wk-note-btn" id="wk-note-cancel">关闭</button>' +
    '<button class="wk-note-btn primary" id="wk-note-save">保存</button></div>';
  document.body.appendChild(box);
  const area = box.querySelector("textarea");
  area.value = data.content || "";
  box.querySelector("#wk-note-cancel").addEventListener("click", () => { const el = $("#wk-note-box"); if (el) el.remove(); });
  box.querySelector("#wk-note-save").addEventListener("click", async () => {
    try {
      await api("/api/workers/" + workerID + "/note", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: area.value }),
      });
      toast("公约已保存", "ok");
      const el = $("#wk-note-box");
      if (el) el.remove();
    } catch (err) { toast("保存失败:" + err.message, "err"); }
  });
  area.focus();
  area.setSelectionRange(area.value.length, area.value.length);
}

async function refreshCurrentNote() {
  if (!state.current) { toast("先打开一个会话", "err"); return; }
  try {
    const r = await api("/api/sessions/" + state.current + "/worker/refresh", { method: "POST" });
    toast(r.changed ? "已装好最新公约(下一条消息会带上一条新简报)" : "公约没有变化", "ok");
  } catch (e) { toast("刷新失败:" + e.message, "err"); }
}

/* ---------------- 事件绑定(由 app.js 的 init 调用)---------------- */
function initWorkerBar() {
  restoreWorker();
  const pick = $("#wk-pick"); // ‹ › 循环切换已删除,切换只走下拉
  if (pick) pick.addEventListener("click", (e) => { e.stopPropagation(); openWorkerMenu(pick); });
  const add = $("#btn-new-worker");
  if (add) add.addEventListener("click", (e) => { e.stopPropagation(); createWorker(); });
  document.addEventListener("click", (e) => {
    const menu = $("#worker-menu");
    if (menu && menu.classList.contains("open") && !menu.contains(e.target)) closeWorkerMenu();
  });
  loadWorkers();
}
