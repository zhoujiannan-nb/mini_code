
const pluginRuntime = {
  decl: null,        
  motionByEvent: {}, 
  bound: false,
};

export const MC_EVENTS = {
  send: "mc:send",       
  reply: "mc:reply",     
  error: "mc:error",     
  tool: "mc:tool",       
  session: "mc:session", 
};

export function mcEmit(name) {
  const ev = MC_EVENTS[name];
  if (!ev) return;
  try {
    document.dispatchEvent(new CustomEvent(ev, { detail: { at: Date.now() } }));
  } catch (e) {
  }
}

export function pluginReadDecl() {
  if (pluginRuntime.decl) return pluginRuntime.decl;
  const el = document.getElementById("mc-plugin-ui");
  if (!el) return (pluginRuntime.decl = { motion: [] });
  try {
    pluginRuntime.decl = JSON.parse(el.textContent || "{}");
  } catch (e) {
    console.warn("plugin ui declaration is not valid JSON", e);
    pluginRuntime.decl = { motion: [] };
  }
  if (!pluginRuntime.decl.motion) pluginRuntime.decl.motion = [];
  return pluginRuntime.decl;
}

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
    if (!anchor) return; 
    if (name === "overlay") {
      node.classList.add("mc-slot-overlay");
    }
    anchor.appendChild(node);
    node.hidden = false;
  });
}

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
        body.classList.remove("mc-motion-" + cls);
        void body.offsetWidth; 
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

export function pluginRuntimeInit() {
  pluginReadDecl();
  pluginMountSlots();
  pluginBindMotion();
}
