/* ---------------- Worker(工作大类)----------------
 * 一个 worker = 一条工作线:一个共享工作目录 + 一份共享公约(存在 worker 记录里,带版本)。
 * 这里的职责:切换条(‹ worker ›)、按 worker 过滤会话列表、把会话移动到
 * worker(原"归档"按钮的位置)、以及公约的查看/编辑。
 *
 * 会话侧的注入规则在服务端(session/worker.go):每个任务开始时检查一次 —— 没归属
 * worker 就插一条加入提醒;已归属但持有的公约版本落后,就插一条最新公约。都是往
 * 对话尾部追加(和用量提醒、压缩摘要同一套路),前缀缓存不受影响;消息发送成功后
 * 才随对话一起落库,所以那一条就是它的"已消费"凭证。保存公约即升版本,同 worker 的
 * 会话下一轮各自收到更新,不需要这里的任何按钮。
 */
const LS_WORKER_KEY = "mini_code.worker";

function workerBucketId() {
  const id = state.worker == null ? "" : state.worker;
  return id === "*" ? "" : id; // 老版本存过 "*",一律归到"未分配"
}

function restoreWorker() {
  try {
    const v = localStorage.getItem(LS_WORKER_KEY);
    if (v != null) state.worker = v === "*" ? "" : v; // 老版本可能存着 "*"
  } catch (e) { /* 隐私模式 / 禁用存储:留在"未分配" */ }
}
function rememberWorker(id) {
  try { localStorage.setItem(LS_WORKER_KEY, id); } catch (e) {}
}

// workerBuckets 是切换条的全部序列:各 worker → 未分配。未分配排最后,它是"没归属"
// 的兜底档,不该抢在真正的工作线前面。没有"全部"这一档,内置分桶(旧版"已归档")也
// 不再列出 —— 归档按钮早就没了,那个桶也不会再被创建。
function workerBuckets() {
  const out = (state.workers || []).filter(x => !x.builtin).slice();
  out.push({ worker_id: "", name: "未分配", sessions: state.unassigned || 0, active: state.unassignedActive || 0 });
  return out;
}

function workerLabel(id) {
  if (id === "" || id == null || id === "*") return "未分配";
  const w = (state.workers || []).find(x => x.worker_id === id);
  return w ? w.name : "未知 worker";
}

function workerEmptyHint() {
  if (workerBucketId() === "") return '暂无未分配会话<br>新建会话时选「未分配」即由 agent 自己判断归属';
  return '这个 worker 还没有会话<br>点「＋ 新建会话」直接在这里开工,或把别的会话 📁 移进来';
}

async function loadWorkers() {
  let got = true;
  try {
    const r = await api("/api/workers");
    state.workers = r.workers || [];
    state.unassigned = r.unassigned || 0;
    state.unassignedActive = r.unassigned_active || 0;
  } catch (e) {
    // 服务未启用 worker(老服务端 / 未接线):切换条退化成只读提示
    got = false;
    state.workers = state.workers || [];
    state.unassigned = state.unassigned || 0;
    state.unassignedActive = 0;
  }
  // 选中的 worker 被删了:回到"未分配",别把列表卡成空。只在真的拿到列表时才校准,
  // 否则一次网络抖动就会把用户正在看的 worker 踢走。
  const id = workerBucketId();
  if (got && id !== "" && !state.workers.some(w => w.worker_id === id)) {
    state.worker = "";
    rememberWorker("");
  }
  paintWorkerBar();
}

