/* ---------------- 群聊 ---------------- */
// 群聊功能开关(UI 层面安全隐藏):false 时侧边栏群区与群入口全部隐藏,
// 后端群接口/代码原样保留(改回 true 即可重新启用,无需改代码),
// 已有群数据(群、成员、群日志)不受影响。
const GROUP_CHAT_ENABLED = false;
// state.currentGroup:当前打开的群 id(null = 会话视图);
// state.groupData:{group,members,messages};state.groupTimer:群视图轮询。
let groupMsgKey = "";

// 内置头像(web/ui/avatars/a001..a100.svg,入群随机分配,支持更换)
const GROUP_AVATARS = Array.from({ length: 100 }, (_, i) => "a" + String(i + 1).padStart(3, "0"));

/* ===== 头像统一渲染(群聊/单聊/个人信息同一套) ===== */
// 哈希取色:种子稳定 → 颜色固定(agent 用会话 id 做种子,改名/换机也不变)
const AVA_COLORS = ["#166534","#1e40af","#6b21a8","#9d174d","#9a3412","#155e75","#3730a3","#713f12","#991b1b","#065f46","#5b21b6","#475569"];
function hashColor(seed) {
  let h = 2166136261;
  const t = String(seed || "");
  for (let i = 0; i < t.length; i++) { h ^= t.charCodeAt(i); h = Math.imul(h, 16777619) >>> 0; }
  return AVA_COLORS[h % AVA_COLORS.length];
}
function firstChar(name) {
  const t = String(name || "").trim();
  return t ? Array.from(t)[0] : "M";
}
// 用户头像块:已上传 → 图片;未设置 → 首字"你"+哈希色回退。
// data-uav 记录基础类名,头像变更后 paintUserAvatarDom() 原地重绘;
// 全局点击委托:点任意用户头像 → 打开头像弹窗。
const USER_FALLBACK_BG = hashColor("user");
function userAvatarBox(cls) {
  if (state.userAvatar) {
    return '<div class="' + cls + ' clickable" data-uav="' + cls + '" title="点击修改用户头像">' +
      '<img src="' + state.userAvatar + '" alt="用户头像"></div>';
  }
  return '<div class="' + cls + '-fallback" data-uav="' + cls + '" title="点击设置用户头像" ' +
    'style="background:' + USER_FALLBACK_BG + ';color:#fff">你</div>';
}
// agent 头像块:统一默认头像(墨色 + "M"),不再从会话派生(标题首字 + id 哈希色)。
// state.agentAvatar 非空(设置里上传)→ 渲染图片。data-aav 记下 "类名|标签",
// 换头像后 paintAgentAvatarDom() 按它原地重绘。
// tag 传 "span"(侧栏条目那种)时不追加 -fallback:那一档只有一套盒子,底色靠
// .agent-ava 覆盖;消息行才有 c-avatar(图) / c-avatar-fallback(字)两版。
const AGENT_DEFAULT_LETTER = "M";
function agentAvatarBox(cls, tag) {
  const t = tag || "div";
  const s = state.sessions.find(x => x.session_id === state.current);
  const name = (s && s.title) || "Mini Code";
  const mark = cls + "|" + t;
  if (state.agentAvatar) {
    return "<" + t + ' class="' + cls + '" data-aav="' + mark + '" title="' + esc(name) + '">' +
      '<img src="' + state.agentAvatar + '" alt="agent 头像"></' + t + ">";
  }
  const solid = tag ? cls : cls + "-fallback";
  return "<" + t + ' class="' + solid + ' agent-ava" data-aav="' + mark + '" title="' + esc(name) + '">' +
    AGENT_DEFAULT_LETTER + "</" + t + ">";
}
// agent 头像变更后(设置里换图 / 恢复默认):页面里所有 agent 头像原地重绘
function paintAgentAvatarDom() {
  document.querySelectorAll("[data-aav]").forEach(el => {
    const parts = (el.dataset.aav || "").split("|");
    const tmp = document.createElement("div");
    tmp.innerHTML = agentAvatarBox(parts[0], parts[1] || "div");
    el.replaceWith(tmp.firstElementChild);
  });
}
// 头像变更(上传/移除)后:页面里所有用户头像原地重绘,不用整页刷新
function paintUserAvatarDom() {
  document.querySelectorAll("[data-uav]").forEach(el => {
    const tmp = document.createElement("div");
    tmp.innerHTML = userAvatarBox(el.dataset.uav);
    el.replaceWith(tmp.firstElementChild);
  });
}
// 全局委托:点任意位置的用户头像 → 打开用户头像弹窗
document.addEventListener("click", (e) => {
  if (e.target.closest("[data-uav]")) openUserAvatarModal();
});
// 成员头像块:有 avatar 渲染图片,旧数据无 avatar 时渲染首字母兜底
function avatarBox(m, cls, title) {
  const t = esc(title || "");
  if (m && m.avatar) {
    return '<div class="' + cls + '" title="' + t + '"><img src="/avatars/' + m.avatar + '.svg" alt=""></div>';
  }
  return '<div class="' + cls + '-fallback" title="' + t + '">' + esc(m ? String(m.name || "?").slice(0, 1) : "?") + "</div>";
}
// @成员高亮:@ 后字符与发送时解析规则一致,且命中当前成员群内名才高亮
function groupTextHTML(content, members) {
  const names = new Set((members || []).map(m => m.name));
  return esc(content).replace(/@([\u4e00-\u9fa5A-Za-z0-9_-]+)/g, (full, name) =>
    names.has(name) ? '<span class="mention">' + full + "</span>" : full);
}

