// 工具步骤的纯逻辑:参数摘要、超时识别、写入量、运行中预览、参数流解析。
// 这些都是旧 steps.js 里与 DOM 无关的部分,函数体尽量逐字搬过来,
// 组件(Steps.js)只负责把它们算出来的东西画出来。
import { fmtBytes, byteLenOf, fmtRun } from "./format.js";

/* ---- 工具参数:标题行只放"这次调了什么" ----
   真实问题:把整段 JSON 参数塞进标题行,长参数(尤其 exec 的命令行)会把工具名
   挤到换行;这里按工具名抽出最关键的一个字段,压成单行,参数全文仍在展开区里。 */
export const TOOL_ARG_KEY = {
  exec: "command", bash: "command", shell: "command", run: "command",
  read_file: "path", write_file: "path", edit_file: "path", list_dir: "path",
  search_files: "pattern", find: "pattern", grep: "pattern",
  web_search: "query", fetch: "url", http: "url", task: "goal", todo: "task",
};

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
  const pick = (k) => {
    const v = o[k];
    if (v == null || typeof v === "object") return null;
    const s = String(v);
    return s ? { k: k, v: s } : null;
  };
  const key = TOOL_ARG_KEY[String(name || "").toLowerCase()];
  if (key && pick(key)) return pick(key);
  // 没登记的工具:取第一个标量字段(值本身通常已能说明问题)
  for (const k of Object.keys(o)) {
    const got = pick(k);
    if (got) return got;
  }
  return null;
}

// isPathKey 判断这个参数是不是"文件路径"。
// 只按**键名**判断,不看值里有没有斜杠 —— exec 的 command 里全是斜杠(路径),
// 但它是一条命令,不能被当成路径截成尾部两段。
export function isPathKey(k) {
  const s = String(k || "").toLowerCase();
  return s === "path" || s === "file" || s === "dir" || s === "cwd" ||
    s === "target" || s === "dest" || s === "source" ||
    /(^|_)(path|file|dir|cwd)$/.test(s);
}

// toolArgDisplay 返回标题行那一小段参数:{value(完整值,做 title), brief(单行省略)}。
//   - 路径:只留最后两段(前面的目录对"这次动了什么"没有信息量);
//   - 其它(命令/查询/目标):压成一行,过长就截断。
export function toolArgDisplay(name, args) {
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
   exec 的 stdout 是进程跑完才一次性收回来的,write_file 的回执也要落盘成功才有,
   所以"正在执行"期间后端确实一个字都还没有。当时就已知的只有"这次要写多少东西"。 */
// toolWorkHint:写类工具在发起调用那一刻就已知的写入量;其它工具返回空串。
export function toolWorkHint(name, args) {
  const n = String(name || "").toLowerCase();
  if (!/write|append|edit|create|save|patch/.test(n)) return "";
  const o = toolArgsOf(args);
  if (!o) return "";
  const key = ["content", "new_text", "text"].find((k) => typeof o[k] === "string" && o[k] !== "");
  if (!key) return "";
  const s = o[key];
  const verb = /append/.test(n) ? "追加 " : "写入 ";
  return verb + fmtBytes(byteLenOf(s)) + " · " + s.split("\n").length + " 行";
}

/* ---- 运行中的"看得见的内容" ----
   只有实现了 StreamTool 的工具(目前是 exec)运行期间真有字节可推;write_file 这些
   后端手里确实一个字都还没有。参数是运行期间唯一确定的内容:写入类工具把它要写的
   字符直接摊开,其它工具列出本次参数,exec 的真输出到了再顶掉预览。 */
export const LIVE_PREV_MAX = 1800;
export function liveClip(s) {
  if (s.length <= LIVE_PREV_MAX) return s;
  return s.slice(0, LIVE_PREV_MAX) + "\n… 其余 " + (s.length - LIVE_PREV_MAX) + " 字（完整内容在参数里）";
}

// toolLivePreview:运行中这一步展开区里该显示的文字。没有可显示的就返回空串。
export function toolLivePreview(name, args) {
  const n = String(name || "").toLowerCase();
  const o = toolArgsOf(args);
  if (o) {
    const bodyKey = ["content", "new_text", "text", "new_string", "replacement"]
      .find((k) => typeof o[k] === "string" && o[k] !== "");
    if (bodyKey) { // 写/改类:直接给要写进去的字符
      const s = o[bodyKey];
      const path = o.path || o.file || o.target || o.dest || o.source || "?";
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
   write_file 的 content 要模型一个字一个字生成,几十 KB 的大纲能跑一两分钟,而
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
export function argsStreamPath(raw) {
  const m = raw.match(/"(?:path|file|target|dest)"\s*:\s*"([^"]*)/);
  return m ? m[1].replace(/\\\\/g, "\\") : "";
}

/* ---- 运行中计时 ----
   只有一枚"运行中"圆点不够:exec 跑一条长命令时,读者分不清"在慢慢正常地跑"和
   "已经卡死了"。所以从**发起调用那一刻**起每秒报一次已运行时长,并尽量从参数里
   认出本次的超时上限,写成"执行 12s / 最长 300s"。计时完全在浏览器里做。 */
const TOOL_TIMEOUT_KEYS = ["timeout", "timeout_s", "timeout_sec", "timeout_seconds",
  "deadline", "time_limit", "max_wait", "maxruntime"];

// timeoutSecOf 把"最长等待"读成秒。超过 3600 的按毫秒填的理解(300000 → 300s)。
export function timeoutSecOf(v) {
  const n = typeof v === "number" ? v : parseFloat(String(v == null ? "" : v).trim());
  if (!isFinite(n) || n <= 0) return 0;
  return n > 3600 ? Math.round(n / 1000) : Math.round(n);
}

// toolTimeoutSec 从参数里认出本次调用的超时上限(秒),认不出返回 0。两条来源:
// 参数里的 timeout 类字段,或命令文本里自己写的 --timeout 300 / timeout 30 / sleep 45。
export function toolTimeoutSec(args) {
  const o = toolArgsOf(args);
  if (o) {
    for (const k of Object.keys(o)) {
      if (TOOL_TIMEOUT_KEYS.indexOf(String(k).toLowerCase()) >= 0) {
        const n = timeoutSecOf(o[k]);
        if (n) return n;
      }
    }
  }
  const cmd = String((o && (o.command || o.cmd)) || "").toLowerCase();
  if (!cmd) return 0;
  // curl --timeout=30 / wait 60 / sleep 45 / 命令里单写一个 timeout 30
  let m = cmd.match(/(?:^|[\s;&|])(?:--?(?:t|timeout)|timeout|sleep|wait)[\s=:]+(\d{1,6})\b/);
  // Windows 的 timeout /t 20
  if (!m) m = cmd.match(/(?:\btimeout\b|\/t)[\s=:]+(\d{1,6})\b/);
  return m ? timeoutSecOf(m[1]) : 0;
}

// 运行中输出只留尾部 4000 字:一条 `dir /s` 能吐几 MB,完整输出最终在工具结果里。
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