function paintWorkerBar() {
  const name = $("#wk-name");
  if (!name) return;
  const id = workerBucketId();
  const w = (state.workers || []).find(x => x.worker_id === id);
  const count = id === "" ? (state.unassigned || 0) : (w ? w.sessions || 0 : 0);
  name.textContent = (w ? w.name : workerLabel(id)) + (count ? " · " + count : "");

  const sub = $("#worker-sub");
  if (!sub) return;
  if (!w || (!w.work_dir && !w.reserved)) {
    // "未分配"/"已归档"没有共享目录,原来那行解释性小字("未分配:新会话会自己判断…")是纯
    // 噪音,还老跟下拉菜单打架 —— 删了。空的 .worker-sub 由 CSS 的 :empty 折叠,不留空隙。
    sub.innerHTML = "";
  } else {
    // 固定的「其他」能写公约、能改摘要,但删不掉、名字也写死了 —— 不渲染 🗑 和改名。
    // 「＋ 会话」所有 worker 都有:在这里建的会话默认就归这个 worker(见 send.js)。
    sub.innerHTML =
      '<button class="wk-sub-btn" id="wk-note-btn" title="查看/编辑公约(共享给这个 worker 的全部会话)">📄 公约</button>' +
      '<button class="wk-sub-btn" id="wk-summary-btn" title="改写摘要(新会话靠它决定加入哪个 worker)">✎ 摘要</button>' +
      (w.reserved ? "" :
        '<button class="wk-sub-btn" id="wk-rename-btn" title="改名(只改名字:目录不动,会话里的路径不受影响)">✎ 名称</button>') +
      '<button class="wk-sub-btn" id="wk-new-sess-btn" title="在这个 worker 里新建会话(发出第一条消息即归属这里)">＋ 会话</button>' +
      (w.reserved ? "" :
        '<button class="wk-sub-btn wk-sub-danger" id="wk-del-btn" title="删除工作区及其全部会话(需手打确认语;目录保留)">🗑</button>');
    const bind = (sel, fn) => { const el = $(sel); if (el) el.addEventListener("click", fn); };
    bind("#wk-note-btn", () => openNoteEditor(w.worker_id));
    bind("#wk-summary-btn", () => editSummary(w.worker_id));
    bind("#wk-rename-btn", () => renameWorker(w.worker_id));
    bind("#wk-new-sess-btn", newSessionInWorker);
    bind("#wk-del-btn", () => deleteWorker(w.worker_id));
  }
}

// 小「＋ 会话」:草稿**明确挂到当前 worker**。归属只有 send.js 一处来源
// (body.worker_id = state.worker),而 resetToNew 不碰它,所以这里要把它钉成当前区。
// "已归档"是个虚拟桶,不能当归属,那种情况下草稿留作未分配。
function newSessionInWorker() {
  if (state.currentGroup && typeof exitGroupView === "function") exitGroupView();
  const id = workerBucketId();
  if (id && id !== "*") {
    state.worker = id;
    rememberWorker(id);
  }
  state.unassignedActive = 0;
  resetToNew();
  const input = $("#input");
  if (input) input.focus();
}

// 顶部大「＋ 新建会话」:建一个**不属于任何 worker** 的会话,归属交给会话自己判断 ——
// 没有 worker 时后端会注入 join 提醒(session/worker.go 的 BuildWorkerNotice),
// 由 agent 决定 join 哪条线 / create 新线 / 干脆不处理。
// 不清 state.worker 的话,大按钮会跟着上一个选中的区走,和小「＋ 会话」变成同一件事。
function detachWorkerForNewSession() {
  state.worker = "";
  rememberWorker("");
  state.unassignedActive = 1; // 会话还没建出来,也要让"未分配"出现在切换器里
  paintWorkerBar();
}

async function switchWorker(id) {
  const changed = id !== workerBucketId();
  state.worker = id;
  rememberWorker(id);
  closeWorkerMenu();
  await loadSessions(null, true);
  if (!changed) return;
  // 换了区,右边展示的会话也跟着换:选中新列表的第一条(服务端按 updated_at 倒序,
  // 就是侧栏最上面那条)。空区回到"新会话"草稿 —— 否则还挂着上一个区的会话,
  // 看着像没切过去。
  const first = (state.sessions || [])[0];
  if (first) selectSession(first.session_id);
  else resetToNew();
}

