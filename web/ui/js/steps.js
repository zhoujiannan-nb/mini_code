/* ---------------- 执行过程归并(steps) ----------------
   agent 一轮任务常常是:思考 → 调工具 → 思考 → 调工具 → …… → 最后一段正文。
   中间这些"只有思考、没有正文"的步骤若一条条平铺,屏幕全是工具卡(刷屏)。
   这里把它们收进一条「执行过程」折叠条:平时只有一行摘要(N 步 · M 次思考),
   点开才见每一步(参数 + 结果 + 该步思考);等真正有正文的回复出现,这条折叠条
   会被并进那张卡片的正文上方。整轮都没有正文时,折叠条独立成卡,信息不丢。
   流式期间同样用这条折叠条:它是活的,步数实时往上加,想看就点开。 */
 var STEPS = { card: null, det: null, body: null }; // 步数不在这存:见 stepsPaint 的注释

function stepsReset() {            // 消息容器被清空(renderHistory)后丢掉引用
  liveHoldCancel();               // 正在"展示中"的那步跟着容器一起没了,定时器要撤
  stepsRunStop();
  stepsStripReset();
  LIVE.card = null; LIVE.body = null; // 「正在执行」卡随容器一起没了,只丢引用
}
// stepsStripReset:只丢折叠条的引用(它被并进正文卡、或任务收尾)。
// 「正在执行」卡可能还挂在界面上跑着,不能跟着一起丢 —— 丢了就再也搬不回去,
// 界面底下会永远留一张空转的"正在执行"卡。
function stepsStripReset() {
  STEPS.card = null; STEPS.det = null; STEPS.body = null;
}
function stepsStrip() {            // 懒创建折叠条(一张独立卡,逐步往里加)
   if (STEPS.det && STEPS.det.isConnected) return STEPS.det;
   stepsStripReset();
   const div = addCard("steps",
     '<div class="m-card"><details class="m-steps"><summary></summary>' +
     '<div class="m-steps-wrap"><div class="m-steps-body"></div></div></details></div>');
    STEPS.card = div;
    // 折叠条也带上 agent 头像:进行中那段时间左边不是一片空白,和下面的回答卡左对齐
    // (.msg.steps 在 layers.css v8 里已改成 flex 行 + 去掉原来的左缩进)
    div.insertAdjacentHTML("afterbegin", agentAvatarBox("c-avatar"));
    STEPS.det = div.querySelector("details");
   STEPS.body = div.querySelector(".m-steps-body");
   // <details> 被浏览器原生开合时不会走我们的重绘,这里挂个 toggle 只改右侧提示字
   STEPS.det.addEventListener("toggle", function () {
     const h = this.querySelector(".ms-hint");
     if (h) h.textContent = this.open ? "收起" : "展开";
   });
   stepsPaint();
   return STEPS.det;
}

function stepsPaint() {
  if (!STEPS.det) return;
  /* 步数一律从 DOM 数,不用累加器。
     曾经的 bug:实时步骤住在折叠条外面的「正在执行」卡里,累加器在它还没住进来时
     就 +1;而折叠条是懒创建的,创建时 stepsStripReset() 把已经算过的数清零 —— 于是
     界面出现"执行过程 0 步",点开却躺着一堆步骤。DOM 里有几条就是几条,永远对得上。 */
  let n = STEPS.body ? STEPS.body.children.length : 0;
  if (LIVE.body && LIVE.body.isConnected) n += LIVE.body.children.length;
  const th = STEPS.body ? STEPS.body.querySelectorAll(".m-think").length : 0;
  const last = stepsLastHint();
  STEPS.det.querySelector("summary").innerHTML =
    '<span class="ms-t">执行过程</span>' +
    '<span class="ms-n">' + n + ' 步</span>' +
    (th ? '<span class="ms-n">' + th + ' 次思考</span>' : '') +
    (last ? '<span class="ms-last">' + esc(last) + '</span>' : '') +
    '<span class="ms-hint">' + (STEPS.det.open ? "收起" : "展开") + '</span>';
}
// stepsLastHint:最近一步的"名字 + 回执摘要"。折叠条平时是收着的,这一行让
// "刚跑了哪个工具、结果如何"不用点开也看得见(写文件的回执尤其想一眼看到)。
function stepsLastHint() {
  if (!STEPS.body) return "";
  const kids = [].slice.call(STEPS.body.children);
  for (let i = kids.length - 1; i >= 0; i--) {
    const name = kids[i].querySelector(".t-name");
    if (!name) continue;                       // 纯思考步骤不当"最近一步"
    const tag = kids[i].querySelector(".ms-tag");
    const t = tag ? tag.textContent.trim() : "";
    return "最近 " + name.textContent.trim() + (t ? " · " + t : "");
  }
  return "";
}
function stepsPush(html, isThink, tag) {
   const body = stepsStrip().querySelector(".m-steps-body");
   const d = document.createElement(tag || "div");
   d.className = "ms-step";
   d.innerHTML = html;
   body.appendChild(d);
   stepsPaint();
   if (!histRendering && nearBottom()) scrollBottom(false, true);
   // 只在"追加当下"淡入一次;历史重绘是成批建卡,动画会让整屏一起闪,
   // 所以 histRendering 期间直接给最终态。
   if (!histRendering) {
     d.classList.add("ms-in");
     setTimeout(() => d.classList.remove("ms-in"), 220);
   }
   return d;
}