async function loadGroups() {
  try { state.groups = await api("/api/groups"); }
  catch (e) { state.groups = []; }
  renderGroupList();
}

function groupRunning(g) { return (g.members || []).some(m => m.status === "running"); }

// 群区折叠状态(localStorage 记忆;拖拽会话入群时由 dragstart 自动展开,保证落点可见)
function setGroupCollapsed(c) {
  const sec = $("#group-sec"), t = $("#group-toggle");
  if (!sec || !t) return;
  if (sec.classList.contains("collapsed") === c) return;
  sec.classList.toggle("collapsed", c);
  t.textContent = c ? "▸" : "▾";
  try { localStorage.setItem("mc-group-collapsed", c ? "1" : "0"); } catch (e) {}
}

function renderGroupList() {
  const list = $("#group-list");
  const gs = state.groups || [];
  const gcnt = $("#group-count");
  if (gcnt) gcnt.textContent = gs.length ? String(gs.length) : "";
  list.innerHTML = "";
  if (gs.length === 0) {
    list.innerHTML = '<div class="empty-hint" style="padding:12px 8px">暂无群聊<br>点分区标题右侧 ＋ 创建</div>';
    return;
  }
  for (const g of gs) {
    const el = document.createElement("div");
    el.className = "group-item" + (g.group_id === state.currentGroup ? " active" : "");
    el.innerHTML =
      '<div class="g-title">' + (groupRunning(g) ? '<span class="pulse"></span>' : "") +
      "👥 " + esc(g.title) + ' <span class="g-count">' + (g.member_count || 0) + "</span></div>" +
      '<button class="rename-btn" title="群改名">✎</button>' +
      '<button class="del-btn" title="删除群">✕</button>';
    el.addEventListener("click", (e) => {
      if (e.target.classList.contains("del-btn") || e.target.classList.contains("rename-btn")) return;
      openGroup(g.group_id);
    });
    el.querySelector(".rename-btn").addEventListener("click", (e) => { e.stopPropagation(); openGroupSettings(g.group_id); });
    el.querySelector(".del-btn").addEventListener("click", async (e) => {
      e.stopPropagation();
      if (!confirm("确定删除群聊 " + g.title + " ?(群日志删除,成员会话保留)")) return;
      try {
        await api("/api/groups/" + g.group_id, { method: "DELETE" });
        if (state.currentGroup === g.group_id) exitGroupView();
        await loadGroups(); toast("群已删除", "ok");
      } catch (err) { toast("删除失败:" + err.message, "err"); }
    });
    // 拖会话到群 = 入群
    el.addEventListener("dragover", (e) => { e.preventDefault(); el.classList.add("drop-hint"); });
    el.addEventListener("dragleave", () => el.classList.remove("drop-hint"));
    el.addEventListener("drop", (e) => {
      e.preventDefault(); el.classList.remove("drop-hint");
      const p = readDragPayload(e);
      if (p && p.kind === "session") joinSessionToGroup(g.group_id, p.sid);
    });
    list.appendChild(el);
  }
}