/* ---------------- 侧栏跟着会话走 ---------------- */
// followSessionWorker:会话归属哪个 worker,切换条就切到哪个区。
// 和 switchWorker 的区别是它**不**去挑「新区的第一条会话」——要看的还是这一条,
// 谁调用谁负责 selectSession。返回是否真的换了区。
async function followSessionWorker(workerID) {
  const id = workerID || "";
  // 已经在这个区就别折腾了:打开会话是高频动作,不该为它多打一次 /api/workers。
  if (id === workerBucketId() && (id === "" || (state.workers || []).some(w => w.worker_id === id))) return false;
  // 先拉一次 worker 列表:agent 可能在会话里刚 create 出一条新线,列表里没有它,
  // 直接切过去只会是个空区。
  await loadWorkers();
  if (id !== "" && !(state.workers || []).some(w => w.worker_id === id)) return false; // 已被删掉:别切
  if (id === workerBucketId()) return false;
  state.worker = id;
  rememberWorker(id);
  closeWorkerMenu();
  await loadSessions(null, true);
  return true;
}

// resyncSessionWorker:agent 在会话里自己改了归属(workers 工具的 join/create/leave)。
// 服务端不为这个单独推事件,所以只能在工具跑完后把这条会话重新读一遍对齐 ——
// 否则「新建会话 → agent 帮你 join 了 worker」之后侧栏还停在「未分配」,看着像没生效。
async function resyncSessionWorker(sid) {
  if (!sid) return;
  try {
    const data = await api("/api/sessions/" + sid);
    // 读回来的这段时间用户可能已经切走了:别拿旧会话的归属去改现在的侧栏。
    if (state.current !== sid) return;
    await followSessionWorker((data && data.session && data.session.worker_id) || "");
  } catch (e) { /* 对齐失败就算了:下一次列表轮询照常收敛 */ }
}

/* ---------------- worker 菜单(切换 / 新建)---------------- */
// 菜单图标:未分配是一格虚线框,真 worker 一律文件夹。
// 用内联 SVG 不用 emoji —— emoji 在不同字体下大小/基线全飘,颜色也没法跟主题走。
const WK_ICONS = {
  "": '<rect x="4.2" y="4.2" width="15.6" height="15.6" rx="3.2" stroke-dasharray="3.6 3"/>',
};
const WK_ICON_FOLDER = '<path d="M4 7.2A2.2 2.2 0 0 1 6.2 5h3.4l1.8 2.2H17.8A2.2 2.2 0 0 1 20 9.4v7.4A2.2 2.2 0 0 1 17.8 19H6.2A2.2 2.2 0 0 1 4 16.8V7.2Z"/>';
function wkIcon(id) {
  const d = Object.prototype.hasOwnProperty.call(WK_ICONS, id) ? WK_ICONS[id] : WK_ICON_FOLDER;
  return '<svg class="wk-menu-ico" viewBox="0 0 24 24" width="16" height="16" fill="none" ' +
    'stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    d + "</svg>";
}
// worker 菜单的状态点:颜色语义与会话列表一致 —— 黄(呼吸)=还有会话在跑,
// 绿=这个区的事都跑完了。没有会话就不上色,但格子照样占住(见 layers.css),
// 否则数量药丸那列会随有没有点而左右漂。
function wkDot(b) {
  const busy = b.active || 0;
  const cls = busy > 0 ? "running" : ((b.sessions || 0) > 0 ? "done" : "none");
  const tip = busy > 0 ? busy + " 个会话执行中" : ((b.sessions || 0) > 0 ? "没有会话在跑" : "还没有会话");
  return '<span class="wk-menu-dot ' + cls + '" title="' + tip + '"></span>';
}
function closeWorkerMenu() {
  const m = $("#worker-menu");
  if (m) { m.classList.remove("open"); m.innerHTML = ""; }
  const note = $("#wk-note-box");
  if (note) note.remove();
}