/* ---- 工具参数:标题行只放"这次调了什么" ----
    真实问题:以前把整段 JSON 参数(args 本身是字符串)塞进标题行的单个 span,
    长参数(尤其 exec 的命令行)会把工具名挤到换行、和参数黏在一起。
    这里按工具名抽出最关键的一个字段,压成单行;参数全文仍在展开区里。 */
const TOOL_ARG_KEY = {
   exec: "command", bash: "command", shell: "command", run: "command",
   read_file: "path", write_file: "path", edit_file: "path", list_dir: "path",
   search_files: "pattern", find: "pattern", grep: "pattern",
   web_search: "query", fetch: "url", http: "url", task: "goal", todo: "task",
};

// toolArgsOf 把 args 解析成对象;解析不了就返回 null(不代表出错)。
function toolArgsOf(args) {
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
function toolArgOne(name, args) {
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
// 但它是一条命令,不能被当成路径截成尾部两段,否则一条 cd && dir && findstr
// 会被显示成 "…/js/steps.js",读者完全看不出这次执行了什么。
function isPathKey(k) {
  const s = String(k || "").toLowerCase();
  return s === "path" || s === "file" || s === "dir" || s === "cwd" ||
    s === "target" || s === "dest" || s === "source" ||
    /(^|_)(path|file|dir|cwd)$/.test(s);
}

// toolArgLine 渲染标题行里的那一小段参数:单行 + 省略号,完整值放 title。
//   - 路径:只留最后两段(前面的目录对"这次动了什么"没有信息量);
//   - 其它(命令/查询/目标):压成一行,过长就截断。
function toolArgLine(name, args) {
  const got = toolArgOne(name, args);
  if (!got) {
    const raw = args == null ? "" : String(args).replace(/\s+/g, " ").trim();
    if (!raw) return "";
    return '<span class="ms-arg1" title="' + esc(raw) + '">' + esc(raw) + "</span>";
  }
  let shown = got.v.replace(/\s+/g, " ").trim();
  if (isPathKey(got.k)) {
    const parts = shown.split(/[\\/]+/).filter(Boolean);
    if (parts.length > 3) shown = "…/" + parts.slice(-2).join("/");
  }
  const brief = shown.length > 58 ? shown.slice(0, 58) + "…" : shown;
  return '<span class="ms-arg1" title="' + esc(got.v) + '">' + esc(brief) + "</span>";
}


/* ---- 正在执行的那一步不折叠 ----
   折叠条是给"回头看过程"用的,不适合放"此刻在跑什么":跑着的那步排在折叠列表
   最后一行,想看还得先展开、再滚动,计时数字也跟着藏在折叠里。所以实时调用的步骤
   先住在折叠条外面一张「正在执行」小卡里,跑完那一刻再搬进折叠条 —— 搬进去的位置
   就是它在整个过程中的次序,过程记录仍然完整。 */
var LIVE = { card: null, body: null };

function stepsLiveStrip() {
  if (LIVE.body && LIVE.body.isConnected) return LIVE.body;
  LIVE.card = null;
  // 借用 steps 那一档的缩进,和「执行过程」卡左对齐成一组
  LIVE.card = addCard("steps",
    '<div class="m-card ms-live"><div class="ms-live-hd"><span class="pulse gray"></span>正在执行</div>' +
    '<div class="ms-live-body"></div></div>');
  LIVE.body = LIVE.card.querySelector(".ms-live-body");
  LIVE.card.insertAdjacentHTML("afterbegin", agentAvatarBox("c-avatar")); // 同「执行过程」条:带 agent 头像
  return LIVE.body;
}
function stepsLivePush(html) {
  liveHoldFlush(); // 有更快回来的步骤还"展示中"：先收进折叠条，顺序才不会颠倒
  const d = document.createElement("details");
  d.className = "ms-step ms-live-step";
  d.innerHTML = html;
  stepsLiveStrip().appendChild(d);
  if (!histRendering) {
    d.classList.add("ms-in");
    setTimeout(() => d.classList.remove("ms-in"), 220);
    if (nearBottom()) scrollBottom(false, true);
  }
  return d;
}
// stepsLiveDone:这一步跑完了,搬回折叠条。折叠卡可能是这一步才懒创建的
// (会话开头第一个动作就是工具),所以顺手把「正在执行」卡挪到它下面,
// 顺序永远是"过程列表在上、正在执行的在下"。
// stepsFoldStep:把一步从「正在执行」卡收回折叠条 —— 收起、撤掉实时输出、补回参数
// 全文。回到折叠条里它就是个普通历史步骤,完整输出在结果里,不该有第二份。
function stepsFoldStep(st) {
  st.open = false;
  const out = st.querySelector(".ms-out");
  if (out) out.remove();
  if (st._args) {
    const body = st.querySelector(".ms-step-body");
    if (body) {
      const a = String(st._args);
      body.insertAdjacentHTML("afterbegin",
        '<div class="ms-args">' + esc(a.slice(0, 2000)) + (a.length > 2000 ? " …" : "") + "</div>");
    }
    st._args = null;
  }
  STEPS.body.appendChild(st);
  st.classList.remove("ms-live-step");
}
/* ---- 快工具至少让人看一眼 ----
   写文件几十毫秒就回来了:「正在执行」卡一闪就没,参数预览压根来不及读。所以跑完的
   那一步先在卡里待够 LIVE_MIN_MS(这段时间里展开区是参数预览 + 刚回来的回执),再搬进
   折叠条。新步骤一到就立刻搬,顺序不会乱;本轮过程收尾(出正文/任务结束)也立刻搬,
   绝不会有步骤被计时器留在界面底下。 */
const LIVE_MIN_MS = 900;
let liveHold = null; // { st, timer }

function liveHoldCancel() {          // 容器要没了:取消展示,别把步骤搬进新建的折叠条
  if (!liveHold) return;
  clearTimeout(liveHold.timer);
  liveHold.st._held = false;
  liveHold = null;
}
function liveHoldFlush() {           // 立刻把还"展示中"的那步收进折叠条
  if (!liveHold) return;
  const st = liveHold.st;
  clearTimeout(liveHold.timer);
  liveHold = null;
  stepsLiveDone(st);                 // _held 仍为 true → 这次直接折叠,不会再续期
}
function stepsLiveDone(st) {
  if (st._folded) return;
  if (!st._held) {
    const t0 = Number(st.dataset.t0 || 0);
    const elapsed = t0 ? Date.now() - t0 : LIVE_MIN_MS;
    if (elapsed < LIVE_MIN_MS && LIVE.body && LIVE.body.children.length === 1) {
      st._held = true;
      liveHold = {
        st: st,
        timer: setTimeout(() => {
          liveHold = null;
          // 容器已被换掉(renderHistory)或这一步已被别的路径收走:什么都不做
          if (!st.isConnected || !st.classList.contains("ms-live-step")) { st._held = false; return; }
          stepsLiveDone(st);
        }, LIVE_MIN_MS - elapsed),
      };
      return;
    }
  }
  st._held = false;
  st._folded = true;
  stepsStrip();
  if (!STEPS.body) return;
  stepsFoldStep(st);
  if (STEPS.card && LIVE.card) STEPS.card.after(LIVE.card);
  if (!LIVE.body || LIVE.body.children.length === 0) stepsLiveDrop();
  stepsPaint(); // 这一步从「正在执行」搬进折叠条,摘要里的步数与"最近一步"都要更新
}
function stepsLiveDrop() {
  if (!LIVE.card) return;
  LIVE.card.remove();
  LIVE.card = null;
  LIVE.body = null;
}
// stepsLiveFlush:任务收尾或被中断时还有步骤挂在外面(结果永远不会回来了),
// 一并搬进折叠条,别在界面底下留一张孤零零的"正在执行"卡。
function stepsLiveFlush() {
  liveHoldCancel();
  if (!LIVE.body) return;
  stepsStrip();
  if (!STEPS.body) return;
  for (const st of [].slice.call(LIVE.body.children)) { st._folded = true; st._held = false; stepsFoldStep(st); }
  stepsLiveDrop();
  stepsPaint();
}

/* ---- 运行期间唯一已知的"产出量" ----
   exec 的 stdout 是进程跑完才一次性收回来的(shell.go 里 cmd.Stdout = &buffer),
   write_file 的回执也要落盘成功才有,所以"正在执行"期间后端确实一个字都还没有。
   当时就已知的只有"这次要写多少东西"——从参数就能算,那写文件的等待至少有个可看的量。 */
function fmtBytes(n) {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10240 ? 1 : 0) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}
function byteLenOf(s) {
  return typeof TextEncoder === "function" ? new TextEncoder().encode(s).length : s.length;
}

// toolWorkHint:写类工具在发起调用那一刻就已知的写入量;其它工具返回空串。
function toolWorkHint(name, args) {
  const n = String(name || "").toLowerCase();
  if (!/write|append|edit|create|save|patch/.test(n)) return "";
  const o = toolArgsOf(args);
  if (!o) return "";
  const key = ["content", "new_text", "text"].find(k => typeof o[k] === "string" && o[k] !== "");
  if (!key) return "";
  const s = o[key];
  const verb = /append/.test(n) ? "追加 " : "写入 ";
  return verb + fmtBytes(byteLenOf(s)) + " · " + s.split("\n").length + " 行";
}

/* ---- 运行中的"看得见的内容" ----
   只有实现了 StreamTool 的工具(目前是 exec)运行期间真有字节可推;write_file 这些
   后端手里确实一个字都还没有,展开区于是全程空着,快工具还一闪就没 —— 看起来像"根本没
   展示"。参数是运行期间唯一确定的内容:写入类工具把它要写的字符直接摊开,其它工具列出
   本次参数,exec 的真输出到了再顶掉预览。预览是输入不是输出,单独用 .ms-prev 淡化。 */
const LIVE_PREV_MAX = 1800;
function liveClip(s) {
  if (s.length <= LIVE_PREV_MAX) return s;
  return s.slice(0, LIVE_PREV_MAX) + "\n… 其余 " + (s.length - LIVE_PREV_MAX) + " 字（完整内容在参数里）";
}

// toolLivePreview:运行中这一步展开区里该显示的文字。没有可显示的就返回空串。
function toolLivePreview(name, args) {
  const n = String(name || "").toLowerCase();
  const o = toolArgsOf(args);
  if (o) {
    const bodyKey = ["content", "new_text", "text", "new_string", "replacement"]
      .find(k => typeof o[k] === "string" && o[k] !== "");
    if (bodyKey) { // 写/改类:直接给要写进去的字符
      const s = o[bodyKey];
      const path = o.path || o.file || o.target || o.dest || o.source || "?";
      const verb = /append/.test(n) ? "追加" : (/edit|patch/.test(n) ? "改动" : "写入");
      return verb + " " + path + " · " + fmtBytes(byteLenOf(s)) + " · " + s.split("\n").length + " 行 (" + bodyKey + ")\n" + liveClip(s);
    }
    const lines = Object.keys(o).map(k => {
      const v = o[k];
      return k + ": " + (typeof v === "string" ? v : JSON.stringify(v));
    });
    if (lines.length) return liveClip(lines.join("\n"));
  }
  const raw = args == null ? "" : String(args).trim();
  return raw ? liveClip(raw) : "";
}

// stepsLivePreview:把预览放进这一步的展开区,并标成"输入预览"。
function stepsLivePreview(st, name, args) {
  const pre = st.querySelector(".ms-out");
  const text = toolLivePreview(name, args);
  if (!pre || !text) return false;
  pre.textContent = text;
  pre.hidden = false;
  pre.classList.add("ms-prev");
  st.dataset.preview = "1";
  return true;
}

/* ---- 模型"正在打字"的参数 ----
   write_file 的 content 要模型一个字一个字生成,几十 KB 的大纲能跑一两分钟,而 tool_start
   得等整段回复收完才发。后端把参数的当前快照推过来(tool_args,累计值不是增量),这里就在
   「正在执行」卡里先立一张"预备步骤",把已经在生成的正文显示出来;tool_start 一到就把它
   认下来当这一步,不另开卡。 */

// jsonStringField:从(可能残缺的)JSON 里抠出某个字符串字段的值,顺手把常见转义解开。
// 不解析 JSON —— 参数还在长,此刻它根本不是合法 JSON。
function jsonStringField(raw, key) {
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
function argsStreamBody(raw) {
  let best = "";
  for (const k of ["content", "new_text", "text", "new_string", "replacement"]) {
    const v = jsonStringField(raw, k);
    if (v.length > best.length) best = v;
  }
  return best;
}

// argsStreamPath:路径通常在 content 之前生成完,能提前知道要写哪个文件。
function argsStreamPath(raw) {
  const m = raw.match(/"(?:path|file|target|dest)"\s*:\s*"([^"]*)/);
  return m ? m[1].replace(/\\\\/g, "\\") : "";
}