async function openGroup(id) {
  state.current = null; saveSession(null);
  state.currentGroup = id;
  state.groupData = null; groupMsgKey = "";
  document.querySelector("main").classList.add("group-mode");
  setChatTitle("👥 …", "加载中");
  $("#input").placeholder = "发送到全群…(@成员名称 可指定接收)";
  // 群视图:恢复发送按钮
  $("#btn-send").style.display = "";
  $("#btn-stop").style.display = "none";
  // 清空容器前先摘掉会话视图的流式块/转圈,否则 state.live/state.pending
  // 悬空,回到会话后 delta 全部早退(转圈永不消除)
  liveRemove();
  pendingRemove();
  messagesEl().innerHTML = "";
  await refreshGroupView();
  clearInterval(state.groupTimer);
  state.groupTimer = setInterval(refreshGroupView, 4000);
  loadGroups();
  $("#input").focus();
}

async function refreshGroupView() {
  if (!state.currentGroup) return;
  let d;
  try { d = await api("/api/groups/" + state.currentGroup); }
  catch (e) { toast("群已不存在:" + e.message, "err"); exitGroupView(); return; }
  if (state.currentGroup !== d.group.group_id) return; // 等待期间切走了
  state.groupData = d;
  setChatTitle("👥 " + d.group.title,
    d.group.background ? d.group.background.slice(0, 60) : (d.members.length + " 名成员 · @成员 可指定接收"));
  renderGroupMessages(d.messages);
  renderGroupPanel(d.members);
}

function exitGroupView() {
  clearInterval(state.groupTimer); state.groupTimer = null;
  state.currentGroup = null; state.groupData = null;
  document.querySelector("main").classList.remove("group-mode");
  closeGroupPanel();
  resetToNew();
  $("#input").placeholder = inputPlaceholder();
}

// 群日志渲染(增量:仅当日志变化时重画)。
// 成员消息:头像可点 → 进该成员私聊;消息正文点击不跳转。
function renderGroupMessages(msgs) {
  const key = msgs.length ? (msgs[0].id + ":" + msgs[msgs.length - 1].id + ":" + msgs.length) : "empty";
  if (key === groupMsgKey) return;
  groupMsgKey = key;
  const m = messagesEl();
  const wasNear = nearBottom(); // 清空前快照(清空后 scrollTop=0,再测就没意义了)
  m.innerHTML = "";
  if (!msgs.length) {
    m.innerHTML = '<div class="placeholder"><div class="big">👥</div><p>群里还没有消息,发送一条开始协作<br>(成员完成任务/发消息都会出现在这里)</p></div>';
    return;
  }
  const members = (state.groupData && state.groupData.members) || [];
  const bySession = {}, byName = {};
  for (const mm of members) { bySession[mm.session_id] = mm; byName[mm.name] = mm; }
  for (const x of msgs) {
    const isUser = x.sender === "user", isSys = x.sender === "system";
    const div = document.createElement("div");
    div.className = "msg group-msg " + (isUser ? "group-user" : isSys ? "group-system" : "group-member");
    const ts = (x.created_at || "").slice(5, 16).replace("T", " ");
    const target = (x.target && x.target !== "all" && x.target !== "group") ? '<span class="g-target">→ <span class="mention">' + esc(x.target) + "</span></span>" : "";
    const tagCls = isUser ? "user" : (isSys ? "sys" : "member");
    const card = '<div class="m-card">' +
      '<div class="m-head"><span class="m-tag ' + tagCls + '">' + esc(x.sender) + "</span>" + target +
      '<span class="g-time">' + ts + "</span></div>" +
      '<div class="m-text">' + groupTextHTML(x.content, members) + "</div></div>";
    if (isUser) {
      // 用户消息:左侧用户头像(点头像可改头像,点正文不跳)
      div.innerHTML = userAvatarBox("g-avatar") + card;
    } else if (isSys) {
      div.innerHTML = card;
    } else {
      // 成员消息:头像(点 → 该成员的会话),正文不响应点击
      const mm = (x.sender_session && bySession[x.sender_session]) || byName[x.sender] || null;
      div.innerHTML = avatarBox(mm, "g-avatar", "查看该成员的会话") + card;
      const av = div.querySelector(".g-avatar, .g-avatar-fallback");
      if (x.sender_session && av) {
        av.addEventListener("click", () => selectSession(x.sender_session));
      }
    }
    m.appendChild(div);
  }
  // 群视图每次整体重画:用户重画前在底部才贴底,上滚回看时保持位置
  scrollBottom(wasNear, wasNear);
}

