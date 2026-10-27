/* ---------------- 执行过程归并(steps) ----------------
   agent 一轮任务常常是:思考 → 调工具 → 思考 → 调工具 → …… → 最后一段正文。
   中间这些"只有思考、没有正文"的步骤若一条条平铺,屏幕全是工具卡(刷屏)。
   这里把它们收进一条「执行过程」折叠条:平时只有一行摘要(N 步 · M 次思考),
   点开才见每一步(参数 + 结果 + 该步思考);等真正有正文的回复出现,这条折叠条
   会被并进那张卡片的正文上方。整轮都没有正文时,折叠条独立成卡,信息不丢。
   流式期间同样用这条折叠条:它是活的,步数实时往上加,想看就点开。 */
var STEPS = { card: null, det: null, body: null, n: 0, th: 0 };

function stepsReset() {            // 消息容器被清空(renderHistory)后丢掉引用
  STEPS.card = null; STEPS.det = null; STEPS.body = null; STEPS.n = 0; STEPS.th = 0;
}
function stepsStrip() {            // 懒创建折叠条(一张独立卡,逐步往里加)
   if (STEPS.det && STEPS.det.isConnected) return STEPS.det;
   stepsReset();
   const div = addCard("steps",
     '<div class="m-card"><details class="m-steps"><summary></summary>' +
     '<div class="m-steps-wrap"><div class="m-steps-body"></div></div></details></div>');
   STEPS.card = div;
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
  STEPS.det.querySelector("summary").innerHTML =
    '<span class="ms-t">执行过程</span>' +
    '<span class="ms-n">' + STEPS.n + ' 步</span>' +
    (STEPS.th ? '<span class="ms-n">' + STEPS.th + ' 次思考</span>' : '') +
    '<span class="ms-hint">' + (STEPS.det.open ? "收起" : "展开") + '</span>';
}
function stepsPush(html, isThink, tag) {
   const body = stepsStrip().querySelector(".m-steps-body");
   const d = document.createElement(tag || "div");
   d.className = "ms-step";
   d.innerHTML = html;
   body.appendChild(d);
   STEPS.n++;
   if (isThink) STEPS.th++;
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


function stepsAddThink(reasoning) {
  return stepsPush('<details class="m-think"><summary>💭 思考过程(' + reasoning.length + ' 字)</summary>'
    + '<div class="think-body md">' + mdToHtml(reasoning) + "</div></details>", true);
}
// 工具步骤:标题行(名字 + 一行参数 + 状态徽标)常态可见,参数全文与返回结果默认折起
function stepsAddTool(name, args, result) {
  mcEmit("tool");
  const a = args == null ? "" : String(args);
  const st = stepsPush(
    '<summary><span class="t-name">⚙ ' + esc(name) + '</span>' +
    toolArgLine(name, args) + '<span class="ms-tag"></span></summary>' +
    '<div class="ms-step-body">' +
      (a ? '<div class="ms-args">' + esc(a.slice(0, 2000)) + (a.length > 2000 ? " …" : "") + "</div>" : "") +
      '<div class="ms-res-slot"></div>' +
    "</div>", false, "details");
  stepsTag(st, result == null ? "run" : "dim", result == null ? "运行中" : "待返回");
  st.dataset.name = name || "";
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
function stepsFill(st, result) {   // 真结果写进步骤内部(折起时看不到,点开才展开)
  const s = String(result == null ? "" : result);
  const shown = s.slice(0, 4000);
  const cut = s.length > shown.length ? '<span class="ms-cut">… 共 ' + s.length + " 字,已截断</span>" : "";
  const slot = st.querySelector(".ms-res-slot");
  const html = '<div class="t-res">✓ ' + esc(shown) + cut + "</div>";
  if (slot) slot.innerHTML = html; else st.insertAdjacentHTML("beforeend", html);
  stepsTag(st, "ok", "✓ " + s.length + " 字");
  st.dataset.soft = "0";
}
// 还没拿到真结果的步骤:优先按工具名配对(并行调用时结果可能乱序回来),否则按先后顺序
function stepsSoft(name) {
  if (!STEPS.body) return null;
  if (name) {
    const hit = [].slice.call(STEPS.body.querySelectorAll('.ms-step[data-soft="1"]'))
      .filter(s => s.dataset.name === name);
    if (hit.length) return hit[0];
  }
  return STEPS.body.querySelector('.ms-step[data-soft="1"]');
}
function stepsResult(text, name) {
  const s = stepsSoft(name);
  if (!s) return false;
  stepsFill(s, text);
  return true;
}
function stepsClose() {            // 收尾:折叠条原位留着(独立卡),下一轮另起一条
  stepsPaint();
  STEPS.card = null; STEPS.det = null; STEPS.body = null;
}
function stepsMergeInto(card) {    // 并进"有正文"的那张卡片:放在标题行之下、正文之上
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