// stepsTakePrep:这一名字的"预备步骤"(参数还在生成)有就拿来用。
function stepsTakePrep(name) {
  if (!LIVE.body || !LIVE.body.isConnected) return null;
  for (const s of [].slice.call(LIVE.body.children)) {
    if (s.dataset.name === name && s.dataset.prep === "1") return s;
  }
  return null;
}

// stepsPrepStep:找到该挂哪一步;没有就立一张预备步骤。已经在跑的同名步骤不另开卡。
function stepsPrepStep(name) {
  const prep = stepsTakePrep(name);
  if (prep) return prep;
  if (LIVE.body && LIVE.body.isConnected) {
    const kids = [].slice.call(LIVE.body.children);
    for (const s of kids) if (s.dataset.name === name) return null; // 这步已经开始跑了
    if (kids.length >= 8) return null;                              // 兜底:别攒出一堆空卡
  }
  return stepsAddTool(name, "", null, true);
}

// appendToolArgs:刷新"预备步骤"。推来的是累计快照,所以每次整体重算,天然幂等,
// 丢一个包只是少刷一次,不会像增量那样错开半截。
function appendToolArgs(name, argsSoFar) {
  if (!name || !argsSoFar || histRendering) return;
  const st = stepsPrepStep(name);
  const pre = st && st.querySelector(".ms-out");
  if (!pre) return;
  const path = argsStreamPath(argsSoFar);
  const body = argsStreamBody(argsSoFar);
  let text = body || String(argsSoFar);
  // 流式期留尾巴(看的就是"正在写哪几个字"),tool_start 之后按完整参数从头显示
  if (text.length > LIVE_OUT_TAIL) text = "…" + text.slice(-(LIVE_OUT_TAIL - 1));
  pre.hidden = false;
  pre.classList.add("ms-prev");
  st.dataset.preview = "1";
  pre.textContent = text;
  pre.scrollTop = pre.scrollHeight;
  const work = st.querySelector(".ms-work");
  if (work) {
    const lines = body ? body.split("\n").length : 0;
    work.textContent = "生成参数中 " + fmtBytes(byteLenOf(argsSoFar)) + (lines ? " · " + lines + " 行" : "");
    work.title = path ? "准备写入:" + path : "参数由模型实时生成,还没开始执行";
  }
  if (nearBottom()) scrollBottom(false, true);
}

