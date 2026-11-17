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
  context: (id) => "/api/sessions/" + encodeURIComponent(id) + "/context",
  trajectory: (id, opts) => {
    let q = "/api/sessions/" + encodeURIComponent(id) + "/trajectory";
    const o = opts || {};
    if (o.before != null) q += "?before=" + encodeURIComponent(o.before);
    if (o.limit) q += (q.includes("?") ? "&" : "?") + "limit=" + encodeURIComponent(o.limit);
    return q;
  },
  contextDetail: (id, part, opts) => {
    let q = "/api/sessions/" + encodeURIComponent(id) + "/context/detail?part=" + encodeURIComponent(part);
    const o = opts || {};
    if (o.i != null) q += "&i=" + encodeURIComponent(o.i);
    if (o.role) q += "&role=" + encodeURIComponent(o.role);
    if (o.before != null) q += "&before=" + encodeURIComponent(o.before);
    if (o.limit) q += "&limit=" + encodeURIComponent(o.limit);
    return q;
  },
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
  superEnsure: "/api/super/ensure",
  superOverview: "/api/super/overview",
  superSchedCreate: "/api/super/schedules/create",
  superSchedDelete: "/api/super/schedules/delete",
  superSchedToggle: "/api/super/schedules/toggle",
  superSchedRun: "/api/super/schedules/run",
});
