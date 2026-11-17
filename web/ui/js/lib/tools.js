import { fmtBytes, byteLenOf, fmtRun } from "./format.js";

export const TOOL_ARG_KEY = {
  read: ["file_path", "path"],
  write: ["file_path", "path"],
  edit: ["file_path", "path"],
  read_image: ["file_path", "path"],
  glob: "pattern",
  grep: ["pattern", "query"],
  pwsh: ["description", "command"],
  skill: "name",
  web_fetch: "url",
  todo_write: ["todos", ""],       
  working_directory: "cd",
  subagent: ["description", "name"],
  subagent_fork: ["description", "name"],
  send_message: ["agent", "message"],
  create_goal: "objective",
  get_goal: "goal_id",
  update_goal: "goal_id",
  workflow: "meta",               
  job_output: "job_id",
  job_list: "job_id",
  job_kill: ["job_id", "reason"],
  wait_agent: "",                 
  list_agents: "",

  exec: ["command", "cmd"], bash: "command", shell: "command", run: "command",
  read_file: "path", write_file: "path", edit_file: "path", list_dir: "path",
  search_files: "pattern", find: "pattern",
  web_search: "query", fetch: "url", http: "url", task: "goal", todo: "task",
};

export function toolNoArg(name) {
  const e = TOOL_ARG_KEY[String(name || "").toLowerCase()];
  return e === "" || (Array.isArray(e) && e.indexOf("") >= 0);
}

export function toolArgsOf(args) {
  if (args == null) return null;
  if (typeof args === "object") return args;
  const s = String(args).trim();
  if (!s) return null;
  try {
    const o = JSON.parse(s);
    return o && typeof o === "object" ? o : null;
  } catch (e) {
    return null;
  }
}

export function toolArgOne(name, args) {
  const o = toolArgsOf(args);
  if (!o) return null;
  const scalar = (v) => {
    if (v == null || typeof v === "object") return null;
    const s = String(v);
    return s ? s : null;
  };
  const pick = (k) => {
    const v = o[k];
    const s = scalar(v);
    if (s) return { k: k, v: s };
    if (v && typeof v === "object" && !Array.isArray(v)) {
      for (const sub of ["name", "description", "title", "label"]) {
        const sv = scalar(v[sub]);
        if (sv) return { k: k, v: sv };
      }
      for (const sub of Object.keys(v)) {     
        const sv = scalar(v[sub]);
        if (sv) return { k: k, v: sv };
      }
    }
    return null;
  };
  const entry = TOOL_ARG_KEY[String(name || "").toLowerCase()];
  if (entry === "" || (Array.isArray(entry) && entry.indexOf("") >= 0)) return null;   
  for (const k of [].concat(entry || [])) {   
    if (k === "") continue;
    const got = pick(k);
    if (got) return got;
  }
  for (const k of Object.keys(o)) {
    const got = pick(k);
    if (got) return got;
  }
  return null;
}

export function isPathKey(k) {
  const s = String(k || "").toLowerCase();
  return s === "path" || s === "file" || s === "file_path" || s === "dir" ||
    s === "cwd" || s === "workdir" || s === "working_dir" ||
    s === "target" || s === "dest" || s === "source" ||
    /(^|_)(path|file|dir|cwd)$/.test(s);
}

export function toolArgDisplay(name, args) {
  if (toolNoArg(name)) return null;
  const got = toolArgOne(name, args);
  if (!got) {
    const raw = args == null ? "" : String(args).replace(/\s+/g, " ").trim();
    if (!raw) return null;
    return { value: raw, brief: raw };
  }
  let shown = got.v.replace(/\s+/g, " ").trim();
  if (isPathKey(got.k)) {
    const parts = shown.split(/[\\/]+/).filter(Boolean);
    if (parts.length > 3) shown = "…/" + parts.slice(-2).join("/");
  }
  const brief = shown.length > 58 ? shown.slice(0, 58) + "…" : shown;
  return { value: got.v, brief: brief };
}

export function toolWorkHint(name, args) {
  const n = String(name || "").toLowerCase();
  if (!/write|append|edit|create|save|patch/.test(n)) return "";
  const o = toolArgsOf(args);
  if (!o) return "";
  const key = ["content", "new_text", "text", "new_string", "replacement"]
    .find((k) => typeof o[k] === "string" && o[k] !== "");
  if (!key) return "";
  const s = o[key];
  const verb = /append/.test(n) ? "追加 " : "写入 ";
  return verb + fmtBytes(byteLenOf(s)) + " · " + s.split("\n").length + " 行";
}

