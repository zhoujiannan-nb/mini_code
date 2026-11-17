import { api, ROUTES } from "./api.js";
import { state, toast } from "./store.js";
import { nav } from "./nav.js";

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
function mdUnescape(s) {
  return String(s).replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

const MD_FILE_EXT = /\.(html?|xhtml|css|scss|sass|less|js|mjs|cjs|jsx|ts|tsx|json|jsonc|ya?ml|toml|ini|cfg|conf|xml|svg|md|markdown|txt|log|csv|tsv|pdf|docx?|xlsx?|pptx?|png|jpe?g|gif|webp|bmp|ico|avif|mp4|webm|mov|mp3|wav|ogg|flac|zip|rar|7z|gz|tar|exe|msi|dll|go|py|rb|rs|java|kt|c|h|cpp|hpp|cs|php|lua|sh|bat|cmd|ps1|sql|db|sqlite3?|vue|svelte|astro)$/i;

function mdSplitLine(raw) {
  const m = /^(.*?)(:\d+(?::\d+)?)$/.exec(String(raw == null ? "" : raw));
  return m ? { path: m[1], line: m[2] } : { path: String(raw == null ? "" : raw), line: "" };
}

export function mdIsFilePath(raw) {
  const p = mdSplitLine(mdUnescape(String(raw == null ? "" : raw)).trim()).path;
  if (!p || p.length > 260) return false;
  if (/^[a-z][a-z0-9+.\-]*:\/\//i.test(p)) return false; 
  if (/[\s<>"\u0000|]/.test(p)) return false;            
  if (/[?*]/.test(p)) return false;                      
  if (/^[a-z]:[\\/]/i.test(p)) return true;              
  if (/^(\\\\|\/|\.{1,2}[\\/]|~[\\/])/.test(p)) return true; 
  return MD_FILE_EXT.test(p);
}

export function fileOpenLinkHTML(raw, wrapOpen, labelText) {
  const rawPath = mdUnescape(String(raw == null ? "" : raw)).trim();
  const path = mdSplitLine(rawPath).path;
  const shown = labelText != null ? mdUnescape(labelText) : rawPath;
  const inner = wrapOpen ? wrapOpen + escFast(shown) + "</code>" : escFast(shown);
  return '<a class="md-file" role="button" tabindex="0" title="点击打开 ' + esc(path) +
    '" data-file="' + esc(path) + '">' + inner + "</a>";
}

function sessionLinkHTML(id, labelText) {
  const sid = String(id == null ? "" : id).trim().replace(/\/+$/, "");
  const shown = labelText != null ? mdUnescape(labelText) : "会话 " + sid.slice(0, 8);
  return '<a class="md-sess" role="button" tabindex="0" title="跳转到会话 ' + esc(sid) +
    '" data-session="' + esc(sid) + '">💬 ' + escFast(shown) + "</a>";
}

export function mdLinkifyPaths(s, keep) {
  if (s.indexOf(":") < 0 && s.indexOf("/") < 0 && s.indexOf("\\") < 0) return s;
  const re = /(?:[a-z]:[\\/]|\\\\|\/|\.{1,2}[\\/]|~[\\/])[^\s<>"'`|]*[^\s<>"'`|.,;:!?)\]}]|[\w.\-]+[\\/][^\s<>"'`|]*[^\s<>"'`|.,;:!?)\]}]|[\w.\-]+\.[a-z0-9]+:\d+(?::\d+)?/gi;
  return s.replace(re, (m, off, str) => {
    const prev = off > 0 ? str[off - 1] : "";
    if (/[0-9a-z_\-:.]/i.test(prev) && !/^[a-z]:[\\/]/i.test(m)) return m;
    if (!mdIsFilePath(m)) return m;
    const trail = /[.,;:!?]$/.exec(m);
    const path = trail ? m.slice(0, -1) : m;
    return keep(fileOpenLinkHTML(path, null)) + (trail ? trail[0] : "");
  });
}

const MD_LANG_ALIAS = {
  javascript: "js", jsx: "js", typescript: "ts", tsx: "ts", python: "py", python3: "py",
  golang: "go", "c++": "cpp", cpp: "cpp", cc: "cpp", cxx: "cpp", "c#": "cs", csharp: "cs",
  shell: "sh", bash: "sh", zsh: "sh", powershell: "sh", ps1: "sh", yml: "yaml",
  plaintext: "text", txt: "text",
};
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
    "(\"\"\"[\\s\\S]*?\"\"\"|'''[\\s\\S]*?''')" +          
    "|(\"(?:\\\\.|[^\"\\\\\\n])*\"?|'(?:\\\\.|[^'\\\\\\n])*'?)" + 
    "|(`(?:\\\\.|[^`\\\\])*`?)" +                              
    "|" + comments.join("|") +                                  
    "|\\b(?:0[xX][0-9a-fA-F_]+|\\d[\\d_]*(?:\\.\\d+)?(?:[eE][+-]?\\d+)?)\\b" + 
    "|" + kwPart,                                               
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
    if (s.length === 0) re.lastIndex++; 
  }
  out += escd.slice(last);
  return out;
}
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
  if (l === "json" || (l === "" && /^\s*[{[]/.test(code))) {
    try { body = highlightJsonPretty(JSON.parse(code.trim())); label = "json"; } catch (e) {  }
  }
  if (body == null) body = highlightCode(code, l);
  return '<div class="codeblock">' +
    '<div class="cb-head"><span class="cb-lang">' + escFast(label) + "</span>" +
    '<button type="button" class="cb-copy">复制</button></div>' +
    '<pre class="cb-pre"><code>' + body + "</code></pre></div>";
}
function inlineMd(raw) {
  let s = escFast(raw);
  const stash = [];
  const keep = (html) => { stash.push(html); return "\u0000" + (stash.length - 1) + "\u0000"; };
  s = s.replace(/``([\s\S]+?)``|`([^`\n]+)`/g, (m, a, b) => {
    const code = a != null ? a : b;
    return keep(mdIsFilePath(code) ? fileOpenLinkHTML(code, "<code>") : "<code>" + code + "</code>");
  });
  s = s.replace(/!\[([^\]]*)\]\(\s*(https?:[^)\s"']+)(?:\s+"[^"]*")?\s*\)/g,
    (m, alt, url) => keep('<img class="md-img" src="' + url + '" alt="' + alt + '">'));
  s = s.replace(/\[([^\]]+)\]\(\s*([^)\s"']+)(?:\s+"[^"]*")?\s*\)/g, (m, txt, url) => {
    if (/^\s*(javascript|data|vbscript):/i.test(url)) return m; 
    if (/^mc-session:\/\//i.test(url)) return keep(sessionLinkHTML(url.replace(/^mc-session:\/\//i, ""), txt));
    if (mdIsFilePath(url)) return keep(fileOpenLinkHTML(url, null, txt));

    return '<a href="' + url + '" target="_blank" rel="noopener">' + txt + "</a>";
  });
  s = mdLinkifyPaths(s, keep);
  s = s.replace(/\*\*([^\s*](?:[^*]*[^\s*])?)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/(^|[^_])__(?!_)([^\s_](?:[^_]*[^\s_])?)__(?![\w])/g, (m, p, c) => p + "<strong>" + c + "</strong>");
  s = s.replace(/(^|[^*\w])\*(?!\s)([^*\n]*?[^\s*])\*(?![\w*])/g, (m, p, c) => p + "<em>" + c + "</em>");
  s = s.replace(/(^|[\s(])_(?!\s)([^_\n]*?[^\s_])_(?=[\s).,;:!?]|$)/g, (m, p, c) => p + "<em>" + c + "</em>"); 
  s = s.replace(/~~([^\s~](?:[^~]*[^\s~])?)~~/g, "<del>$1</del>");
  s = s.replace(/https?:\/\/[^\s<>"']+/g, (m, off, str) => {
    if (str[off - 1] === '"' || str[off - 1] === "'" || str[off - 1] === ">") return m;
    let u = m, trail = "";
    while (u.length && /[,.;:!?]$/.test(u)) { trail = u.slice(-1) + trail; u = u.slice(0, -1); } 
    let bal = 0;
    for (let k = 0; k < u.length; k++) { if (u[k] === "(") bal++; else if (u[k] === ")") bal--; }
    while (u.endsWith(")") && bal < 0) { bal++; u = u.slice(0, -1); } 
    if (u.length < 8) return m; 
    return '<a href="' + u + '" target="_blank" rel="noopener">' + u + "</a>" + trail;
  });
  s = s.replace(/\u0000(\d+)\u0000/g, (m, n) => stash[+n]);
  return s;
}
function mdSplitRow(s) {
  let t = s.trim();
  if (t.startsWith("|")) t = t.slice(1);
  if (t.endsWith("|")) t = t.slice(0, -1);
  return t.split("|").map(c => c.trim());
}
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
        if (top.liOpen) { html += "</li>"; top.liOpen = false; }
        html += close(stack.pop().type) + open(type);
        stack.push({ type, lv, liOpen: false });
      } else if (top.liOpen) {
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
export function mdToHtml(src) {
  const raw = String(src == null ? "" : src).replace(/\r\n?/g, "\n");
  if (!raw.trim()) return "";
  const t0 = raw.trim();
  if ((t0[0] === "{" || t0[0] === "[") && raw.indexOf("```") < 0 && raw.indexOf("~~~") < 0) {
    try {
      JSON.parse(t0);
      return codeBlockHtml(t0, "json");
    } catch (e) {  }
  }
  const lines = raw.split("\n");
  const out = [];
  const isBlank = (l) => /^\s*$/.test(l);
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
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
    const h = line.match(/^\s{0,3}(#{1,6})\s+(.+?)\s*$/);
    if (h) {
      const title = h[2].replace(/\s+#+\s*$/, ""); 
      out.push("<h" + h[1].length + ">" + inlineMd(title) + "</h" + h[1].length + ">");
      i++; continue;
    }
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) { out.push("<hr>"); i++; continue; }
    if (/^\s{0,3}>\s?/.test(line)) {
      const buf = [];
      while (i < lines.length && /^\s{0,3}>\s?/.test(lines[i])) { buf.push(lines[i].replace(/^\s{0,3}>\s?/, "")); i++; }
      out.push("<blockquote>" + mdToHtml(buf.join("\n")) + "</blockquote>");
      continue;
    }
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
          if (j < lines.length && /^\s{2,}([-*+]|\d+[.)])\s+/.test(lines[j])) { i++; continue; }
          break;
        } else if (/^\s{2,}\S/.test(lines[i])) {
          items[items.length - 1].text += "\n" + lines[i].trim(); i++; 
        } else break;
      }
      out.push(buildList(items));
      continue;
    }
    if (isBlank(line)) { i++; continue; }
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
    } catch (e) {  }
  }
}

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

export function activateMDLink(a) {
  if (!a.classList.contains("md-sess")) { openFilePath(a.dataset.file || a.textContent); return; }
  const sid = (a.dataset.session || "").trim();
  if (!sid) return;
  if (!nav.selectSession) { toast("无法跳转:界面尚未就绪", "err"); return; }
  Promise.resolve(nav.selectSession(sid))
    .then(() => toast("已跳到会话 " + sid.slice(0, 8), "ok"))
    .catch(() => toast("找不到会话 " + sid.slice(0, 8), "err"));
}

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
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const a = e.target.closest && e.target.closest(".md-file, .md-sess");
    if (!a) return;
    e.preventDefault();
    activateMDLink(a);
  });
}