// stepsAdoptPrep:tool_start 到了,把预备步骤转正 —— 同一张卡,不换元素。
function stepsAdoptPrep(st, name, args) {
  const a = args == null ? "" : String(args);
  st.dataset.prep = "0";
  st.open = true;
  const work = st.querySelector(".ms-work");
  if (work) { work.textContent = ""; work.title = ""; }
  if (a) {
    const old = st.querySelector(".ms-arg1");
    if (old) old.remove();
    if (work) work.insertAdjacentHTML("beforebegin", toolArgLine(name, a));
    st._args = a;                    // 折叠条里要看到参数全文
    stepsLivePreview(st, name, a);   // 参数齐了,按完整值重显示一次
  }
  stepsTag(st, "run", "运行中");
  st.dataset.soft = "1";
  st.dataset.live = "1";
  st.dataset.t0 = String(Date.now());   // 计时从真正发起执行算起,不含模型生成参数的时间
  const lim = toolTimeoutSec(args);
  if (lim) st.dataset.tmo = String(lim);
  stepsRunStart();
  return st;
}

/* ---- 工具的实时输出 ----
   后端把还在跑的工具输出切成片推过来(tool_output),这里贴到「正在执行」卡里那一步
   的展开区。只留尾部 LIVE_OUT_TAIL 字:一条 `dir /s` 能吐几 MB,而完整输出最终在工具
   结果里,这里只是让"运行中"不再是一个冻住的标签。 */