export const LIVE_PREV_MAX = 1800;
export function liveClip(s) {
  if (s.length <= LIVE_PREV_MAX) return s;
  return s.slice(0, LIVE_PREV_MAX) + "\n… 其余 " + (s.length - LIVE_PREV_MAX) + " 字（完整内容在参数里）";
}

export function toolLivePreview(name, args) {
  const n = String(name || "").toLowerCase();
  const o = toolArgsOf(args);
  if (o && isTodoTool(n) && todoItemsOf(n, o)) return "";
  if (o) {
    const bodyKey = ["content", "new_text", "text", "new_string", "replacement"]
      .find((k) => typeof o[k] === "string" && o[k] !== "");
    if (bodyKey) { 
      const s = o[bodyKey];
      const path = o.file_path || o.path || o.file || o.target || o.dest || o.source || o.workdir || "?";
      const verb = /append/.test(n) ? "追加" : (/edit|patch/.test(n) ? "改动" : "写入");
      return verb + " " + path + " · " + fmtBytes(byteLenOf(s)) + " · " + s.split("\n").length + " 行 (" + bodyKey + ")\n" + liveClip(s);
    }
    const lines = Object.keys(o).map((k) => {
      const v = o[k];
      return k + ": " + (typeof v === "string" ? v : JSON.stringify(v));
    });
    if (lines.length) return liveClip(lines.join("\n"));
  }
  const raw = args == null ? "" : String(args).trim();
  return raw ? liveClip(raw) : "";
}

export function jsonStringField(raw, key) {
  const m = raw.match(new RegExp('"' + key + '"\\s*:\\s*"'));
  if (!m) return "";
  let i = m.index + m[0].length, out = "";
  while (i < raw.length) {
    const ch = raw[i];
    if (ch === '"') break;                     
    if (ch === "\\") {
      const nx = raw[i + 1];
      if (nx === "n") { out += "\n"; i += 2; continue; }
      if (nx === "t") { out += "\t"; i += 2; continue; }
      if (nx === "r") { out += "\r"; i += 2; continue; }
      if (nx === "u" && /^[0-9a-fA-F]{4}$/.test(raw.substr(i + 2, 4))) {
        out += String.fromCharCode(parseInt(raw.substr(i + 2, 4), 16)); i += 6; continue;
      }
      out += nx === undefined ? "" : nx; i += 2; continue;   
    }
    out += ch; i++;
  }
  return out;
}

export function argsStreamBody(raw) {
  let best = "";
  for (const k of ["content", "new_text", "text", "new_string", "replacement"]) {
    const v = jsonStringField(raw, k);
    if (v.length > best.length) best = v;
  }
  return best;
}

export function argsStreamPath(raw) {
  const m = String(raw == null ? "" : raw)
    .match(/"(?:file_path|path|file|target|dest|workdir)"\s*:\s*"([^"]*)/);
  return m ? m[1].replace(/\\\\/g, "\\") : "";
}

const TOOL_TIMEOUT_KEYS = ["timeout", "timeout_s", "timeout_sec", "timeout_seconds",
  "timeout_ms", "timeoutms", "deadline", "time_limit", "max_wait", "maxruntime"];

export const SHELL_TIMEOUT_DEFAULT_SEC = 120;
export const SHELL_TIMEOUT_MAX_SEC = 600;
const SHELL_TOOLS = ["pwsh", "exec", "bash", "shell", "run"];

