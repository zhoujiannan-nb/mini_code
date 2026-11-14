// 工具步骤的纯逻辑:参数摘要、超时识别、写入量、运行中预览、参数流解析、
// 待办清单与目标状态的读法。
// 这些都是旧 steps.js 里与 DOM 无关的部分,函数体尽量逐字搬过来,
// 组件(Steps.js)只负责把它们算出来的东西画出来。
//
// 工具面已整体切到 deepseek-harness(dsh)的标准模式:唯一事实源是 tools/dsh_tools.json。
// 名字和参数都换了 —— read/write/edit 收 file_path、命令工具叫 pwsh 收
// description/timeoutMs/workdir、搜内容叫 grep 收 pattern、技能叫 skill(name)、
// 子代理叫 subagent(description)。下面的表**两套都认**:dsh 名在前、历史名在后,
// 因为旧会话里躺着的还是 read_file / exec / search_files 那一步(历史要能照原样渲染)。
import { fmtBytes, byteLenOf, fmtRun } from "./format.js";

/* ---- 工具参数:标题行只放"这次调了什么" ----
   真实问题:把整段 JSON 参数塞进标题行,长参数(尤其命令行的那一条)会把工具名
   挤到换行;这里按工具名抽出最关键的一个字段,压成单行,参数全文仍在展开区里。
   表里的值有三种写法:
     "file_path"           单个键名
     ["file_path", "path"] 按优先级排的一组键名 —— dsh 写法在前,历史写法兜底
                           (后端两套拼写都收,模型也可能顺手写旧名)
     ""                    这个工具没有能在标题行说明自己的参数:标题行留空,
                           别把整段 JSON 摊上去
     ["todos", ""]         关键参数就是清单/结构体本身(不是标量):末位那个空串
                           等于"取不到标量就留空",别退回整段参数原文 */
export const TOOL_ARG_KEY = {
  /* ---- dsh 标准工具面 ---- */
  read: ["file_path", "path"],
  write: ["file_path", "path"],
  edit: ["file_path", "path"],
  read_image: ["file_path", "path"],
  glob: "pattern",
  grep: ["pattern", "query"],
  // dsh 要求模型先写一句 5-10 个词的 description,明说"给 UI 显示";命令全文在展开区。
  pwsh: ["description", "command"],
  skill: "name",
  web_fetch: "url",
  todo_write: ["todos", ""],       // 关键参数 todos 是清单本体(画成 .todo-card),不是标量 → 标题行留空
  working_directory: "cd",
  subagent: ["description", "name"],
  subagent_fork: ["description", "name"],
  send_message: ["agent", "message"],
  create_goal: "objective",
  get_goal: "goal_id",
  update_goal: "goal_id",
  workflow: "meta",               // meta 是对象 → 取它的 name / description
  job_output: "job_id",
  job_list: "job_id",
  job_kill: ["job_id", "reason"],
  wait_agent: "",                 // 只有 timeout_ms,不值得占一行
  list_agents: "",

  /* ---- 历史工具名:旧会话的这一步还得照原样画 ---- */
  exec: ["command", "cmd"], bash: "command", shell: "command", run: "command",
  read_file: "path", write_file: "path", edit_file: "path", list_dir: "path",
  search_files: "pattern", find: "pattern",
  web_search: "query", fetch: "url", http: "url", task: "goal", todo: "task",
};

// toolNoArg:这个工具是不是被显式登记成"标题行留空"。工具名整体小写后查表 ——
// 登记值是空串,或者数组里带一个空串(关键参数不是标量,例如 todo_write 的 todos)。
export function toolNoArg(name) {
  const e = TOOL_ARG_KEY[String(name || "").toLowerCase()];
  return e === "" || (Array.isArray(e) && e.indexOf("") >= 0);
}

// toolArgsOf 把 args 解析成对象;解析不了就返回 null(不代表出错)。
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

// toolArgOne 取"最能说明这次调用"的一个值:先看该工具的惯用字段,再退回
// 第一个标量字段。返回 {k, v} 或 null。
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
    // 登记的是对象型参数(workflow 的 meta):取它里面最能说明问题的一个字段。
    if (v && typeof v === "object" && !Array.isArray(v)) {
      for (const sub of ["name", "description", "title", "label"]) {
        const sv = scalar(v[sub]);
        if (sv) return { k: k, v: sv };
      }
      for (const sub of Object.keys(v)) {     // 再退一步:对象里的第一个标量
        const sv = scalar(v[sub]);
        if (sv) return { k: k, v: sv };
      }
    }
    return null;
  };
  const entry = TOOL_ARG_KEY[String(name || "").toLowerCase()];
  if (entry === "" || (Array.isArray(entry) && entry.indexOf("") >= 0)) return null;   // 显式留空
  for (const k of [].concat(entry || [])) {   // dsh 名在前,历史名在后
    if (k === "") continue;
    const got = pick(k);
    if (got) return got;
  }
  // 没登记的工具:取第一个标量字段(值本身通常已能说明问题)
  for (const k of Object.keys(o)) {
    const got = pick(k);
    if (got) return got;
  }
  return null;
}

