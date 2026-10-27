/* ---------------- 消息区 ---------------- */
function messagesEl() { return $("#messages"); }
/* 智能滚动:用户手动上滚优先于自动输出——只有用户当前"在底部附近"才跟随新内容,
   上滚回看历史时不抢视角(切换会话/初始视图时强制回底)。
   force = 忽略检查强制跳转(切会话/初始视图);
   stick = 追加内容"前"是否在底部的快照(追加后内容高度已变,再测不准)。 */
function nearBottom(threshold) {
  const m = messagesEl();
  return m.scrollHeight - m.scrollTop - m.clientHeight < (threshold == null ? 90 : threshold);
}
function scrollBottom(force, stick) {
  const m = messagesEl();
  if (!force && !(stick === undefined ? nearBottom() : stick)) return;
  if (force) {
    // 瞬时跳转:旧的 scroll-behavior:smooth 下打开会话会从顶部"动画"一路拉到底
    const prev = m.style.scrollBehavior;
    m.style.scrollBehavior = "auto";
    m.scrollTop = m.scrollHeight;
    m.style.scrollBehavior = prev;
  } else {
    m.scrollTop = m.scrollHeight;
  }
}
/* 历史懒加载:打开会话只渲染最近 HIST_PAGE 条(避免全量渲染卡顿),
   更早的缓存到 histFull,用户上滑接近顶部时由 loadOlder 自动按批补齐。 */
const HIST_PAGE = 10;
let histFull = [];        // 当前会话完整消息列表(已滤掉 system)
let histStart = 0;        // 已渲染第一条在 histFull 中的下标
let histRendering = false; // 批量渲染历史期间,addCard 不触发智能滚动
var histBatch = null; // 批量渲染期间新建的卡片清单(loadOlder 用它精确搬移)
function hidePlaceholder() {
  const p = $("#placeholder");
  if (p) p.style.display = "none";
}
function showPlaceholder() {
  const m = messagesEl();
  if (m.children.length === 0) {
    m.innerHTML = '<div class="placeholder" id="placeholder"><div class="big">💬</div><p>发送消息开始对话,新会话将自动创建</p></div>';
  }
}
function addCard(cls, inner) {
  hidePlaceholder();
  const stick = !histRendering && nearBottom(); // 追加内容前快照(追加后高度已变)
  const m = messagesEl();
  const div = document.createElement("div");
  div.className = "msg " + cls;
  div.innerHTML = "<div class=\"m-card\">" + inner + "</div>";
  m.appendChild(div);
  if (histRendering && histBatch) histBatch.push(div);
  if (stick) scrollBottom(false, true);
  return div;
}
function addUser(text, images) {
  let inner = '<div class="m-head"><span class="m-tag user">你</span></div>';
  if (text) inner += '<div class="m-text">' + esc(text) + "</div>";
  if (images && images.length) {
    inner += '<div class="msg-imgs">' +
      images.map(u => '<img src="' + u + '" alt="截图" title="点击放大">').join("") +
      "</div>";
  }
  // 单聊:用户消息靠右,头像在右(与群聊同一套头像尺寸)
  const div = addCard("user", inner);
  div.insertAdjacentHTML("afterbegin", userAvatarBox("c-avatar"));
}
// 点击聊天里的截图放大查看(简易全屏遮罩,再点关闭)
document.addEventListener("click", (e) => {
  const img = e.target.closest(".msg-imgs img");
  if (!img) return;
  const ov = document.createElement("div");
  ov.style.cssText = "position:fixed;inset:0;background:rgba(15,23,42,.82);z-index:99;display:flex;align-items:center;justify-content:center;cursor:zoom-out;padding:32px;";
  ov.innerHTML = '<img style="max-width:100%;max-height:100%;border-radius:8px;" alt="截图">';
  ov.querySelector("img").src = img.src;
  ov.addEventListener("click", () => ov.remove());
  document.body.appendChild(ov);
});
