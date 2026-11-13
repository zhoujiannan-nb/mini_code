// 所有 HTTP 路径只在这里出现一次:组件和 actions 一律走这几个函数。
// 路径与 web/web.go 的路由表逐条对齐(见各函数注释),改后端时只有这一个文件要跟着改。
//
// api():成功返回 JSON;失败抛 Error(优先用后端 {"error": ...} 的文案)。
// 注意用 globalThis.fetch 而不是模块顶层的 fetch 引用:render 测试会替换掉它。
export async function api(path, opts) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.status + " " + r.statusText;
    try { const j = await r.json(); if (j.error) msg = j.error; } catch (e) {}
    throw new Error(msg);
  }
  return r.json();
}

function jsonBody(body) {
  return {
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

export function getJSON(path) {
  return api(path);
}

export function postJSON(path, body) {
  return api(path, { method: "POST", ...jsonBody(body) });
}

export function putJSON(path, body) {
  return api(path, { method: "PUT", ...jsonBody(body) });
}

export function del(path) {
  return api(path, { method: "DELETE" });
}

// ---- 路由清单(只读文档,供人核对) ----
// GET    /api/status
// GET    /api/sessions?worker=<id>          GET /api/sessions/{id}
// DELETE /api/sessions/{id}                 GET /api/sessions/{id}/events?tail_run=1
// POST   /api/sessions/{id}/compact|mode|context-reminders|model|reasoning|rename|cancel
// GET    /api/sessions/{id}/children        GET /api/sessions/{id}/jobs
// GET    /api/sessions/{id}/jobs/output?job=<id>
// POST   /api/sessions/{id}/jobs/kill|jobs/clear
// GET    /api/workers                       POST /api/workers
// POST   /api/workers/{id}/summary|rename   DELETE /api/workers/{id}
// GET    /api/workers/{id}/note             POST /api/workers/{id}/note
// POST   /api/messages                      GET  /api/events (SSE)
// GET/PUT /api/config                       GET  /api/avatar|agent-avatar
// POST   /api/avatar|agent-avatar
// POST   /api/llm/models|probe
// GET    /api/fs/search?q=&limit=           POST /api/fs/open
// GET    /api/approvals                     POST /api/approvals/{id}/decision
// GET    /api/plugins                       POST /api/plugins/{id}/toggle
// DELETE /api/plugins/{id}                  POST /api/plugins/{id}/process
// GET    /api/plugins/{id}/logs?n=          GET  /api/plugins/catalog
// POST   /api/plugins/install               GET  /api/plugins/prompt?kind=
// GET    /api/bootanim                      PUT  /api/bootanim
export const ROUTES = Object.freeze({
  status: "/api/status",
  sessions: (worker) => "/api/sessions?worker=" + encodeURIComponent(worker || ""),
  session: (id) => "/api/sessions/" + encodeURIComponent(id),
  sessionEvents: (id, tailRun) => "/api/sessions/" + encodeURIComponent(id) + "/events?tail_run=" + (tailRun ? 1 : 0),
  messages: "/api/messages",
  events: "/api/events",
  config: "/api/config",
  approvals: "/api/approvals",
  approval: (id) => "/api/approvals/" + encodeURIComponent(id) + "/decision",
  workers: "/api/workers",
  worker: (id) => "/api/workers/" + encodeURIComponent(id),
  workerNote: (id) => "/api/workers/" + encodeURIComponent(id) + "/note",
  jobs: (id) => "/api/sessions/" + encodeURIComponent(id) + "/jobs",
  jobOutput: (id, job) => "/api/sessions/" + encodeURIComponent(id) + "/jobs/output?job=" + encodeURIComponent(job),
  jobKill: (id) => "/api/sessions/" + encodeURIComponent(id) + "/jobs/kill",
  jobClear: (id) => "/api/sessions/" + encodeURIComponent(id) + "/jobs/clear",
  children: (id) => "/api/sessions/" + encodeURIComponent(id) + "/children",
  cancel: (id) => "/api/sessions/" + encodeURIComponent(id) + "/cancel",
  rename: (id) => "/api/sessions/" + encodeURIComponent(id) + "/rename",
  mode: (id) => "/api/sessions/" + encodeURIComponent(id) + "/mode",
  model: (id) => "/api/sessions/" + encodeURIComponent(id) + "/model",
  reasoning: (id) => "/api/sessions/" + encodeURIComponent(id) + "/reasoning",
  reminders: (id) => "/api/sessions/" + encodeURIComponent(id) + "/context-reminders",
  compact: (id) => "/api/sessions/" + encodeURIComponent(id) + "/compact",
  fsSearch: (q, limit) => "/api/fs/search?q=" + encodeURIComponent(q) + "&limit=" + (limit || 20),
  fsOpen: "/api/fs/open",
  avatar: "/api/avatar",
  agentAvatar: "/api/agent-avatar",
  llmModels: "/api/llm/models",
  llmProbe: "/api/llm/probe",
  plugins: "/api/plugins",
  plugin: (id) => "/api/plugins/" + encodeURIComponent(id),
  pluginToggle: (id) => "/api/plugins/" + encodeURIComponent(id) + "/toggle",
  pluginProcess: (id) => "/api/plugins/" + encodeURIComponent(id) + "/process",
  pluginLogs: (id, n) => "/api/plugins/" + encodeURIComponent(id) + "/logs?n=" + (n || 80),
  pluginCatalog: "/api/plugins/catalog",
  pluginInstall: "/api/plugins/install",
  pluginPrompt: (kind) => "/api/plugins/prompt?kind=" + encodeURIComponent(kind),
  bootanim: "/api/bootanim",
});
