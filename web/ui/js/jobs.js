/* ---------------- 后台命令(job)面板 ---------------- */
// 当前会话派发的后台命令(exec run_in_background / 前台等待超时转后台)。
// 入口按钮只在当前会话有可见 job 时出现;面板分「运行中 / 已结束」两段(可折叠),
// 运行中行带两按停止,已结束行 ⌄ 就地展开输出。面板开着才轮询(2s),关了零开销。
state.jobs = [];
state.jobsTimer = null;
let jobsSid = null;     // 面板当前服务的会话;切会话自动关面板(数据不跨会话)
let killArmed = null;   // {key, timer}:两按确认中的 job
let secClosed = {};     // 分组折叠状态(面板会话内保持)
let outOpen = null;     // 当前展开输出的 job id(同时只开一个)
let outCache = {};      // job id -> 已取到的输出文本

function currentSessionSummary() {
  return (state.sessions || []).find(x => x.session_id === state.current) || null;
}

// paintJobsBtn 用会话摘要里的 jobs_running/jobs_total 刷新入口按钮(显隐+计数)。
// 由 renderSessions 调:列表每刷一次,徽标就跟着收敛一次。
function paintJobsBtn() {
  const btn = $("#btn-jobs");
  if (!btn) return;
  const s = currentSessionSummary();
  const running = s ? (s.jobs_running || 0) : 0;
  const total = s ? (s.jobs_total || 0) : 0;
  btn.classList.toggle("show", !!state.current && !state.currentGroup && total > 0);
  const cnt = $("#jobs-count");
  if (cnt) {
    cnt.textContent = running > 0 ? running : total;
    cnt.classList.toggle("hot", running > 0);
  }
  if (jobsSid && jobsSid !== state.current) closeJobsPanel();
}

async function loadJobs() {
  if (!state.current) { state.jobs = []; renderJobsPanel(); return; }
  try {
    const r = await api("/api/sessions/" + state.current + "/jobs");
    state.jobs = r.jobs || [];
  } catch (e) { state.jobs = []; }
  // 展开中的输出跟着轮询刷新,不动 outOpen/outCache 状态。
  if (outOpen) refreshOut(outOpen, true);
  renderJobsPanel();
}

// jobElapsed 运行中行用当前时刻,结束行用 ended_at;小时封顶(超过一小时的 job 已经是异常)。
function jobElapsed(j) {
  const t0 = Date.parse(j.started_at || "");
  if (isNaN(t0)) return "";
  const end = j.status === "running" ? Date.now() : (j.ended_at ? Date.parse(j.ended_at) : Date.now());
  let sec = Math.max(0, Math.floor((end - t0) / 1000));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60);
  sec = sec % 60;
  if (h > 0) return h + "时" + m + "分";
  if (m > 0) return m + "分" + sec + "秒";
  return sec + "秒";
}
const JOB_STATUS = { running: "运行中", completed: "已完成", failed: "失败", killed: "已终止", lost: "丢失" };
const JOB_DOT = { running: "running", completed: "completed", failed: "failed", killed: "idle", lost: "idle" };

function renderJobsPanel() {
  const panel = $("#jobs-panel");
  if (!panel || !panel.classList.contains("open")) return;
  const jobs = state.jobs || [];
  const live = jobs.filter(j => j.status === "running").sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at));
  const settled = jobs.filter(j => j.status !== "running")
    .sort((a, b) => Date.parse(b.ended_at || b.started_at) - Date.parse(a.ended_at || a.started_at));
  const row = (j) => {
    const st = JOB_STATUS[j.status] || j.status;
    const act = j.status === "running"
      ? '<button class="j-stop job-stop" data-id="' + esc(j.id) + '" title="停止(按两次确认)">停</button>'
      : '<button class="j-btn job-out" data-id="' + esc(j.id) + '" title="展开/收起输出">' + (outOpen === j.id ? "⌃" : "⌄") + "</button>";
    let html = '<div class="job-item"><span class="s ' + (JOB_DOT[j.status] || "idle") + '"></span>' +
      '<span class="j-kind">' + esc(j.kind || "exec") + "</span>" +
      '<span class="j-label" title="' + esc(j.label || "") + '">' + esc(j.label || j.id) + "</span>" +
      '<span class="j-st">' + (j.status === "running" ? st : esc(j.detail || st)) + "</span>" +
      '<span class="j-el" title="' + esc(j.id) + '">' + jobElapsed(j) + "</span>" +
      '<span class="j-act">' + act + "</span></div>";
    if (outOpen === j.id) {
      html += '<div class="j-out">' + esc(outCache[j.id] != null ? outCache[j.id] : "加载中…") + "</div>";
    }
    return html;
  };
  let html = "";
  if (live.length) html += secHead("live", "运行中 " + live.length, "");
  if (settled.length) html += secHead("settled", "已结束 " + settled.length, '<span class="j-clear">清空</span>');
  if (!live.length && !settled.length) html = "<div class='empty-hint'>暂无后台命令</div>";
  if (live.length && !secClosed.live) html += live.map(row).join("");
  if (settled.length && !secClosed.settled) html += settled.map(row).join("");
  panel.innerHTML = html;
  panel.querySelectorAll(".job-stop").forEach(b => b.addEventListener("click", (e) => {
    e.stopPropagation();
    pressKill(b.dataset.id, b);
  }));
  panel.querySelectorAll(".job-out").forEach(b => b.addEventListener("click", (e) => {
    e.stopPropagation();
    toggleOut(b.dataset.id);
  }));
  panel.querySelectorAll(".j-sec").forEach(h => h.addEventListener("click", () => {
    secClosed[h.dataset.sec] = !secClosed[h.dataset.sec];
    renderJobsPanel();
  }));
  const clr = panel.querySelector(".j-clear");
  if (clr) clr.addEventListener("click", (e) => { e.stopPropagation(); clearSettledJobs(); });
}
function secHead(key, label, right) {
  return '<div class="j-sec' + (secClosed[key] ? " closed" : "") + '" data-sec="' + key +
    '"><span class="chev">⌄</span>' + label + right + "</div>";
}