export function isShellTool(name) {
  return SHELL_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

export function timeoutSecOf(v, key) {
  const n = typeof v === "number" ? v : parseFloat(String(v == null ? "" : v).trim());
  if (!isFinite(n) || n <= 0) return 0;
  if (/(?:^|_)ms$|ms$/.test(String(key == null ? "" : key).toLowerCase())) return Math.round(n / 1000);
  return n > 3600 ? Math.round(n / 1000) : Math.round(n);
}

export function toolTimeoutSec(args, name) {
  const o = toolArgsOf(args);
  if (o) {
    for (const k of Object.keys(o)) {
      if (TOOL_TIMEOUT_KEYS.indexOf(String(k).toLowerCase()) >= 0) {
        const n = timeoutSecOf(o[k], k);
        if (n) return clampShell(n, name);
      }
    }
  }
  const cmd = String((o && (o.command || o.cmd)) || "").toLowerCase();
  if (cmd) {
    let m = cmd.match(/(?:^|[\s;&|])(?:--?(?:t|timeout)|timeout|sleep|wait)[\s=:]+(\d{1,6})\b/);
    if (!m) m = cmd.match(/(?:\btimeout\b|\/t)[\s=:]+(\d{1,6})\b/);
    if (m) return clampShell(timeoutSecOf(m[1]), name);
  }
  return isShellTool(name) ? SHELL_TIMEOUT_DEFAULT_SEC : 0;
}

function clampShell(sec, name) {
  if (!sec) return 0;
  return isShellTool(name) ? Math.min(sec, SHELL_TIMEOUT_MAX_SEC) : sec;
}

export const TODO_TOOLS = ["todo_write", "todo"];
const TODO_LEGACY = { doing: "in_progress", active: "in_progress", working: "in_progress", run: "in_progress", complete: "completed", done: "completed", finished: "completed" };

export function isTodoTool(name) {
  return TODO_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

export function todoStatusOf(v) {
  const s = String(v == null ? "" : v).trim().toLowerCase();
  if (s === "in_progress" || s === "inprogress") return "in_progress";
  if (s === "completed") return "completed";
  if (s === "pending") return "pending";
  return TODO_LEGACY[s] || "pending";
}

export const TODO_ICON = { pending: "○", in_progress: "●", completed: "✓" };
export const TODO_STATUS_TEXT = { pending: "待办", in_progress: "进行中", completed: "已完成" };

export function todoItemsOf(name, args) {
  if (!isTodoTool(name)) return null;
  const o = toolArgsOf(args);
  if (!o) return null;
  const raw = Array.isArray(o.todos) ? o.todos : Array.isArray(o.items) ? o.items : null;
  if (!raw || !raw.length) return null;
  const items = [];
  for (const t of raw) {
    if (t == null) continue;
    if (typeof t !== "object") {
      const only = String(t).trim();
      if (only) items.push({ text: only, status: "pending" });
      continue;
    }
    const text = String(t.content || t.text || t.title || "").replace(/\s+/g, " ").trim();
    if (!text) continue;
    items.push({ text: text, status: todoStatusOf(t.status) });
  }
  if (!items.length) return null;
  const done = items.filter((i) => i.status === "completed").length;
  const doing = items.filter((i) => i.status === "in_progress").length;
  return { items: items, done: done, doing: doing, total: items.length, label: done + "/" + items.length };
}

export const GOAL_TOOLS = ["create_goal", "update_goal", "get_goal"];
const GOAL_ACTION_TEXT = {
  edit: "编辑目标", pause: "暂停目标", resume: "恢复目标",
  complete: "标记完成", blocked: "标记受阻",
};

export function isGoalTool(name) {
  return GOAL_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

export function goalLineOf(name, args) {
  const n = String(name || "").toLowerCase();
  if (!isGoalTool(n)) return "";
  const o = toolArgsOf(args) || {};
  const one = (v) => String(v == null ? "" : v).replace(/\s+/g, " ").trim();
  if (n === "get_goal") return "查看当前目标";
  if (n === "create_goal") {
    const ob = one(o.objective);
    return ob ? "新建目标：" + ob : "新建目标";
  }
  const action = GOAL_ACTION_TEXT[one(o.action).toLowerCase()] || "更新目标";
  const detail = one(o.objective) || one(o.blocked_reason);
  return detail ? action + "：" + detail : action;
}

export const LIVE_OUT_TAIL = 4000;
export function tailOutput(text) {
  const s = String(text == null ? "" : text);
  return s.length > LIVE_OUT_TAIL ? "…" + s.slice(-(LIVE_OUT_TAIL - 1)) : s;
}

export function stepTag(step, sessionRunning) {
  const phase = step.phase;
  if (phase === "run") return { kind: "run", text: "执行中", spin: true };
  if (phase === "prep") return { kind: "prep", text: "生成参数中", spin: true };
  if (phase === "soft") {
    return sessionRunning
      ? { kind: "soft", text: "待返回", spin: true }
      : { kind: "dim", text: "待返回", spin: false };
  }
  if (phase === "notrun") return { kind: "dim", text: "未执行", spin: false };
  if (phase === "lost") return { kind: "dim", text: "已执行·未收到回执", spin: false };
  const dur = step.durationSec || 0;
  const len = step.result == null ? 0 : String(step.result).length;
  return {
    kind: "ok",
    text: "✓ " + (dur >= 2 ? "总共 " + fmtRun(dur) + " · " : "") + len + " 字",
    spin: false,
  };
}