// 摆放下拉。菜单挂在 body 下(见 index.html 的注释:侧栏的 backdrop-filter 会把
// 后代 fixed 元素关进侧栏盒子,菜单会被侧栏右缘剪掉)。这里再按视口夹一次上下左右,
// 并把上缘的尖角挪回锚点下方 —— 窄窗口、贴近屏幕底部时都不会被切。
function placeWorkerMenu(menu, anchor) {
  const r = anchor && anchor.getBoundingClientRect
    ? anchor.getBoundingClientRect()
    : { left: 12, bottom: 120, width: 200 };
  const w = menu.offsetWidth, h = menu.offsetHeight;
  const left = Math.max(8, Math.min(r.left, Math.max(8, window.innerWidth - w - 8)));
  const top = Math.max(8, Math.min(r.bottom + 4, Math.max(8, window.innerHeight - h - 8)));
  menu.style.left = left + "px";
  menu.style.top = top + "px";
  const tip = r.left + r.width / 2 - left - 5; // ::before 是 11px 宽的小方块
  menu.style.setProperty("--wk-arrow-x", Math.max(20, Math.min(tip, Math.max(20, w - 32))) + "px");
}

function openWorkerMenu(anchor) {
  const menu = $("#worker-menu");
  if (!menu) return;
  if (menu.classList.contains("open")) { closeWorkerMenu(); return; }
  const id = workerBucketId();
  // 条上已经写着 worker | 未分配 · 3,菜单里不再重复一行"切换 Worker"小标题
  let html = "";
  for (const w of workerBuckets()) {
    const cur = w.worker_id === id;
    html += '<div class="wk-menu-item' + (cur ? " cur" : "") + '" data-id="' + esc(w.worker_id) +
      '" title="' + esc(w.name) + '">' +
      wkIcon(w.worker_id) +
      '<span class="wk-menu-name">' + esc(w.name) + '</span>' +
      wkDot(w) +
      '<span class="wk-menu-n">' + (w.sessions || 0) + '</span>' +
      // 勾永远渲染(没有就留空):占住同一格,数量药丸在每行才对齐
      '<span class="wk-menu-tick">' + (cur ? "✓" : "") + "</span></div>";
  }
  menu.innerHTML = html;
  menu.classList.add("open");
  placeWorkerMenu(menu, anchor);
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
    html += '<div class="wk-menu-item' + (w.worker_id === cur ? " cur" : "") + '" data-id="' + esc(w.worker_id) + '">' +
      wkIcon(w.worker_id) +
      '<span class="wk-menu-name">' + esc(w.name) + '</span>' +
      '<span class="wk-menu-n">' + (w.worker_id === cur ? "当前" : "") + "</span></div>";
  }
  menu.innerHTML = html;
  menu.classList.add("open");
  placeWorkerMenu(menu, anchor);
  const target = sess.session_id;
  menu.querySelectorAll(".wk-menu-item").forEach(el => {
    el.addEventListener("click", () => {
      closeWorkerMenu();
      moveSessionToWorker(target, el.dataset.id, cur);
    });
  });
}