// toggleOut / refreshOut: ⌄ 展开走 GET jobs/output(读不消耗,模型侧账不受影响)。
// 同时只展开一个;再点收起。轮询时 refreshOut(id,true) 静默更新已展开的输出。
function toggleOut(id) {
  if (outOpen === id) { outOpen = null; renderJobsPanel(); return; }
  outOpen = id;
  renderJobsPanel();
  refreshOut(id, false);
}
async function refreshOut(id, silent) {
  try {
    const r = await api("/api/sessions/" + state.current + "/jobs/output?job=" + encodeURIComponent(id));
    outCache[id] = r.output || "(无输出)";
  } catch (e) { if (!silent) outCache[id] = "读取失败:" + e.message; }
  if (!silent || outOpen === id) renderJobsPanel();
}

// pressKill 两按确认(dsh 同款):第一下装填(按钮变红写"确认停止",3 秒不动自愈),
// 第二下才真的发 kill——单按杀进程的手滑成本太高。
function pressKill(id, btn) {
  if (killArmed && killArmed.key === id) { doKill(id); return; }
  if (killArmed) { clearTimeout(killArmed.timer); resetKillBtn(); }
  btn.classList.add("armed");
  btn.textContent = "确认停止";
  const timer = setTimeout(() => { killArmed = null; btn.classList.remove("armed"); btn.textContent = "停"; }, 3000);
  killArmed = { key: id, timer: timer, btn: btn };
}
function resetKillBtn() {
  if (killArmed && killArmed.btn) { killArmed.btn.classList.remove("armed"); killArmed.btn.textContent = "停"; }
}
async function doKill(id) {
  if (killArmed) { clearTimeout(killArmed.timer); killArmed = null; }
  try {
    const r = await api("/api/sessions/" + state.current + "/jobs/kill", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ job_id: id, reason: "从界面停止" }),
    });
    toast(r.killed ? "已请求停止,正在杀进程树" : "该命令已经不在运行了", r.killed ? "ok" : "");
  } catch (e) { toast("停止失败:" + e.message, "err"); }
  loadJobs();
}
// Clear 走服务端:把已结束条目从注册表里真删掉(Prune(0),运行中不动),再重取。
// 纯客户端隐藏不行——面板 2s 轮询就是从这里重取数据的,隐藏等于没清。
// 清完顺手刷会话摘要:徽标的 jobs_total 只有摘要里有,不刷会挂着旧计数。
async function clearSettledJobs() {
  if (!state.current) return;
  try {
    await api("/api/sessions/" + state.current + "/jobs/clear", { method: "POST" });
  } catch (e) { toast("清除失败:" + e.message, "err"); return; }
  outOpen = null; outCache = {};
  await loadJobs();
  if (!state.jobs || !state.jobs.length) closeJobsPanel();
  loadSessions();
}

function toggleJobsPanel() {
  const p = $("#jobs-panel");
  if (p.classList.contains("open")) { closeJobsPanel(); return; }
  p.classList.add("open");
  jobsSid = state.current;
  secClosed = {}; outOpen = null; outCache = {};
  loadJobs();
  if (state.jobsTimer) clearInterval(state.jobsTimer);
  state.jobsTimer = setInterval(loadJobs, 2000); // 只在面板开着时轮询
}
function closeJobsPanel() {
  const p = $("#jobs-panel");
  if (p) p.classList.remove("open");
  jobsSid = null;
  if (state.jobsTimer) { clearInterval(state.jobsTimer); state.jobsTimer = null; }
  if (killArmed) { clearTimeout(killArmed.timer); resetKillBtn(); killArmed = null; }
}
