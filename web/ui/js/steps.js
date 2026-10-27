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
    '<div class="m-card"><details class="m-steps"><summary></summary><div class="m-steps-body"></div></details></div>');
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
  return d;
}
function stepsAddThink(reasoning) {
  return stepsPush('<details class="m-think"><summary>💭 思考过程(' + reasoning.length + ' 字)</summary>'
    + '<div class="think-body md">' + mdToHtml(reasoning) + "</div></details>", true);
}
// 工具步骤:标题行(名字 + 一行参数 + 状态徽标)常态可见,参数全文与返回结果默认折起
function stepsAddTool(name, args, result) {
  const a = args == null ? "" : String(args);
  const st = stepsPush(
    '<summary><span class="t-name">⚙ ' + esc(name) + '</span>' +
    '<span class="ms-arg1">' + esc(a) + '</span><span class="ms-tag"></span></summary>' +
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