const LIVE_OUT_TAIL = 4000;
function appendToolOutput(name, chunk) {
  if (!chunk) return;
  // 已经收进折叠条(或压根没有在跑的步骤):全文会在工具结果里出现,这里直接丢
  if (!LIVE.body || !LIVE.body.isConnected) return;
  const steps = [].slice.call(LIVE.body.querySelectorAll('.ms-step[data-live="1"]'));
  if (!steps.length) return;
  const st = steps.filter(s => s.dataset.name === name).pop() || steps[steps.length - 1];
  const pre = st.querySelector(".ms-out");
  if (!pre) return;
  const stick = nearBottom();
  pre.hidden = false;
  // 真输出来了就把参数预览顶掉:预览是输入,和输出混在同一格里只会看错
  const fresh = !pre._real;
  pre._real = true;
  if (st.dataset.preview === "1") {
    st.dataset.preview = "0";
    pre.classList.remove("ms-prev");
  }
  let text = (fresh ? "" : (pre.textContent || "")) + chunk;
  if (text.length > LIVE_OUT_TAIL) text = "…" + text.slice(-(LIVE_OUT_TAIL - 1));
  pre.textContent = text;
  pre.scrollTop = pre.scrollHeight; // 输出自己往下走
  if (stick) scrollBottom(false, true);
}