// isPathKey 判断这个参数是不是"文件路径"。
// 只按**键名**判断,不看值里有没有斜杠 —— 命令工具里的 command 全是斜杠(路径),
// 但它是一条命令,不能被当成路径截成尾部两段。
export function isPathKey(k) {
  const s = String(k || "").toLowerCase();
  return s === "path" || s === "file" || s === "file_path" || s === "dir" ||
    s === "cwd" || s === "workdir" || s === "working_dir" ||
    s === "target" || s === "dest" || s === "source" ||
    /(^|_)(path|file|dir|cwd)$/.test(s);
}

// toolArgDisplay 返回标题行那一小段参数:{value(完整值,做 title), brief(单行省略)}。
//   - 路径:只留最后两段(前面的目录对"这次动了什么"没有信息量);
//   - 其它(命令/查询/目标):压成一行,过长就截断;
//   - 登记成空串的工具(wait_agent / todo_write 这些):返回 null,标题行留空。
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

/* ---- 运行期间唯一已知的"产出量" ----
   命令工具的 stdout 是进程跑完才一次性收回来的,write 的回执也要落盘成功才有,
   所以"正在执行"期间后端确实一个字都还没有。当时就已知的只有"这次要写多少东西"。 */
// toolWorkHint:写类工具在发起调用那一刻就已知的写入量;其它工具返回空串。
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

/* ---- 运行中的"看得见的内容" ----
   只有实现了 StreamTool 的工具(目前是命令工具)运行期间真有字节可推;write 这些
   后端手里确实一个字都还没有。参数是运行期间唯一确定的内容:写入类工具把它要写的
   字符直接摊开,其它工具列出本次参数,命令工具的真输出到了再顶掉预览。 */
export const LIVE_PREV_MAX = 1800;
export function liveClip(s) {
  if (s.length <= LIVE_PREV_MAX) return s;
  return s.slice(0, LIVE_PREV_MAX) + "\n… 其余 " + (s.length - LIVE_PREV_MAX) + " 字（完整内容在参数里）";
}