// 成员面板:头像 + 运行状态 + 查看/换头像/改名/移出 + 添加成员
function renderGroupPanel(members) {
  const p = $("#group-panel");
  if (!p.classList.contains("open")) return;
  $("#group-members-count").textContent = members.length;
  let html = "";
  for (const mm of members) {
    const dot = mm.status === "running" ? '<span class="pulse"></span>' : '<span class="pulse gray"></span>';
    const st = mm.status === "running" ? "运行中" : (mm.status === "idle" ? "空闲" : "离线");
    html += '<div class="gp-item">' +
      avatarBox(mm, "gp-avatar", "群内头像") +
      '<div class="gp-main" data-sid="' + mm.session_id + '" title="点击查看该成员会话">' +
      '<div class="gp-name">' + dot + " " + esc(mm.name) + "</div>" +
      '<div class="gp-sub">' + esc(st) + " · " + esc(mm.title || mm.session_id.slice(0, 10)) + "</div></div>" +
      '<div class="gp-acts">' +
      '<button data-act="avatar" data-sid="' + mm.session_id + '" data-name="' + esc(mm.name) + '" data-avatar="' + esc(mm.avatar || "") + '" title="换头像">🎨</button>' +
      '<button data-act="rename" data-sid="' + mm.session_id + '" data-name="' + esc(mm.name) + '" title="改群内名称">✎</button>' +
      '<button data-act="leave" data-sid="' + mm.session_id + '" data-name="' + esc(mm.name) + '" title="移出群聊">✕</button>' +
      "</div></div>";
  }
  html += '<div class="gp-add"><button id="gp-add-btn">＋ 添加成员(选一个会话)</button></div>';
  p.innerHTML = html;
  p.querySelectorAll(".gp-main").forEach(el =>
    el.addEventListener("click", () => selectSession(el.dataset.sid)));
  p.querySelectorAll("button[data-act]").forEach(b => b.addEventListener("click", async () => {
    const gid = state.currentGroup, sid = b.dataset.sid;
    if (b.dataset.act === "avatar") {
      openAvatarPicker(gid, sid, b.dataset.avatar);
      return;
    }
    if (b.dataset.act === "rename") {
      const n = prompt("改群内名称", b.dataset.name);
      if (n == null) return;
      try {
        await api("/api/groups/" + gid + "/members/" + sid + "/rename", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: n.trim() }),
        });
        toast("已改名", "ok"); refreshGroupView(); loadGroups();
      } catch (e) { toast("改名失败:" + e.message, "err"); }
    } else {
      if (!confirm("将 " + (b.dataset.name || sid) + " 移出群聊?(其会话历史保留)")) return;
      try {
        await api("/api/groups/" + gid + "/members/" + sid, { method: "DELETE" });
        toast("已移出", "ok"); refreshGroupView(); loadGroups();
      } catch (e) { toast("移出失败:" + e.message, "err"); }
    }
  }));
  const add = $("#gp-add-btn");
  if (add) add.addEventListener("click", openAddMemberModal);
}

function toggleGroupPanel() {
  const p = $("#group-panel");
  if (p.classList.contains("open")) { closeGroupPanel(); return; }
  $("#tasks-panel").classList.remove("open");
  p.classList.add("open");
  if (state.groupData) renderGroupPanel(state.groupData.members);
}
function closeGroupPanel() { $("#group-panel").classList.remove("open"); }

// 把某个会话加入指定群(拖拽/按钮共用)
async function joinSessionToGroup(gid, sid) {
  const name = prompt("群内名称(留空随机分配,如 哈基-123号)", "");
  if (name == null) return;
  try {
    const r = await api("/api/groups/" + gid + "/join", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sid, name: name.trim() }),
    });
    toast("已入群,群内名称:" + r.member.name, "ok");
    await loadGroups();
    if (state.currentGroup === gid) refreshGroupView();
  } catch (e) { toast("入群失败:" + e.message, "err"); }
}