function stepsAddThink(reasoning) {
  return stepsPush('<details class="m-think"><summary>💭 思考过程(' + reasoning.length + ' 字)</summary>'
    + '<div class="think-body md">' + mdToHtml(reasoning) + "</div></details>", true);
}
// 工具步骤:标题行(名字 + 一行参数 + 状态徽标)常态可见,参数全文与返回结果默认折起。
// prep=true 是"模型还在生成参数"的预备步骤(见 appendToolArgs),它不计时、等 tool_start 转正。
function stepsAddTool(name, args, result, prep) {
  mcEmit("tool");
  const a = args == null ? "" : String(args);
  // 参数流先到了 → tool_start 只是把这一步转正,复用同一个元素,别另开一张卡
  if (!prep && result == null && !histRendering) {
    const prepped = stepsTakePrep(name);
    if (prepped) return stepsAdoptPrep(prepped, name, args);
  }
  // 实时调用先在「正在执行」卡里跑,跑完才进折叠条;历史回显直接进折叠条。
  const live = result == null && !histRendering;
  const head =
    '<summary><span class="t-name">⚙ ' + esc(name) + '</span>' +
    toolArgLine(name, args) + '<span class="ms-work"></span><span class="ms-run"></span><span class="ms-tag"></span></summary>';
  // 运行中的展开区只放实时输出:参数全文此刻没有信息量(标题行已经说了这次调什么),
  // 整段塞进来会把"正在执行"卡撑高一屏。搬进折叠条时再补回参数全文。
  const html = live
    ? head + '<div class="ms-step-body"><pre class="ms-out" hidden></pre><div class="ms-res-slot"></div></div>'
    : head + '<div class="ms-step-body">' +
        (a ? '<div class="ms-args">' + esc(a.slice(0, 2000)) + (a.length > 2000 ? " …" : "") + "</div>" : "") +
        '<div class="ms-res-slot"></div></div>';
  const st = live ? stepsLivePush(html) : stepsPush(html, false, "details");
  if (live) stepsPaint(); // 折叠条已在时,「正在执行」卡里这步立刻算进步数
  st.dataset.name = name || "";
  if (prep) {
    // 参数还在生成:不计时(计时从发起执行算起),展开区交给 appendToolArgs 填
    st.dataset.prep = "1";
    st.dataset.soft = "1";
    st.open = true;
    stepsTag(st, "dim", "生成参数中");
    return st;
  }
  stepsTag(st, result == null ? "run" : "dim", result == null ? "运行中" : "待返回");
  if (live) {
    // 参数全文留给搬进折叠条时补上:存在 JS 属性上,不写进 DOM 属性
    st._args = a;
    st.open = true; // 正在跑的那步摊开展示:实时输出就在展开区里
    st.dataset.prep = "0";
    // 计时从发起调用这一刻起。历史重绘出来的步骤不是"正在进行的事",
    // 给它计时会显示出一个凭空的运行时长,所以只在 live 时记起始时间。
    st.dataset.live = "1";
    st.dataset.t0 = String(Date.now());
    const lim = toolTimeoutSec(args);
    if (lim) st.dataset.tmo = String(lim);
    const work = st.querySelector(".ms-work");
    const hint = toolWorkHint(name, args);
    // 展开区里放了预览(写类工具=要写的字符)就不再重复挂那颗徽标;放不下预览的
    // (参数解析不了)才退回"写入 xx KB · N 行"的标题行徽标。
    if (!stepsLivePreview(st, name, args) && work && hint) {
      work.textContent = hint;
      work.title = "这次要写入的量,发起调用时就已知;真正的回执要等写完";
    }
    stepsRunStart();
  }
  // "(历史)" 是占位结果:真结果(随后的 tool 消息)到达时替换掉它
  st.dataset.soft = (result == null || String(result) === "(历史)") ? "1" : "0";
  if (result != null && String(result) !== "(历史)") stepsFill(st, result);
  return st;
}
function stepsTag(st, kind, text) {
  const t = st.querySelector(".ms-tag");
  if (!t) return;
  t.className = "ms-tag " + kind;
  t.innerHTML = kind === "run" ? '<span class="pulse gray"></span>运行中' : esc(text);
}

/* ---- 运行中计时 ----
   只有一枚"运行中"圆点不够:exec 跑一条长命令(编译、拉代码、轮询)时,读者分不清
   "在慢慢正常地跑"和"已经卡死了"。所以从**发起调用那一刻**起每秒报一次已运行时长,
   并尽量从参数里认出本次的超时上限,写成"已运行 12s / 最长 300s";到了上限还没返回
   就转醒目色,提示这次可能真卡住了。计时完全在浏览器里做,是给用户看的近似值。 */
var RUN_TICK = null;
const TOOL_TIMEOUT_KEYS = ["timeout", "timeout_s", "timeout_sec", "timeout_seconds",
  "deadline", "time_limit", "max_wait", "maxruntime"];

// timeoutSecOf 把"最长等待"读成秒。超过 3600 的按毫秒填的理解(300000 → 300s):
// 本仓库 exec 的 timeout 是秒且上限 600,不可能有人正经填三小时的秒数。
function timeoutSecOf(v) {
  const n = typeof v === "number" ? v : parseFloat(String(v == null ? "" : v).trim());
  if (!isFinite(n) || n <= 0) return 0;
  return n > 3600 ? Math.round(n / 1000) : Math.round(n);
}