// toolLivePreview:运行中这一步展开区里该显示的文字。没有可显示的就返回空串。
export function toolLivePreview(name, args) {
  const n = String(name || "").toLowerCase();
  const o = toolArgsOf(args);
  // 待办清单:参数本身就是清单,组件把它画成 .todo-card,这里别再摊一份 JSON。
  if (o && isTodoTool(n) && todoItemsOf(n, o)) return "";
  if (o) {
    const bodyKey = ["content", "new_text", "text", "new_string", "replacement"]
      .find((k) => typeof o[k] === "string" && o[k] !== "");
    if (bodyKey) { // 写/改类:直接给要写进去的字符
      const s = o[bodyKey];
      // dsh 的路径叫 file_path;workdir 是命令工具的工作目录,聊胜于无。
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

/* ---- 模型"正在打字"的参数 ----
   write 的 content 要模型一个字一个字生成,几十 KB 的大纲能跑一两分钟,而
   tool_start 得等整段回复收完才发。后端把参数的当前快照推过来(tool_args,累计值
   不是增量),这里从(可能残缺的)JSON 里抠出正文。 */
// jsonStringField:从(可能残缺的)JSON 里抠出某个字符串字段的值,顺手把常见转义解开。
// 不解析 JSON —— 参数还在长,此刻它根本不是合法 JSON。
export function jsonStringField(raw, key) {
  const m = raw.match(new RegExp('"' + key + '"\\s*:\\s*"'));
  if (!m) return "";
  let i = m.index + m[0].length, out = "";
  while (i < raw.length) {
    const ch = raw[i];
    if (ch === '"') break;                     // 字段写完了(只有完整参数才会走到这)
    if (ch === "\\") {
      const nx = raw[i + 1];
      if (nx === "n") { out += "\n"; i += 2; continue; }
      if (nx === "t") { out += "\t"; i += 2; continue; }
      if (nx === "r") { out += "\r"; i += 2; continue; }
      if (nx === "u" && /^[0-9a-fA-F]{4}$/.test(raw.substr(i + 2, 4))) {
        out += String.fromCharCode(parseInt(raw.substr(i + 2, 4), 16)); i += 6; continue;
      }
      out += nx === undefined ? "" : nx; i += 2; continue;   // \" \\ \/ 等:原样留字符
    }
    out += ch; i++;
  }
  return out;
}

// argsStreamBody:参数里"最长的那个正文字段"就是用户该看见的东西。
export function argsStreamBody(raw) {
  let best = "";
  for (const k of ["content", "new_text", "text", "new_string", "replacement"]) {
    const v = jsonStringField(raw, k);
    if (v.length > best.length) best = v;
  }
  return best;
}

// argsStreamPath:路径通常在 content 之前生成完,能提前知道要写哪个文件。
// dsh 的写类工具管它叫 file_path,历史工具叫 path —— 两种都要能命中整段键名
// (正则里 "file" 后面紧跟引号,是匹配不到 "file_path" 的,所以显式列出)。
export function argsStreamPath(raw) {
  const m = String(raw == null ? "" : raw)
    .match(/"(?:file_path|path|file|target|dest|workdir)"\s*:\s*"([^"]*)/);
  return m ? m[1].replace(/\\\\/g, "\\") : "";
}

/* ---- 运行中计时 ----
   只有一枚"运行中"圆点不够:命令工具跑一条长命令时,读者分不清"在慢慢正常地跑"和
   "已经卡死了"。所以从**发起调用那一刻**起每秒报一次已运行时长,并尽量从参数里
   认出本次的超时上限,写成"执行 12s / 最长 300s"。计时完全在浏览器里做。 */
// dsh 的键名是 timeoutMs(毫秒)与 timeout_ms(毫秒);历史工具用秒。
const TOOL_TIMEOUT_KEYS = ["timeout", "timeout_s", "timeout_sec", "timeout_seconds",
  "timeout_ms", "timeoutms", "deadline", "time_limit", "max_wait", "maxruntime"];

// 命令工具不填超时,执行器也会套上它自己的默认值与上限(pwsh 与 dsh 一致:
// 默认 120000ms、上限 600000ms;超时的命令转后台任务而不是被杀掉)。
// 计时那一栏不该因为参数里没写数字就空着 —— 按默认值报。
export const SHELL_TIMEOUT_DEFAULT_SEC = 120;
export const SHELL_TIMEOUT_MAX_SEC = 600;
const SHELL_TOOLS = ["pwsh", "exec", "bash", "shell", "run"];

// isShellTool:这次调用是不是"跑一条命令"。
export function isShellTool(name) {
  return SHELL_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

// timeoutSecOf 把"最长等待"读成秒。
// 单位优先看**键名**:键名以 ms 结尾(timeoutMs / timeout_ms)就是毫秒,直接除以 1000。
// 键名不带单位(timeout)时只能猜:超过 3600 的按毫秒填的理解(300000 → 300s)。
export function timeoutSecOf(v, key) {
  const n = typeof v === "number" ? v : parseFloat(String(v == null ? "" : v).trim());
  if (!isFinite(n) || n <= 0) return 0;
  if (/(?:^|_)ms$|ms$/.test(String(key == null ? "" : key).toLowerCase())) return Math.round(n / 1000);
  return n > 3600 ? Math.round(n / 1000) : Math.round(n);
}

// toolTimeoutSec 从参数里认出本次调用的超时上限(秒),认不出返回 0。三条来源:
// 参数里的 timeout 类字段、命令文本里自己写的 --timeout 300 / timeout 30 / sleep 45,
// 以及命令工具没写时的执行器默认值。
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
    // curl --timeout=30 / wait 60 / sleep 45 / 命令里单写一个 timeout 30
    let m = cmd.match(/(?:^|[\s;&|])(?:--?(?:t|timeout)|timeout|sleep|wait)[\s=:]+(\d{1,6})\b/);
    // Windows 的 timeout /t 20
    if (!m) m = cmd.match(/(?:\btimeout\b|\/t)[\s=:]+(\d{1,6})\b/);
    if (m) return clampShell(timeoutSecOf(m[1]), name);
  }
  // 命令工具没给数字:执行器用的是它自己的默认值(120s),照它说。
  return isShellTool(name) ? SHELL_TIMEOUT_DEFAULT_SEC : 0;
}