// 建群弹窗(新建/设置共用;gm-mode: create | settings)
let gmMode = "create";
function openCreateGroupModal() {
  gmMode = "create";
  $("#gm-title").textContent = "新建群聊";
  $("#gm-name").value = "";
  $("#gm-background").value = "";
  $("#gm-save").textContent = "创建";
  $("#gm-delete").style.display = "none";
  $("#gm-clear").style.display = "none";
  $("#group-mask").classList.add("open");
  $("#gm-name").focus();
}
function openGroupSettings(gid) {
  const g = (state.groups || []).find(x => x.group_id === gid);
  gmMode = "settings";
  gmGid = gid;
  $("#gm-title").textContent = "群设置";
  $("#gm-name").value = g ? g.title : "";
  $("#gm-background").value = (state.groupData && state.groupData.group.group_id === gid)
    ? state.groupData.group.background : "";
  $("#gm-save").textContent = "保存(改名+背景)";
  $("#gm-delete").style.display = "";
  $("#gm-clear").style.display = "";
  $("#group-mask").classList.add("open");
}
let gmGid = "";
$("#gm-close").addEventListener("click", () => $("#group-mask").classList.remove("open"));
$("#group-mask").addEventListener("click", (e) => {
  if (e.target.id === "group-mask") e.target.classList.remove("open");
});
$("#gm-delete").addEventListener("click", async () => {
  if (!gmGid) return;
  if (!confirm("确定删除该群?(群日志删除,成员会话保留)")) return;
  try {
    await api("/api/groups/" + gmGid, { method: "DELETE" });
    $("#group-mask").classList.remove("open");
    if (state.currentGroup === gmGid) exitGroupView();
    await loadGroups(); toast("群已删除", "ok");
  } catch (e) { toast("删除失败:" + e.message, "err"); }
});
$("#gm-clear").addEventListener("click", async () => {
  if (!gmGid) return;
  if (!confirm("确定清空该群的全部聊天记录?\n群本身、成员和群背景保留,成员的私聊会话历史不受影响。")) return;
  try {
    const r = await api("/api/groups/" + gmGid + "/messages", { method: "DELETE" });
    $("#group-mask").classList.remove("open");
    await loadGroups();
    if (state.currentGroup === gmGid) refreshGroupView();
    toast("已清空群记录(删除 " + (r.cleared || 0) + " 条)", "ok");
  } catch (e) { toast("清空失败:" + e.message, "err"); }
});
$("#gm-save").addEventListener("click", async () => {
  const title = $("#gm-name").value.trim();
  const bg = $("#gm-background").value.trim();
  if (!title) { toast("群名称不能为空", "err"); return; }
  if (gmMode === "create") {
    try {
      const r = await api("/api/groups", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title, background: bg }),
      });
      $("#group-mask").classList.remove("open");
      await loadGroups();
      openGroup(r.group_id);
      toast("群已创建,拖会话到群(或 成员→添加成员)即可入群", "ok");
    } catch (e) { toast("建群失败:" + e.message, "err"); }
  } else {
    try {
      await api("/api/groups/" + gmGid + "/rename", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      });
      await api("/api/groups/" + gmGid + "/background", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ background: bg }),
      });
      $("#group-mask").classList.remove("open");
      await loadGroups();
      if (state.currentGroup === gmGid) refreshGroupView();
      toast("已保存(群背景从成员下一个任务生效)", "ok");
    } catch (e) { toast("保存失败:" + e.message, "err"); }
  }
});
function renameGroup(g) { if (g) openGroupSettings(g.group_id); }

// 添加成员弹窗
function openAddMemberModal() {
  const sel = $("#am-session");
  sel.innerHTML = state.sessions.map(s =>
    '<option value="' + s.session_id + '">' + esc(s.title || s.session_id.slice(0, 12)) +
    (s.active ? " (运行中)" : "") + "</option>").join("");
  $("#am-name").value = "";
  $("#add-member-mask").classList.add("open");
}
$("#am-cancel").addEventListener("click", () => $("#add-member-mask").classList.remove("open"));
$("#add-member-mask").addEventListener("click", (e) => {
  if (e.target.id === "add-member-mask") e.target.classList.remove("open");
});
$("#am-add").addEventListener("click", async () => {
  const gid = state.currentGroup;
  if (!gid) return;
  const sid = $("#am-session").value;
  if (!sid) { toast("请选择会话", "err"); return; }
  const name = $("#am-name").value.trim();
  try {
    const r = await api("/api/groups/" + gid + "/join", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: sid, name }),
    });
    $("#add-member-mask").classList.remove("open");
    toast("已入群,群内名称:" + r.member.name, "ok");
    await loadGroups();
    refreshGroupView();
  } catch (e) { toast("入群失败:" + e.message, "err"); }
});