// toolTimeoutSec 从参数里认出本次调用的超时上限(秒),认不出返回 0。两条来源:
// 参数里的 timeout 类字段,或命令文本里自己写的 --timeout 300 / timeout 30 / sleep 45。
function toolTimeoutSec(args) {
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

function fmtRun(sec) {
  if (sec < 60) return sec + "s";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return s ? m + "m" + s + "s" : m + "m";
}

// stepsRunTick 每秒刷新所有在跑的步骤;没有在跑的就自己停掉,不留空转的定时器。
// 扫全文档而不是只扫当前折叠条:折叠条会被并进正文卡、或被下一条任务另起,
// 而并卡那一刻可能还有工具在跑,步骤还活在新位置。
function stepsRunTick() {
  const live = [].slice.call(document.querySelectorAll('.ms-step[data-live="1"]'));
  if (!live.length) { stepsRunStop(); return; }
  // 并行调用时头部报个数,一眼知道同时在跑几个
  const hd = LIVE.card && LIVE.card.querySelector(".ms-live-hd");
  if (hd) hd.innerHTML = '<span class="pulse gray"></span>正在执行' +
    (live.length > 1 ? '<span class="ms-live-n">' + live.length + ' 个并行</span>' : "");
  const now = Date.now();
  for (const st of live) {
    const el = st.querySelector(".ms-run");
    if (!el) continue;
    const sec = Math.max(0, Math.round((now - Number(st.dataset.t0 || now)) / 1000));
    const lim = Number(st.dataset.tmo || 0);
    el.textContent = "已运行 " + fmtRun(sec) + (lim ? " / 最长 " + fmtRun(lim) : "");
    el.classList.toggle("late", lim > 0 && sec >= lim);
    el.title = lim
      ? "本次调用最长 " + lim + " 秒,已经等了 " + sec + " 秒" + (sec >= lim ? " —— 超过上限还没返回,可能卡住了" : "")
      : "从发起调用开始计时(前端显示,仅供参考)";
  }
}
function stepsRunStart() {
  stepsRunTick();
  if (!RUN_TICK) RUN_TICK = setInterval(stepsRunTick, 1000);
}
function stepsRunStop() {
  if (!RUN_TICK) return;
  clearInterval(RUN_TICK);
  RUN_TICK = null;
}
// stepsRunFreeze:任务已经终止(出了回复/失败/被中断),在跑的那些步骤不会再有
// 结果回来。停掉计时,把"已运行 Xs"停在最后一次的数值上,别再往上加。
function stepsRunFreeze() {
  for (const st of [].slice.call(document.querySelectorAll('.ms-step[data-live="1"]'))) {
    st.dataset.live = "0";
  }
  stepsRunStop();
  // 任务已经没了,还挂在外面的步骤一并收进折叠条,不留空转的"正在执行"卡
  stepsLiveFlush();
}

function stepsFill(st, result) {   // 真结果写进步骤内部(折起时看不到,点开才展开)
  const s = String(result == null ? "" : result);
  // 这一步实际跑了多久:只有实时调用记过起始时间,历史步骤没有。
  const dur = st.dataset.live === "1"
    ? Math.max(0, Math.round((Date.now() - Number(st.dataset.t0 || Date.now())) / 1000)) : 0;
  st.dataset.live = "0";
  const run = st.querySelector(".ms-run");
  if (run) { run.textContent = ""; run.classList.remove("late"); }
  const work = st.querySelector(".ms-work");
  if (work) work.textContent = ""; // 回执已经出来了,"待写入多少"不再有意义
  const shown = s.slice(0, 4000);
  const cut = s.length > shown.length ? '<span class="ms-cut">… 共 ' + s.length + " 字,已截断</span>" : "";
  const slot = st.querySelector(".ms-res-slot");
  const html = '<div class="t-res">✓ ' + esc(shown) + cut + "</div>";
  if (slot) slot.innerHTML = html; else st.insertAdjacentHTML("beforeend", html);
  stepsTag(st, "ok", "✓ " + (dur >= 2 ? fmtRun(dur) + " · " : "") + s.length + " 字");
  st.dataset.soft = "0";
  stepsPaint(); // "最近一步"的摘要跟着回执一起更新
  // 跑完了才折叠进去:这一步从「正在执行」卡回到「执行过程」列表
  if (st.classList.contains("ms-live-step")) stepsLiveDone(st);
}
// 还没拿到真结果的步骤:先找「正在执行」卡里的(那正是此刻在跑的),再找折叠条里的;
// 同一处内优先按工具名配对(并行调用时结果可能乱序回来),否则按先后顺序。
function stepsSoft(name) {
  const pools = [];
  if (LIVE.body && LIVE.body.isConnected) pools.push(LIVE.body);
  if (STEPS.body && STEPS.body.isConnected) pools.push(STEPS.body);
  for (const p of pools) {
    if (!name) break;
    const hit = [].slice.call(p.querySelectorAll('.ms-step[data-soft="1"]'))
      .filter(s => s.dataset.name === name);
    if (hit.length) return hit[0];
  }
  for (const p of pools) {
    const s = p.querySelector('.ms-step[data-soft="1"]');
    if (s) return s;
  }
  return null;
}
function stepsResult(text, name) {
  const s = stepsSoft(name);
  if (!s) return false;
  stepsFill(s, text);
  return true;
}
function stepsClose() {            // 收尾:折叠条原位留着(独立卡),下一轮另起一条
  liveHoldFlush();                // 本轮过程要收尾了,"展示中"的那步先进折叠条再关条
  stepsPaint();
  STEPS.card = null; STEPS.det = null; STEPS.body = null;
}
function stepsMergeInto(card) {    // 并进"有正文"的那张卡片:放在标题行之下、正文之上
  liveHoldFlush();                // 正文来了,"展示中"的那步先归队再并条,别把它关在卡外
  if (!card || !STEPS.det) return;
  const head = card.querySelector(".m-head");
  STEPS.det.classList.add("ms-inline");
  if (head && head.parentNode === card) card.insertBefore(STEPS.det, head.nextSibling);
  else card.insertBefore(STEPS.det, card.firstChild);
  stepsPaint();
  if (STEPS.card) STEPS.card.remove();   // 独立外壳去掉,折叠条已住进卡片
  stepsClose();
}

function addAssistant(text, reasoning) {
  mcEmit("reply");
  // 没有正文 = 这一轮只是过程(思考/准备调工具)→ 收进折叠条,不平铺成卡
  if (!text) { if (reasoning) stepsAddThink(reasoning); return null; }
  let inner = '<div class="m-head"><span class="m-tag ai">Mini Code</span></div>';
  if (reasoning) {
    inner += '<details class="m-think"><summary>💭 思考过程(' + reasoning.length + ' 字)</summary><div class="think-body md">' + mdToHtml(reasoning) + "</div></details>";
  }
  inner += '<div class="m-text md">' + mdToHtml(text) + "</div>";
  // 单聊:agent 消息靠左,头像在左(首字+哈希色,每 agent 固定)
  const div = addCard("ai", inner);
  div.insertAdjacentHTML("afterbegin", agentAvatarBox("c-avatar"));
  stepsMergeInto(div.querySelector(".m-card"));
  return div;
}

function addError(text) {
  mcEmit("error");
  addCard("error", '<div class="m-text">⚠ 任务失败:' + esc(text) + "</div>");
}
function addStatus(text) {
  addCard("status", '<div class="m-text">' + esc(text) + "</div>");
}
function addToolLine(name, args, result) {
  // 工具步骤一律进「执行过程」折叠条;紧随其后的"结果"消息合并进同一步,不再另起一张卡
  if (result != null && String(result) !== "(历史)" && stepsResult(result, name)) return;
  stepsAddTool(name, args, result);
}
function markToolPendingDone(result, name) {
  if (stepsResult(result, name)) {
    if (!histRendering && nearBottom()) scrollBottom(false, true);
    return;
  }
  // 兜底:折叠条不在(旧数据/异常路径)时,退回"最后一张工具卡"的老逻辑
  const cards = messagesEl().querySelectorAll(".msg.tool .m-card");
  if (cards.length === 0) return;
  const last = cards[cards.length - 1];
  const stick = nearBottom();
  const ind = last.querySelector(".live-ind");
  if (ind) ind.remove();
  last.insertAdjacentHTML("beforeend", '<div class="t-res">✓ ' + esc(String(result).slice(0, 420)) + "</div>");
  scrollBottom(false, stick);
}

function renderHistory(messages) {
  const m = messagesEl();
  m.innerHTML = "";
  histFull = (messages || []).filter(msg => msg.role !== "system");
  // 只渲染最近 HIST_PAGE 条,更早的缓存起来,上滑接近顶部时由 loadOlder 自动补齐
  histStart = Math.max(0, histFull.length - HIST_PAGE);
  if (histFull.length === 0) { showPlaceholder(); return; }
  if (histStart > 0) {
    // 顶部隐形锚点:scroll 监听发现上滑接近顶部时自动按批补更早内容
    const box = document.createElement("div");
    box.className = "load-older";
    m.appendChild(box);
  }
  histRendering = true;
  histBatch = [];
  stepsReset();
  try {
    for (let i = histStart; i < histFull.length; i++) appendHistoryMsg(histFull[i]);
    stepsClose();   // 整段没有正文时,攒下的过程步骤独立成一张折叠卡收尾
  } finally { histRendering = false; histBatch = null; }
  scrollBottom(true); // 初始视图直接落在最新一条,不做平滑动画
}
function appendHistoryMsg(msg) {
  if (msg.role === "user") {
    // 多模态用户消息:文字取 text part(不再显示"📷 [图片]"占位),图片单独渲染
    const imgs = contentImages(msg);
    const txt = Array.isArray(msg.content)
      ? msg.content.filter(p => p.type === "text").map(p => p.text || "").join("\n")
      : contentText(msg);
    addUser(txt, imgs);
  } else if (msg.role === "assistant") {
    addAssistant(contentText(msg), msg.reasoning_content);
    for (const tc of msg.tool_calls || []) {
      addToolLine(tc.function && tc.function.name, tc.function && tc.function.arguments, "(历史)");
    }
  } else if (msg.role === "tool") {
    addToolLine(msg.name || "tool", "", contentText(msg));
  }
}
// 按批补更早消息:先追加到容器尾部,再整体移到首批已渲染卡片之前(保时序),
// 最后恢复滚动位置——视口保持用户原来看到的地方,不会跳到最上面。
// 由 scroll 监听在用户上滑接近顶部时自动触发;若恢复位置后仍接近顶部且还有
// 更早消息,后续 scroll 事件会继续补下一批,直到全部加载完。
function loadOlder() {
  const m = messagesEl();
  const box = m.querySelector(".load-older");
  if (!box || histStart === 0) return;
  const anchor = box.nextSibling; // 当前已渲染的第一条卡片,新批次要插到它前面
  const next = Math.max(0, histStart - HIST_PAGE);
  const count = histStart - next;
  const prevH = m.scrollHeight, prevTop = m.scrollTop;
  histRendering = true;
  histBatch = [];
  stepsReset();
  try {
    for (let i = next; i < histStart; i++) appendHistoryMsg(histFull[i]);
    stepsClose();
  } finally { histRendering = false; }
  // 归并后「一条消息 ≠ 一张卡」,按本批新建清单精确搬移
  const nodes = histBatch || [];
  histBatch = null;
  for (const n of nodes) m.insertBefore(n, anchor);
  histStart = next;
  if (next === 0) box.remove();
  const prev = m.style.scrollBehavior;
  m.style.scrollBehavior = "auto";
  m.scrollTop = m.scrollHeight - prevH + prevTop;
  m.style.scrollBehavior = prev;
}

