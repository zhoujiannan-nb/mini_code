/* ---------------- 轻量 Markdown 渲染(无外部依赖,纯前端) ----------------
   模型消息正文/思考过程统一走 mdToHtml(函数体沿用旧 js/markdown.js,逐字保留):
   - 支持 标题/加粗/斜体/删除线/链接/列表(可嵌套)/引用/表格/分隔线/行内代码/围栏代码块
   - 代码块:深色底 + 语言标签 + 复制按钮;json 自动美化 + 高亮
   - 整条消息就是 JSON(无围栏)时,也直接按 json 代码块渲染
   - 安全:所有文本先转义再插 HTML;未闭合的代码围栏(流式输出中)也能正确兜底 */
import { api, ROUTES } from "./api.js";
import { state, toast } from "./store.js";
import { nav } from "./nav.js";

// esc 是"文本 + 属性"双上下文转义。旧 dom.js 里的那条注释仍然成立:
// 只处理 & < > 的写法放进属性里可以被引号逃逸,这里连引号一起转。
export function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function escFast(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
function mdEscRe(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }
// mdUnescape 把 escFast 的三个实体还原:检测路径用原文更准,
// 写进 data-* 时再由 esc() 统一转义。
function mdUnescape(s) {
  return String(s).replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

/* ---------------- 聊天里的文件路径:点了就打开 ---------------- */
// 模型经常把产物路径直接写进回复("改好了:D:\proj\index.html"),
// 以前只能自己复制到资源管理器。这里把这种路径变成可点的链接,
// 点击交给 /api/fs/open,由服务端用系统默认程序打开。
const MD_FILE_EXT = /\.(html?|xhtml|css|scss|sass|less|js|mjs|cjs|jsx|ts|tsx|json|jsonc|ya?ml|toml|ini|cfg|conf|xml|svg|md|markdown|txt|log|csv|tsv|pdf|docx?|xlsx?|pptx?|png|jpe?g|gif|webp|bmp|ico|avif|mp4|webm|mov|mp3|wav|ogg|flac|zip|rar|7z|gz|tar|exe|msi|dll|go|py|rb|rs|java|kt|c|h|cpp|hpp|cs|php|lua|sh|bat|cmd|ps1|sql|db|sqlite3?|vue|svelte|astro)$/i;

// mdSplitLine 拆掉"文件:行号[:列号]"里的行号尾巴。`a.go:12` 在聊天里很常见,
// 它该是"点开 a.go"的链接,而不是一段不是路径的文本;行号只保留在显示文字里,
// 传给 /api/fs/open 的 data-file 是不带行号的真实路径。
function mdSplitLine(raw) {
  const m = /^(.*?)(:\d+(?::\d+)?)$/.exec(String(raw == null ? "" : raw));
  return m ? { path: m[1], line: m[2] } : { path: String(raw == null ? "" : raw), line: "" };
}

// mdIsFilePath 判断一段文本是不是"用户可以点开"的路径。
// 判据刻意保守 —— 宁可漏一个,也别把 `and/or`、`1/2`、`2026/10/27`
// 变成点一下就报错的红字:
//   绝对(盘符/UNC//x/./x/../x/~/x)一律算;
//   带分隔符的相对路径必须落在已知扩展名上(web/ui/index.html);
//   单个文件名(README.md)也必须带已知扩展名。
export function mdIsFilePath(raw) {
  const p = mdSplitLine(mdUnescape(String(raw == null ? "" : raw)).trim()).path;
  if (!p || p.length > 260) return false;
  if (/^[a-z][a-z0-9+.\-]*:\/\//i.test(p)) return false; // http/https/... 交给 URL 逻辑
  if (/[\s<>"\u0000|]/.test(p)) return false;            // 有空格/标签残留:不是单个路径
  if (/[?*]/.test(p)) return false;                      // 通配符/疑问,不是具体文件
  if (/^[a-z]:[\\/]/i.test(p)) return true;              // D:\... / D:/...
  if (/^(\\\\|\/|\.{1,2}[\\/]|~[\\/])/.test(p)) return true; // UNC、/abs、./、../、~/
  // 其余按相对路径:有分隔符的靠扩展名区分,单文件名必须带扩展名
  return MD_FILE_EXT.test(p);
}

// fileOpenLinkHTML 生成一个可点路径的链接。asCode=true 时在链接里套 <code>,
// 让 `反引号里的路径` 保持原来的等宽观感。
export function fileOpenLinkHTML(raw, wrapOpen, labelText) {
  const rawPath = mdUnescape(String(raw == null ? "" : raw)).trim();
  const path = mdSplitLine(rawPath).path;
  const shown = labelText != null ? mdUnescape(labelText) : rawPath;
  const inner = wrapOpen ? wrapOpen + escFast(shown) + "</code>" : escFast(shown);
  return '<a class="md-file" role="button" tabindex="0" title="点击打开 ' + esc(path) +
    '" data-file="' + esc(path) + '">' + inner + "</a>";
}

// sessionLinkHTML 生成一个"会话跳转"芯片。模型输出
//   [八卡4090调优](mc-session://6865c8f734c48ad955555c7c)
// 时渲染成可点元素,点击交给 nav.selectSession(id)。
// 用自定义 scheme 而不是 #fragment:真链接、锚点、路径都不会和它撞车。
function sessionLinkHTML(id, labelText) {
  const sid = String(id == null ? "" : id).trim().replace(/\/+$/, "");
  const shown = labelText != null ? mdUnescape(labelText) : "会话 " + sid.slice(0, 8);
  return '<a class="md-sess" role="button" tabindex="0" title="跳转到会话 ' + esc(sid) +
    '" data-session="' + esc(sid) + '">💬 ' + escFast(shown) + "</a>";
}

// mdLinkifyPaths 在已转义的纯文本里把裸路径替换成链接。
// 只在"还没有标签"的阶段调用(行内代码/图片/链接已进 stash),所以不会咬到 HTML。
// 裸文本只认"明确像路径"的写法:盘符/UNC/绝对/./../~/ 开头,
// 或带分隔符的相对路径(web/ui/index.html)。单个文件名(main.go)不动 ——
// 正文里提到 main.go 太常见,全变红字反而吵;要打开就包反引号或用链接。
export function mdLinkifyPaths(s, keep) {
  if (s.indexOf(":") < 0 && s.indexOf("/") < 0 && s.indexOf("\\") < 0) return s;
  // 第三段 `[\w.-]+\.[\w]+:\d+` 专收"文件名:行号":它没有分隔符,但行号让它
  // 明确是路径而不是正文里的普通单词,值得做成可点(否则 a.go:12 只能手抄)。
  const re = /(?:[a-z]:[\\/]|\\\\|\/|\.{1,2}[\\/]|~[\\/])[^\s<>"'`|]*[^\s<>"'`|.,;:!?)\]}]|[\w.\-]+[\\/][^\s<>"'`|]*[^\s<>"'`|.,;:!?)\]}]|[\w.\-]+\.[a-z0-9]+:\d+(?::\d+)?/gi;
  return s.replace(re, (m, off, str) => {
    // 前面紧贴字母数字或冒号的内容不是路径起点(a/b 里的 /、http: 后的 //)。
    // 例外:盘符路径本身以 "D:" 开头,前面若正好是中文标点冒号(改好了:D:\x),
    // 不能因为那个冒号把整条路径丢掉。
    const prev = off > 0 ? str[off - 1] : "";
    if (/[0-9a-z_\-:.]/i.test(prev) && !/^[a-z]:[\\/]/i.test(m)) return m;
    if (!mdIsFilePath(m)) return m;
    const trail = /[.,;:!?]$/.exec(m);
    const path = trail ? m.slice(0, -1) : m;
    return keep(fileOpenLinkHTML(path, null)) + (trail ? trail[0] : "");
  });
}

// 语言别名归一(代码块高亮用)
const MD_LANG_ALIAS = {
  javascript: "js", jsx: "js", typescript: "ts", tsx: "ts", python: "py", python3: "py",
  golang: "go", "c++": "cpp", cpp: "cpp", cc: "cpp", cxx: "cpp", "c#": "cs", csharp: "cs",
  shell: "sh", bash: "sh", zsh: "sh", powershell: "sh", ps1: "sh", yml: "yaml",
  plaintext: "text", txt: "text",
};
// 各语言关键词(仅高亮用,取最常用的)
const MD_KEYWORDS = {
  js: "abstract async await break case catch class const continue debugger default delete do else enum export extends false finally for function get if import in instanceof interface let new null of package private protected public return set static super switch this throw true try typeof undefined var void while with yield",
  ts: "as any boolean keyof namespace never readonly string unknown implements",
  py: "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass print raise return try while with yield",
  go: "append bool byte cap case chan const continue default defer else fallthrough false float32 float64 for func go goto if imag import interface int int64 iota len make map new nil package range return select string struct switch true type uint uint64 var",
  java: "abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while record var",
  c: "auto break case char const continue default do double else enum extern float for goto if inline int long register return short signed sizeof static struct switch typedef union unsigned void volatile while NULL true false",
  cpp: "alignas alignof bool case catch char class const constexpr continue default delete do double else enum explicit extern false float for friend goto if int long mutable namespace new noexcept nullptr operator private protected public return short signed sizeof static std string switch template this throw true try typedef typename union unsigned using virtual void volatile",
  cs: "abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void volatile while var record global async await",
  rust: "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while Some Ok Err None String Vec Box Option Result HashMap",
  sh: "case cd chmod chown continue do done echo elif else esac exit export fi for function if in local pwd read return set shift then unset while",
  sql: "add all alter and any as asc between by case cast check column constraint create cross database default delete desc distinct drop else end except exists foreign from full group having in inner insert into is join key left like limit not null offset on or order outer primary references right select set table then union unique update values view when where with",
  yaml: "true false null yes no on off",
  json: "true false null",
};
function highlightCode(code, lang) {
  const l = (MD_LANG_ALIAS[lang] || lang || "").toLowerCase();
  let kws = (MD_KEYWORDS[l] || "").split(" ").filter(Boolean);
  if (l === "ts") kws = kws.concat(MD_KEYWORDS.js.split(" ").filter(Boolean));
  if (!kws.length) return escFast(code);
  const kwPart = "\\b(?:" + kws.map(mdEscRe).join("|") + ")\\b";
  const comments = ["(?://[^\n]*|/\\*[\\s\\S]*?(?:\\*/|$))"];
  if (l === "py" || l === "sh" || l === "yaml") comments.push("#[^\\n]*");
  if (l === "sql") comments.push("--[^\\n]*");
  const re = new RegExp(
    "(\"\"\"[\\s\\S]*?\"\"\"|'''[\\s\\S]*?''')" +          // 三引号字符串
    "|(\"(?:\\\\.|[^\"\\\\\\n])*\"?|'(?:\\\\.|[^'\\\\\\n])*'?)" + // 字符串(尾部 ? 容忍流式中未闭合)
    "|(`(?:\\\\.|[^`\\\\])*`?)" +                              // 反引号字符串
    "|" + comments.join("|") +                                  // 注释
    "|\\b(?:0[xX][0-9a-fA-F_]+|\\d[\\d_]*(?:\\.\\d+)?(?:[eE][+-]?\\d+)?)\\b" + // 数字
    "|" + kwPart,                                               // 关键字
    "g"
  );
  const escd = escFast(code);
  let out = "", last = 0, m;
  while ((m = re.exec(escd)) !== null) {
    out += escd.slice(last, m.index);
    const s = m[0];
    let cls;
    if (s[0] === '"' || s[0] === "'" || s[0] === "`") cls = "tok-s";
    else if (s.indexOf("//") === 0 || s.indexOf("/*") === 0 || s[0] === "#" || s.indexOf("--") === 0) cls = "tok-c";
    else if (/^\d/.test(s)) cls = "tok-n";
    else cls = "tok-k";
    out += '<span class="' + cls + '">' + s + "</span>";
    last = m.index + s.length;
    if (s.length === 0) re.lastIndex++; // 防御零宽匹配死循环
  }
  out += escd.slice(last);
  return out;
}
// JSON 美化 + 高亮(调用方保证已能 JSON.parse)
function highlightJsonPretty(obj) {
  const pretty = escFast(JSON.stringify(obj, null, 2));
  return pretty.replace(
    /("(?:\\u[0-9a-fA-F]{4}|\\[^u]|[^\\"])*")(?=\s*:)|("(?:\\u[0-9a-fA-F]{4}|\\[^u]|[^\\"])*")|\b(true|false)\b|\b(null)\b|(-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)/g,
    (m, key, str, bool, nul, num) => {
      if (key != null) return '<span class="j-key">' + key + "</span>";
      if (str != null) return '<span class="j-str">' + str + "</span>";
      if (bool != null) return '<span class="j-bool">' + bool + "</span>";
      if (nul != null) return '<span class="j-null">' + nul + "</span>";
      return '<span class="j-num">' + num + "</span>";
    }
  );
}
function codeBlockHtml(code, lang) {
  const l = (lang || "").trim().toLowerCase();
  let body = null, label = l || "code";
  // 无语言标签且内容像 JSON,或显式 json:先尝试美化
  if (l === "json" || (l === "" && /^\s*[{[]/.test(code))) {
    try { body = highlightJsonPretty(JSON.parse(code.trim())); label = "json"; } catch (e) { /* 非法 JSON,按普通代码 */ }
  }
  if (body == null) body = highlightCode(code, l);
  return '<div class="codeblock">' +
    '<div class="cb-head"><span class="cb-lang">' + escFast(label) + "</span>" +
    '<button type="button" class="cb-copy">复制</button></div>' +
    '<pre class="cb-pre"><code>' + body + "</code></pre></div>";
}
// 行内 Markdown:先转义,再 行内代码→图片→链接→加粗→斜体→删除线→裸路径→裸 URL
function inlineMd(raw) {
  let s = escFast(raw);
  const stash = [];
  const keep = (html) => { stash.push(html); return "\u0000" + (stash.length - 1) + "\u0000"; };
  // `反引号里的路径` 也做成可点:模型几乎总把路径包在反引号里,
  // 只给裸路径加链接的话,最常见的那种写法反而点不了。
  s = s.replace(/``([\s\S]+?)``|`([^`\n]+)`/g, (m, a, b) => {
    const code = a != null ? a : b;
    return keep(mdIsFilePath(code) ? fileOpenLinkHTML(code, "<code>") : "<code>" + code + "</code>");
  });
  s = s.replace(/!\[([^\]]*)\]\(\s*(https?:[^)\s"']+)(?:\s+"[^"]*")?\s*\)/g,
    (m, alt, url) => keep('<img class="md-img" src="' + url + '" alt="' + alt + '">'));
  s = s.replace(/\[([^\]]+)\]\(\s*([^)\s"']+)(?:\s+"[^"]*")?\s*\)/g, (m, txt, url) => {
    if (/^\s*(javascript|data|vbscript):/i.test(url)) return m; // 危险协议不转链接
    // [标题](mc-session://<id>):会话跳转芯片。必须排在 mdIsFilePath 之前,
    // 否则冒号形式的路径判断可能先把它抢走。
    if (/^mc-session:\/\//i.test(url)) return keep(sessionLinkHTML(url.replace(/^mc-session:\/\//i, ""), txt));
    // [在浏览器打开](D:\proj\index.html):本地路径同样是"点了就打开"
    if (mdIsFilePath(url)) return keep(fileOpenLinkHTML(url, null, txt));

    return '<a href="' + url + '" target="_blank" rel="noopener">' + txt + "</a>";
  });
  s = mdLinkifyPaths(s, keep);
  s = s.replace(/\*\*([^\s*](?:[^*]*[^\s*])?)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/(^|[^_])__(?!_)([^\s_](?:[^_]*[^\s_])?)__(?![\w])/g, (m, p, c) => p + "<strong>" + c + "</strong>");
  s = s.replace(/(^|[^*\w])\*(?!\s)([^*\n]*?[^\s*])\*(?![\w*])/g, (m, p, c) => p + "<em>" + c + "</em>");
  s = s.replace(/(^|[\s(])_(?!\s)([^_\n]*?[^\s_])_(?=[\s).,;:!?]|$)/g, (m, p, c) => p + "<em>" + c + "</em>"); // 仅词边界,避免破坏 snake_case
  s = s.replace(/~~([^\s~](?:[^~]*[^\s~])?)~~/g, "<del>$1</del>");
  // 裸 URL 自动加链接(前面是 " ' > 说明已在 href/src 或标签内,不重复处理)
  s = s.replace(/https?:\/\/[^\s<>"']+/g, (m, off, str) => {
    if (str[off - 1] === '"' || str[off - 1] === "'" || str[off - 1] === ">") return m;
    let u = m, trail = "";
    while (u.length && /[,.;:!?]$/.test(u)) { trail = u.slice(-1) + trail; u = u.slice(0, -1); } // 去掉末尾标点
    let bal = 0;
    for (let k = 0; k < u.length; k++) { if (u[k] === "(") bal++; else if (u[k] === ")") bal--; }
    while (u.endsWith(")") && bal < 0) { bal++; u = u.slice(0, -1); } // 去掉与句内 ( 配对的末尾 )
    if (u.length < 8) return m; // 太短,不像 URL
    return '<a href="' + u + '" target="_blank" rel="noopener">' + u + "</a>" + trail;
  });
  // 还原行内代码
  s = s.replace(/\u0000(\d+)\u0000/g, (m, n) => stash[+n]);
  return s;
}
// 表格行拆分:去掉首尾 | 后按 | 分列
function mdSplitRow(s) {
  let t = s.trim();
  if (t.startsWith("|")) t = t.slice(1);
  if (t.endsWith("|")) t = t.slice(0, -1);
  return t.split("|").map(c => c.trim());
}
// 列表项数组 → 嵌套 <ul>/<ol>(按缩进 2 空格 = 1 级)
function buildList(items) {
  const open = (t) => "<" + t + ">";
  const close = (t) => "</" + t + ">";
  let html = "";
  const stack = [];
  const closeTop = () => {
    const e = stack.pop();
    if (e.liOpen) { html += "</li>"; e.liOpen = false; }
    html += close(e.type);
  };
  for (const it of items) {
    const lv = Math.min(4, Math.floor(it.indent / 2));
    const type = it.ordered ? "ol" : "ul";
    while (stack.length && stack[stack.length - 1].lv > lv) closeTop();
    const top = stack[stack.length - 1];
    if (!top) {
      stack.push({ type, lv, liOpen: false });
      html += open(type);
    } else if (lv === top.lv) {
      if (top.type !== type) {
        // 同级但有序/无序切换:收起当前项与列表,另开一个
        if (top.liOpen) { html += "</li>"; top.liOpen = false; }
        html += close(stack.pop().type) + open(type);
        stack.push({ type, lv, liOpen: false });
      } else if (top.liOpen) {
        // 同级兄弟项:先闭合上一项(更深一级时不闭合,嵌套列表要进父 li)
        html += "</li>"; top.liOpen = false;
      }
    }
    if (lv > stack[stack.length - 1].lv) {
      stack.push({ type, lv, liOpen: false });
      html += open(type);
    }
    html += "<li>" + it.text.split("\n").map(inlineMd).join("<br>");
    stack[stack.length - 1].liOpen = true;
  }
  while (stack.length) closeTop();
  return html;
}
// Markdown → HTML(入口)。src 为纯文本时退化为 <p>+<br>,视觉上与原 pre-wrap 一致。
export function mdToHtml(src) {
  const raw = String(src == null ? "" : src).replace(/\r\n?/g, "\n");
  if (!raw.trim()) return "";
  // 整条消息就是 JSON(无围栏)→ 直接按 json 代码块渲染(美化 + 高亮 + 复制)
  const t0 = raw.trim();
  if ((t0[0] === "{" || t0[0] === "[") && raw.indexOf("```") < 0 && raw.indexOf("~~~") < 0) {
    try {
      JSON.parse(t0);
      return codeBlockHtml(t0, "json");
    } catch (e) { /* 不是合法 JSON,走普通 markdown */ }
  }
  const lines = raw.split("\n");
  const out = [];
  const isBlank = (l) => /^\s*$/.test(l);
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    // 围栏代码块(流式输出中未闭合的也按代码块兜底)
    const fence = line.match(/^\s{0,3}(`{3,}|~{3,})\s*([^\s`]*)\s*$/);
    if (fence) {
      const ch = fence[1][0], n = fence[1].length;
      const buf = [];
      i++;
      while (i < lines.length) {
        const c = lines[i].match(/^\s{0,3}(`{3,}|~{3,})\s*$/);
        if (c && c[1][0] === ch && c[1].length >= n) { i++; break; }
        buf.push(lines[i]); i++;
      }
      out.push(codeBlockHtml(buf.join("\n"), fence[2] || ""));
      continue;
    }
    // 标题
    const h = line.match(/^\s{0,3}(#{1,6})\s+(.+?)\s*$/);
    if (h) {
      const title = h[2].replace(/\s+#+\s*$/, ""); // 仅去掉"空格+井号"的闭合
      out.push("<h" + h[1].length + ">" + inlineMd(title) + "</h" + h[1].length + ">");
      i++; continue;
    }
    // 分隔线
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) { out.push("<hr>"); i++; continue; }
    // 引用
    if (/^\s{0,3}>\s?/.test(line)) {
      const buf = [];
      while (i < lines.length && /^\s{0,3}>\s?/.test(lines[i])) { buf.push(lines[i].replace(/^\s{0,3}>\s?/, "")); i++; }
      out.push("<blockquote>" + mdToHtml(buf.join("\n")) + "</blockquote>");
      continue;
    }
    // 表格(当前行含 |,下一行是 |---| 分隔行)
    if (line.includes("|") && i + 1 < lines.length) {
      const sepCells = mdSplitRow(lines[i + 1]);
      if (lines[i + 1].includes("|") && sepCells.length > 0 &&
          sepCells.every(c => /^:?-+:?$/.test(c))) {
        const head = mdSplitRow(line);
        const aligns = sepCells.map(c => c.startsWith(":") && c.endsWith(":") ? "center" : c.startsWith(":") ? "left" : (c.endsWith(":") ? "right" : ""));
        i += 2;
        const rows = [];
        while (i < lines.length && lines[i].includes("|") && !isBlank(lines[i])) { rows.push(mdSplitRow(lines[i])); i++; }
        let tb = '<div class="md-tb"><table><thead><tr>';
        head.forEach((c, k) => { tb += "<th" + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : "") + ">" + inlineMd(c) + "</th>"; });
        tb += "</tr></thead><tbody>";
        for (const r of rows) {
          tb += "<tr>";
          for (let k = 0; k < head.length; k++) tb += "<td" + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : "") + ">" + inlineMd(r[k] || "") + "</td>";
          tb += "</tr>";
        }
        tb += "</tbody></table></div>";
        out.push(tb);
        continue;
      }
    }
    // 列表(无序 - * + / 有序 1. 1),支持缩进嵌套
    const li = line.match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
    if (li) {
      const items = [];
      while (i < lines.length) {
        const m2 = lines[i].match(/^(\s*)([-*+]|\d+[.)])\s+(.*)$/);
        if (m2) {
          items.push({
            indent: m2[1].replace(/\t/g, "  ").length,
            ordered: /\d/.test(m2[2][0]),
            text: m2[3],
          });
          i++;
        } else if (isBlank(lines[i])) {
          const j = i + 1;
          // 空行后仍是缩进列表行 → 同一列表;否则列表结束
          if (j < lines.length && /^\s{2,}([-*+]|\d+[.)])\s+/.test(lines[j])) { i++; continue; }
          break;
        } else if (/^\s{2,}\S/.test(lines[i])) {
          items[items.length - 1].text += "\n" + lines[i].trim(); i++; // 缩进续行并入上一项
        } else break;
      }
      out.push(buildList(items));
      continue;
    }
    if (isBlank(line)) { i++; continue; }
    // 段落:连续行用 <br> 连接(保持 pre-wrap 的换行观感)
    const buf = [line];
    i++;
    while (i < lines.length && !isBlank(lines[i]) &&
      !/^\s{0,3}(#{1,6}\s|>\s?|`{3,}|~{3,})/.test(lines[i]) &&
      !/^(\s*)([-*+]|\d+[.)])\s+/.test(lines[i]) &&
      !/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(lines[i])) {
      buf.push(lines[i]); i++;
    }
    out.push("<p>" + buf.map(inlineMd).join("<br>") + "</p>");
  }
  return out.join("");
}

// 代码块复制按钮的兜底实现(剪贴板 API 需要安全上下文,WebView 里不一定有)。
function copyText(s) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(s).catch(() => legacyCopy(s));
  } else legacyCopy(s);
  function legacyCopy(str) {
    try {
      const ta = document.createElement("textarea");
      ta.value = str;
      ta.style.cssText = "position:fixed;opacity:0;pointer-events:none;";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
    } catch (e) { /* 忽略 */ }
  }
}