// clampShell:执行器会把命令工具的超时压到 600s,界面报一个跑不到的上限是骗人。
function clampShell(sec, name) {
  if (!sec) return 0;
  return isShellTool(name) ? Math.min(sec, SHELL_TIMEOUT_MAX_SEC) : sec;
}

/* ---- todo_write:把清单参数读成一份清单 ----
   dsh 的 todo_write 收 todos=[{content,status}],status ∈ pending/in_progress/completed。
   这一步的参数本身就是一张清单,摊成 JSON 给人看是最浪费的画法 —— 组件按这里算出来的
   结构画成 .todo-card(三态图标 + 文本 + "3/5" 统计)。 */
export const TODO_TOOLS = ["todo_write", "todo"];
// 旧会话里见过的状态写法 → 三态。认不出的一律当待办(见 todoStatusOf)。
const TODO_LEGACY = { doing: "in_progress", active: "in_progress", working: "in_progress", run: "in_progress", complete: "completed", done: "completed", finished: "completed" };

// isTodoTool:这一步是不是待办清单(工具名整体小写后查表)。
export function isTodoTool(name) {
  return TODO_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

// todoStatusOf:状态归一到 pending / in_progress / completed。认不出的当待办 ——
// 待办是这一条还没做,顶多少一格高亮,不会把没做的事说成做完了。
export function todoStatusOf(v) {
  const s = String(v == null ? "" : v).trim().toLowerCase();
  if (s === "in_progress" || s === "inprogress") return "in_progress";
  if (s === "completed") return "completed";
  if (s === "pending") return "pending";
  return TODO_LEGACY[s] || "pending";
}

// 三态图标与文案(组件画、测试也直接断言这两个表)。
// 待办空心 / 进行中实心 / 完成打勾 —— 顺序就是清单里的视觉顺序。
export const TODO_ICON = { pending: "○", in_progress: "●", completed: "✓" };
export const TODO_STATUS_TEXT = { pending: "待办", in_progress: "进行中", completed: "已完成" };

// todoItemsOf 把 todo_write 的参数读成:
//   { items: [{text,status}], done, doing, total, label }   label 就是 "3/5" 那种统计
// 不是清单工具、参数还不是合法 JSON(模型正在打字)、或者一条都读不出来 → null,
// 组件于是退回普通步骤的画法。
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

/* ---- goal 工具:一行状态文案 ----
   create_goal / update_goal / get_goal 不是"动了哪个文件",读者要看的只有
   "现在这个会话在朝什么目标推进、推到哪一步了",所以只给一行字,别做面板。 */
export const GOAL_TOOLS = ["create_goal", "update_goal", "get_goal"];
const GOAL_ACTION_TEXT = {
  edit: "编辑目标", pause: "暂停目标", resume: "恢复目标",
  complete: "标记完成", blocked: "标记受阻",
};

// isGoalTool:这一步是不是 goal 工具。
export function isGoalTool(name) {
  return GOAL_TOOLS.indexOf(String(name || "").toLowerCase()) >= 0;
}

// goalLineOf 把这个 goal 调用读成一行中文文案;不是 goal 工具返回空串。
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
  // update_goal:action 是唯一有信息量的字段;edit 会带新 objective,blocked 会带原因。
  const action = GOAL_ACTION_TEXT[one(o.action).toLowerCase()] || "更新目标";
  const detail = one(o.objective) || one(o.blocked_reason);
  return detail ? action + "：" + detail : action;
}

// 运行中输出只留尾部 4000 字:一条递归列目录能吐几 MB,完整输出最终在工具结果里。
export const LIVE_OUT_TAIL = 4000;
export function tailOutput(text) {
  const s = String(text == null ? "" : text);
  return s.length > LIVE_OUT_TAIL ? "…" + s.slice(-(LIVE_OUT_TAIL - 1)) : s;
}

// stepTag 是步骤状态徽标的唯一画法(组件用它,测试也直接测它)。
// "还在干活"的三种状态(执行中/待返回/生成参数中)都挂转圈:旋转 = 活着。
// ok/dim 是终态,保持静止 —— 转圈只属于真的在等的时刻。
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
  // done:回执到了,报"总共 xxs · N 字"
  const dur = step.durationSec || 0;
  const len = step.result == null ? 0 : String(step.result).length;
  return {
    kind: "ok",
    text: "✓ " + (dur >= 2 ? "总共 " + fmtRun(dur) + " · " : "") + len + " 字",
    spin: false,
  };
}
