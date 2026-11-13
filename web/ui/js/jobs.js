/* ---------------- 后台命令(job)面板 ---------------- */
// 当前会话派发的后台命令(exec run_in_background / 前台等待超时转后台)。
// 入口按钮只在当前会话有可见 job 时出现;面板分「运行中 / 已结束」两段,
// 运行中行带两按停止。面板开着才轮询(2s),关了零开销——监控不该有常驻成本。
state.jobs = [];
state.jobsTimer = null;
let jobsSid = null;     // 面板当前服务的会话;切会话自动关面板(数据不跨会话)
let killArmed = null;   // {key, timer}:两按确认中的 job

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
    const detail = j.detail ? " · " + esc(j.detail) : "";
    const stop = j.status === "running" ? '<button class="task-stop job-stop" data-id="' + esc(j.id) + '">停止</button>' : "";
    return '<div class="job-item"><span class="s ' + (JOB_DOT[j.status] || "idle") + '"></span>' +
      '<span class="j-kind">' + esc(j.kind || "exec") + '</span>' +
      '<span class="j-label" title="' + esc(j.label || "") + '">' + esc(j.label || j.id) + '</span>' +
      '<span class="j-st">' + st + detail + '</span>' +
      '<span class="j-el" title="' + esc(j.id) + '">' + jobElapsed(j) + '</span>' +
      '<span class="j-act">' + stop + '</span></div>';
  };
  let html = "<h4>后台命令</h4>";
  if (live.length) html += '<div class="j-sec">运行中</div>' + live.map(row).join("");
  if (settled.length) {
    html += '<div class="j-sec j-sec-settled">已结束(' + settled.length + ')<button class="j-clear">清除</button></div>' +
      settled.map(row).join("");
  }
  if (!live.length && !settled.length) html += "<div class='empty-hint'>暂无后台命令</div>";
  panel.innerHTML = html;
  panel.querySelectorAll(".job-stop").forEach(b => b.addEventListener("click", (e) => {
    e.stopPropagation();
    pressKill(b.dataset.id, b);
  }));
  const clr = panel.querySelector(".j-clear");
  if (clr) clr.addEventListener("click", (e) => { e.stopPropagation(); clearSettledJobs(); });
}

// pressKill 两按确认(dsh 同款):第一下装填(按钮变红写"确认停止",3 秒不动自愈),
// 第二下才真的发 kill——单按杀进程的手滑成本太高。
function pressKill(id, btn) {
  if (killArmed && killArmed.key === id) { doKill(id); return; }
  if (killArmed) { clearTimeout(killArmed.timer); resetKillBtn(); }
  btn.classList.add("armed");
  btn.textContent = "确认停止";
  const timer = setTimeout(() => { killArmed = null; btn.classList.remove("armed"); btn.textContent = "停止"; }, 3000);
  killArmed = { key: id, timer: timer, btn: btn };
}
function resetKillBtn() {
  if (killArmed && killArmed.btn) { killArmed.btn.classList.remove("armed"); killArmed.btn.textContent = "停止"; }
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
  await loadJobs();
  if (!state.jobs || !state.jobs.length) closeJobsPanel();
  loadSessions();
}

function toggleJobsPanel() {
  const p = $("#jobs-panel");
  if (p.classList.contains("open")) { closeJobsPanel(); return; }
  p.classList.add("open");
  jobsSid = state.current;
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