// 换头像弹窗:100 个内置头像(入群时随机分配),点选确认后调 API
let avPicker = { gid: "", sid: "", sel: "" };
function openAvatarPicker(gid, sid, current) {
  avPicker = { gid, sid, sel: current || "" };
  const grid = $("#avatar-grid");
  if (!grid.childElementCount) {
    for (const id of GROUP_AVATARS) {
      const b = document.createElement("button");
      b.className = "avatar-opt";
      b.dataset.id = id;
      b.innerHTML = '<img src="/avatars/' + id + '.svg" alt="" loading="lazy">';
      b.addEventListener("click", () => {
        avPicker.sel = id;
        grid.querySelectorAll(".avatar-opt").forEach(o => o.classList.toggle("sel", o.dataset.id === id));
      });
      grid.appendChild(b);
    }
  }
  grid.querySelectorAll(".avatar-opt").forEach(o => o.classList.toggle("sel", o.dataset.id === avPicker.sel));
  $("#avatar-mask").classList.add("open");
}
$("#av-cancel").addEventListener("click", () => $("#avatar-mask").classList.remove("open"));
$("#avatar-mask").addEventListener("click", (e) => {
  if (e.target.id === "avatar-mask") e.target.classList.remove("open");
});
$("#av-confirm").addEventListener("click", async () => {
  if (!avPicker.gid || !avPicker.sid) return;
  if (!avPicker.sel) { toast("请先选一个头像", "err"); return; }
  try {
    await api("/api/groups/" + avPicker.gid + "/members/" + avPicker.sid + "/avatar", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ avatar: avPicker.sel }),
    });
    $("#avatar-mask").classList.remove("open");
    toast("已更换头像", "ok");
    refreshGroupView(); loadGroups();
  } catch (e) { toast("换头像失败:" + e.message, "err"); }
});

// 用户头像弹窗:上传(jpg/png/webp,≤4MB)→ canvas 居中裁 256×256 JPEG → 存服务端配置
function openUserAvatarModal() {
  paintUaPreview();
  $("#user-avatar-mask").classList.add("open");
}
function paintUaPreview() {
  $("#ua-preview").innerHTML = userAvatarBox("ua-ava");
}
function paintSettingsUserAvatar() {
  const el = $("#c-user-ava");
  if (el) el.innerHTML = userAvatarBox("c-avatar");
}
// 文件 → 256×256 方形 data URL(居中裁切;透明图铺白底;JPEG 0.85)
function fileToAvatarDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("文件读取失败"));
    reader.onload = () => {
      const img = new Image();
      img.onload = () => {
        const c = document.createElement("canvas");
        c.width = 256; c.height = 256;
        const ctx = c.getContext("2d");
        ctx.fillStyle = "#fff";
        ctx.fillRect(0, 0, 256, 256);
        const side = Math.min(img.width, img.height);
        const sx = (img.width - side) / 2, sy = (img.height - side) / 2;
        ctx.drawImage(img, sx, sy, side, side, 0, 0, 256, 256);
        resolve(c.toDataURL("image/jpeg", 0.85));
      };
      img.onerror = () => reject(new Error("图片解析失败"));
      img.src = reader.result;
    };
    reader.readAsDataURL(file);
  });
}
async function saveUserAvatar(dataURL) {
  await api("/api/avatar", {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ avatar: dataURL }),
  });
  state.userAvatar = dataURL;
  paintUserAvatarDom();
  paintSettingsUserAvatar();
}
$("#ua-close").addEventListener("click", () => $("#user-avatar-mask").classList.remove("open"));
$("#user-avatar-mask").addEventListener("click", (e) => {
  if (e.target.id === "user-avatar-mask") e.target.classList.remove("open");
});
$("#ua-upload").addEventListener("click", () => $("#ua-file").click());
$("#ua-file").addEventListener("change", async (e) => {
  const f = (e.target.files || [])[0];
  e.target.value = "";
  if (!f) return;
  if (!/image\/(jpeg|png|webp)/.test(f.type)) { toast("仅支持 jpg / png / webp", "err"); return; }
  if (f.size > 4 * 1024 * 1024) { toast("图片超过 4MB", "err"); return; }
  try {
    const dataURL = await fileToAvatarDataURL(f);
    await saveUserAvatar(dataURL);
    toast("头像已更新", "ok");
    $("#user-avatar-mask").classList.remove("open");
  } catch (err) { toast("头像保存失败:" + err.message, "err"); }
});
$("#ua-clear").addEventListener("click", async () => {
  if (!state.userAvatar) { toast("当前未设置头像", ""); return; }
  try {
    await saveUserAvatar("");
    toast("已移除头像(回退默认)", "ok");
    $("#user-avatar-mask").classList.remove("open");
  } catch (e) { toast("移除失败:" + e.message, "err"); }
});
// 启动时拉取用户头像(刷新/多开场景保持同步)
async function loadUserAvatar() {
  try {
    const r = await api("/api/avatar");
    state.userAvatar = r.avatar || "";
  } catch (e) { /* 头像加载失败不阻塞:用回退 */ }
  paintSettingsUserAvatar();
}