/* ---------------- 聊天里的文件路径:点了就用系统程序打开 ---------------- */
// 相对路径按当前会话的工作目录解析(对话说的项目 ≠ 服务进程的 cwd)。
export async function openFilePath(path) {
  const p = String(path || "").trim();
  if (!p) return;
  try {
    await api(ROUTES.fsOpen, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: p, base: state.workDir || "" }),
    });
    toast("已打开 " + p, "ok");
  } catch (e) {
    toast("打开失败:" + e.message, "err");
  }
}

// activateMDLink:.md-file → 用系统默认程序打开那个路径;.md-sess → 跳到那个会话。
// selectSession 直接按 id 打 /api/sessions/{id},所以已归档的会话同样能跳。
export function activateMDLink(a) {
  if (!a.classList.contains("md-sess")) { openFilePath(a.dataset.file || a.textContent); return; }
  const sid = (a.dataset.session || "").trim();
  if (!sid) return;
  if (!nav.selectSession) { toast("无法跳转:界面尚未就绪", "err"); return; }
  Promise.resolve(nav.selectSession(sid))
    .then(() => toast("已跳到会话 " + sid.slice(0, 8), "ok"))
    .catch(() => toast("找不到会话 " + sid.slice(0, 8), "err"));
}

// bindMarkdownLinks 只绑一次 document 级委托(复制按钮 / 路径链接 / 会话芯片)。
// 用委托而不是给每个 codeblock 挂 handler:流式新增的块也能立刻生效。
let bound = false;
export function bindMarkdownLinks() {
  if (bound) return;
  bound = true;
  document.addEventListener("click", (e) => {
    const btn = e.target.closest(".cb-copy");
    if (btn) {
      const box = btn.closest(".codeblock");
      const code = box && box.querySelector(".cb-pre code");
      copyText(code ? code.textContent : "");
      const old = btn.textContent;
      btn.textContent = "✓ 已复制";
      btn.classList.add("ok");
      clearTimeout(btn._t);
      btn._t = setTimeout(() => { btn.textContent = old; btn.classList.remove("ok"); }, 1200);
      return;
    }
    const a = e.target.closest(".md-file, .md-sess");
    if (!a) return;
    e.preventDefault();
    activateMDLink(a);
  });
  // 键盘同样可用:它是一个 role=button 的链接
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const a = e.target.closest && e.target.closest(".md-file, .md-sess");
    if (!a) return;
    e.preventDefault();
    activateMDLink(a);
  });
}