/* ---------------- 新建 / 改名 / 删除 ---------------- */
async function createWorker() {
  const name = (prompt("新建 Worker(工作大类):名称\n会在工作区里创建 worker-<名字> 目录(共享公约存在 worker 记录里)", "") || "").trim();
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

async function renameWorker(workerID) {
  const w = (state.workers || []).find(x => x.worker_id === workerID);
  const cur = w ? w.name || "" : "";
  const ans = prompt("Worker 改名。\n只改名字:共享目录的名字不动(会话、工具结果里已经写满了那条绝对路径),会话里显示的 worker 名字会一起更新。", cur);
  if (ans == null) return; // 取消:什么都不写
  const name = ans.trim();
  if (!name || name === cur) return;
  try {
    await api("/api/workers/" + workerID + "/rename", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    toast("已改名为:" + name, "ok");
    await loadWorkers();
  } catch (err) { toast("改名失败:" + err.message, "err"); }
}

// WK_DELETE_CONFIRM is what the user has to type, verbatim, before a worker goes
// away. Deleting one takes every session in it with it, so a stray click on 🗑
// must not be enough — the click-only confirm() it used to have was one reflex away from mass deletion.
const WK_DELETE_CONFIRM = "我确认删除工作区";

async function deleteWorker(workerID) {
  const w = (state.workers || []).find(x => x.worker_id === workerID);
  const n = w ? w.sessions || 0 : 0;
  const typed = (prompt(
    "删除工作区「" + (w ? w.name : workerID) + "」会一并删掉里面的 " + n +
    " 个会话(共享目录里的文件保留在磁盘)。\n要删请一字不差地输入:" + WK_DELETE_CONFIRM,
    "") || "").trim();
  if (typed !== WK_DELETE_CONFIRM) {
    if (typed) toast("没删:确认语要一模一样(" + WK_DELETE_CONFIRM + ")", "err");
    return;
  }
  // 展示区里那条可能正是这个区名下的 —— 它会被一起删掉,删完得回草稿,别对着
  // 一条已经不存在的会话。先记住它归属哪个区(列表里的 worker_id)。
  const shown = (state.sessions || []).find(x => x.session_id === state.current);
  // 删除要连带干掉名下所有会话,服务端同步做完才回话;这几秒最不该看到的就是
  // "还停留在旧列表上"。所以先清空摆骨架,🗑 同时置灰防重复点。
  const delBtn = $("#wk-del-btn");
  if (delBtn) { delBtn.disabled = true; delBtn.textContent = "删除中…"; }
  beginSessionsRefresh();
  try {
    const r = await api("/api/workers/" + workerID, { method: "DELETE" });
    toast("已删除(" + (r.deleted_sessions || 0) + " 个会话)", "ok");
    if (workerBucketId() === workerID) { state.worker = ""; rememberWorker(""); }
    await loadWorkers();
    await loadSessions(null, true);
    if (shown && shown.worker_id === workerID) resetToNew();
  } catch (err) {
    toast("删除失败:" + err.message, "err");
    // 没删成也得把列表拉回真实状态,别停在骨架或空列表上。
    await loadSessions(null, true);
  } finally {
    // 子栏多半已被 loadWorkers 重画过(那颗 🗑 是新节点、默认就能点),这里只
    // 在节点还活着时恢复,免得把一个已经不存在的按钮改来改去。
    const btn = $("#wk-del-btn");
    if (btn) { btn.disabled = false; btn.textContent = "🗑"; }
  }
}

/* ---------------- 公约(存在 worker 记录里,带版本)---------------- */
// 公约只有这一条写路径:人在这里改 = 授权本身。模型侧只有 note_read,tools 里
// 没有 note_write(它会影响同 worker 的全部会话,不该由 agent 代改)。
// 保存即升版本:同 worker 的会话下一轮各自收到一条更新消息,已有对话不动。
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
    '<div class="wk-note-path">当前版本:v' + (data.ver || 0) + '</div>' +
    '<textarea class="wk-note-area" spellcheck="false"></textarea>' +
    '<div class="wk-note-foot">' +
    '<span class="wk-note-tip">保存后同 worker 的会话下一轮自动收到更新</span>' +
    '<button class="wk-note-btn" id="wk-note-cancel">关闭</button>' +
    '<button class="wk-note-btn primary" id="wk-note-save">保存</button></div>';
  document.body.appendChild(box);
  const area = box.querySelector("textarea");
  area.value = data.content || "";
  box.querySelector("#wk-note-cancel").addEventListener("click", () => { const el = $("#wk-note-box"); if (el) el.remove(); });
  box.querySelector("#wk-note-save").addEventListener("click", async () => {
    try {
      const r = await api("/api/workers/" + workerID + "/note", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: area.value }),
      });
      toast("公约已保存到 v" + (r.ver || 1), "ok");
      const el = $("#wk-note-box");
      if (el) el.remove();
      await loadWorkers();
    } catch (err) { toast("保存失败:" + err.message, "err"); }
  });
  area.focus();
  area.setSelectionRange(area.value.length, area.value.length);
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
