/* ---------------- 回车发送模式(点击底部提示切换) ---------------- */
// "enter"(默认)= Enter 发送, Shift+Enter 换行; "shift" = Shift+Enter 发送, Enter 换行。
// 点一下底部快捷键提示即切换, 存 localStorage(浏览器与桌面 WebView2 均持久)。
const LS_SEND_MODE_KEY = "mc-send-mode";
let sendMode = "enter";
try { if (localStorage.getItem(LS_SEND_MODE_KEY) === "shift") sendMode = "shift"; } catch (e) {}
function inputPlaceholder() {
  return (sendMode === "shift" ? "输入任务,Shift+Enter 发送,Enter 换行" : "输入任务,Enter 发送,Shift+Enter 换行")
    + ",粘贴截图,@ 插入文件夹路径";
}
function applySendMode() {
  const hint = $("#foot-hint");
  if (hint) hint.textContent = (sendMode === "shift" ? "Shift+Enter 发送 · Enter 换行" : "Enter 发送 · Shift+Enter 换行")
    + " · Ctrl+V 粘贴截图 · @ 选文件夹";
  if (!state.currentGroup) $("#input").placeholder = inputPlaceholder();
}
function toggleSendMode() {
  sendMode = sendMode === "shift" ? "enter" : "shift";
  try { localStorage.setItem(LS_SEND_MODE_KEY, sendMode); } catch (e) {}
  applySendMode();
  toast(sendMode === "shift" ? "已切换: Shift+Enter 发送, Enter 换行" : "已切换: Enter 发送, Shift+Enter 换行", "ok");
}
applySendMode();

