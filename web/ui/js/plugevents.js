/* ---------------- 插件动效事件总线 ----------------
   第三类插件的 ui.motion 把 app 事件绑到皮肤里定义的 CSS 动画。
   这里提供**唯一**的事件名表,app 在关键路径上 mcEmit 一次,
   pluginruntime.js 监听并挂动画类。

   为什么不用 DOM 自定义事件到处散着 dispatch:
   全项目只有这一份名字表,插件清单校验(Go 侧 validMotionEvents)
   和前端用的是同一批字符串,写错会被 manifest 校验挡下来。 */

const MC_EVENTS = {
  send: "mc:send",       // 用户发出消息
  reply: "mc:reply",     // 助手回复结束
  error: "mc:error",     // 任务/请求失败
  tool: "mc:tool",       // 工具步骤开始
  session: "mc:session", // 切换了当前会话
};

// mcEmit 派发一个插件可见的事件。没有插件时也安全:没有监听者就是空转。
function mcEmit(name) {
  const ev = MC_EVENTS[name];
  if (!ev) return;
  try {
    document.dispatchEvent(new CustomEvent(ev, { detail: { at: Date.now() } }));
  } catch (e) {
    // 极老的运行时没有 CustomEvent 构造器:静默跳过,动效只是不放而已。
  }
}
