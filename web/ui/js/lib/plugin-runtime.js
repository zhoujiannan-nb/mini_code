/* ---------------- 插件运行时:slot / 动效 ----------------
   第三类插件(皮肤、壁纸、动画)是**纯数据**进来的:服务端在 index.html 里注入
     - <link rel=stylesheet> 皮肤
     - .mc-slot 容器(里面是 sandbox iframe)
     - <script id="mc-plugin-ui" type="application/json"> 动效声明
   这里只做两件事:把 slot 摆到正确位置、把 app 事件映射到动画类。
   没有任何插件 JavaScript 被执行 —— 皮肤也不能。

   桌面宠物**不在**这里:网页画不到自己的窗口外面。 */

const pluginRuntime = {
  decl: null,        // {motion:[]}
  motionByEvent: {}, // event -> [class]
  bound: false,
};

/* ---------------- 插件动效事件总线 ---------------- */
// 全项目只有这一份名字表:app 在关键路径上 mcEmit 一次,下面的 pluginBindMotion
// 监听并挂动画类。插件清单校验(Go 侧 validMotionEvents)用的是同一批字符串。
export const MC_EVENTS = {
  send: "mc:send",       // 用户发出消息
  reply: "mc:reply",     // 助手回复结束
  error: "mc:error",     // 任务/请求失败
  tool: "mc:tool",       // 工具步骤开始
  session: "mc:session", // 切换了当前会话
};

// mcEmit 派发一个插件可见的事件。没有插件时也安全:没有监听者就是空转。
export function mcEmit(name) {
  const ev = MC_EVENTS[name];
  if (!ev) return;
  try {
    document.dispatchEvent(new CustomEvent(ev, { detail: { at: Date.now() } }));
  } catch (e) {
    // 极老的运行时没有 CustomEvent 构造器:静默跳过,动效只是不放而已。
  }
}

// pluginReadDecl 读取服务端注入的 JSON 声明。没有插件时元素不存在。
export function pluginReadDecl() {
  if (pluginRuntime.decl) return pluginRuntime.decl;
  const el = document.getElementById("mc-plugin-ui");
  if (!el) return (pluginRuntime.decl = { motion: [] });
  try {
    pluginRuntime.decl = JSON.parse(el.textContent || "{}");
  } catch (e) {
    // 插件的声明坏了不应该让整个界面挂掉:记一条日志,当没装。
    console.warn("plugin ui declaration is not valid JSON", e);
    pluginRuntime.decl = { motion: [] };
  }
  if (!pluginRuntime.decl.motion) pluginRuntime.decl.motion = [];
  return pluginRuntime.decl;
}

/* ---- slot:把每个 .mc-slot 搬到页面对应的锚点上 ----
   锚点优先用页面自己声明的 [data-mc-slot="<name>"];没有就退回内置映射。
   找不到锚点的 slot 保持隐藏,不报错 —— 换皮肤不该因为少一个锚点就让
   控制台刷错。注意不要把这些节点搬进 Preact 管理的子树里:App 的渲染会
   重建自己的 DOM,插件节点必须挂在稳定的容器/锚点上。 */
const PLUGIN_SLOT_ANCHORS = {
  "sidebar-top": () => document.querySelector("#sess-list"),
  "sidebar-bottom": () => document.querySelector(".sidebar-foot"),
  "composer-bar": () => document.querySelector(".composer"),
  "header-right": () => document.querySelector(".topbar"),
  "overlay": () => document.body,
};

export function pluginMountSlots() {
  const slots = document.querySelectorAll(".mc-slot[data-slot]");
  slots.forEach((node) => {
    const name = node.getAttribute("data-slot");
    let anchor = document.querySelector('[data-mc-slot="' + name + '"]');
    if (!anchor && PLUGIN_SLOT_ANCHORS[name]) anchor = PLUGIN_SLOT_ANCHORS[name]();
    if (!anchor) return; // 这个页面/主题没有这个位置
    if (name === "overlay") {
      // overlay 浮在所有东西上面,不能吃掉点击。
      node.classList.add("mc-slot-overlay");
    }
    anchor.appendChild(node);
    node.hidden = false;
  });
}

/* ---- 动效绑定:把 app 事件映射到皮肤里定义的 CSS 动画 ---- */
export function pluginBindMotion() {
  pluginReadDecl();
  pluginRuntime.motionByEvent = {};
  (pluginRuntime.decl.motion || []).forEach((m) => {
    if (!m.event || !m.class) return;
    (pluginRuntime.motionByEvent[m.event] = pluginRuntime.motionByEvent[m.event] || []).push(m.class);
  });
  if (pluginRuntime.bound) return;
  pluginRuntime.bound = true;
  const body = document.body;
  Object.keys(pluginRuntime.motionByEvent).forEach((ev) => {
    if (!MC_EVENTS[ev]) return;
    document.addEventListener(MC_EVENTS[ev], () => {
      (pluginRuntime.motionByEvent[ev] || []).forEach((cls) => {
        // 重放:先移除再加回来,并在 animationend 后清掉,避免类名常驻。
        body.classList.remove("mc-motion-" + cls);
        void body.offsetWidth; // 强制 reflow,让同名动画能再次播放
        body.classList.add("mc-motion-" + cls);
        const done = () => {
          body.classList.remove("mc-motion-" + cls);
          body.removeEventListener("animationend", done);
        };
        body.addEventListener("animationend", done);
      });
    });
  });
}

// pluginRuntimeInit 在页面启动时调一次。
export function pluginRuntimeInit() {
  pluginReadDecl();
  pluginMountSlots();
  pluginBindMotion();
}