// 群发送:@成员名开头 → 指定该成员,否则全群
async function groupSend() {
  const input = $("#input");
  const text = input.value.trim();
  if (!text) return;
  const gid = state.currentGroup;
  // @解析在服务端:正文任意位置 @成员名 → 只投递被 @ 的成员;无 @ → 全群广播。
  input.value = ""; autoGrow();
  try {
    const r = await api("/api/groups/" + gid + "/messages", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content: text, target: "" }),
    });
    toast("已送达 " + r.targets + " 个成员", "ok");
    refreshGroupView();
  } catch (e) {
    toast("发送失败:" + e.message, "err");
    input.value = text; autoGrow();
  }
}

/* ---------------- agent 头像(全局就一张脸,设置里可换)---------------- */
// 与用户头像两套独立存储:agent 这张存 config.agent_avatar,走 /api/agent-avatar。
// 没设置就是内置默认那颗(墨色 + "M"),设置里换了立刻原地重绘,不用刷新。
function paintSettingsAgentAvatar() {
  const el = $("#c-agent-ava");
  if (el) el.innerHTML = agentAvatarBox("c-avatar");
}
async function saveAgentAvatar(dataURL) {
  await api("/api/agent-avatar", {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ avatar: dataURL }),
  });
  state.agentAvatar = dataURL;
  paintAgentAvatarDom();
  paintSettingsAgentAvatar();
  paintChatAva("");
}
// 启动时拉取(刷新/多开保持同步);拉不到就用默认头像,不阻塞界面
async function loadAgentAvatar() {
  try {
    const r = await api("/api/agent-avatar");
    state.agentAvatar = r.avatar || "";
  } catch (e) { /* 用默认 */ }
  // 会话列表比这一步先渲染,已经画好的侧栏头像在这里补一次
  paintAgentAvatarDom();
  paintSettingsAgentAvatar();
}
const _agentAvaBtn = $("#btn-agent-ava");
if (_agentAvaBtn) _agentAvaBtn.addEventListener("click", () => $("#agent-ava-file").click());
const _agentAvaClear = $("#btn-agent-ava-clear");
if (_agentAvaClear) _agentAvaClear.addEventListener("click", async () => {
  if (!state.agentAvatar) { toast("当前已经是默认头像", ""); return; }
  try {
    await saveAgentAvatar("");
    toast("已恢复默认头像", "ok");
  } catch (e) { toast("恢复失败:" + e.message, "err"); }
});
const _agentAvaFile = $("#agent-ava-file");
if (_agentAvaFile) _agentAvaFile.addEventListener("change", async (e) => {
  const f = (e.target.files || [])[0];
  e.target.value = "";
  if (!f) return;
  if (!/image\/(jpeg|png|webp)/.test(f.type)) { toast("仅支持 jpg / png / webp", "err"); return; }
  if (f.size > 4 * 1024 * 1024) { toast("图片超过 4MB", "err"); return; }
  try {
    const dataURL = await fileToAvatarDataURL(f); // 复用用户头像那套:居中裁 256×256
    await saveAgentAvatar(dataURL);
    toast("agent 头像已更新", "ok");
  } catch (err) { toast("保存失败:" + err.message, "err"); }
});
