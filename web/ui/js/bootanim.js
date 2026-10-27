/* ---------------- 开机动画(设置侧) ----------------
   开机动画是一个「外观插件」多声明一个 `boot` 字段(见 plugin/boot.go 与
   web/bootanim.go)。所以设置页里它不再有自己的表单:装、选、开、调全在
   插件中心那张卡的详情里,外观页只留一句状态 + 一个跳转。

   这里剩下的是三件纯数据的事:
     1. bootLoadLocal()  读"装了哪些、当前是哪个、当前怎么播"(GET /api/bootanim);
     2. bootPick(id)     把某个动画设为当前(CONFIG → 插件详情里的那一下);
     3. bootPlay/Preview 立刻播一遍,不用等下次启动。

   注意 play 设置(启用/时机/静默)走 PUT /api/bootanim 的**局部更新**接口,
   而不是 PUT /api/config —— 后者要求整份配置(含模型与 API key),在插件详情里
   提交它既不必要也不安全。 */

const bootState = { dir: "", legacyDir: "", themes: [], picked: "", play: null };

// bootPlaySettings 返回当前播放设置(服务端为准,拿不到就用保守默认值)。
function bootPlaySettings() {
  const p = bootState.play || {};
  return {
    enabled: !!p.enabled,
    mode: p.mode || "session",
    mute: !!p.mute,
  };
}

// bootApplyPlay 清空播放设置并让所有相关视图重画。
function bootApplyPlay(play) {
  bootState.play = play || null;
  if (typeof renderPluginCentre === "function") renderPluginCentre();
  if (typeof paintBootSummary === "function") paintBootSummary();
}

// bootSavePlay 局部保存播放设置:只发这几个开关,不动模型配置。
async function bootSavePlay(patch, msg) {
  try {
    const r = await api("/api/bootanim", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch),
    });
    if (r && r.play) bootState.play = r.play;
    if (msg) toast(msg, "ok");
  } catch (e) {
    toast("保存失败:" + e.message, "err");
  }
  await bootLoadLocal();
}

// bootLoadLocal 读"装了什么"(即启用的 boot 插件)+ 当前播放设置。
// 插件中心的行、插件详情、外观页的摘要共用这一份数据,读一次就够。
async function bootLoadLocal(msg) {
  try {
    const r = await api("/api/bootanim");
    bootState.dir = r.dir || "";
    bootState.legacyDir = r.legacy_dir || "";
    bootState.themes = r.themes || [];
    bootState.play = r.play || null;
    // 选中的动画被删/被停用了:回落到第一个还能播的,别留一个空选择。
    const play = bootPlaySettings();
    if (play.theme !== undefined) bootState.picked = play.theme || bootState.picked;
    if (bootState.picked && !bootState.themes.some((t) => t.id === bootState.picked && !t.error)) {
      bootState.picked = "";
    }
    if (!bootState.picked) {
      const first = bootState.themes.find((t) => !t.error);
      if (first) bootState.picked = first.id;
    }
    paintBootSummary();
    if (typeof renderPluginCentre === "function") renderPluginCentre();
    if (msg) toast(msg, "ok");
  } catch (e) {
    bootState.themes = [];
    bootState.play = null;
    paintBootSummary();
    toast("读取开机动画失败:" + e.message, "err");
  }
}

// bootPick 把某个 boot 插件设成当前播放的开机动画。
// 这是"选动画"的唯一入口:插件详情里的那一下点击,等于写配置 + 记住选择。
async function bootPick(id) {
  bootState.picked = id;
  await bootSavePlay({ theme: id, enabled: true }, "已设为开机动画");
}

// 详情里的"启用"开关:关掉它等于"别播",动画本身还留着。
async function bootSetEnabled(on) {
  await bootSavePlay({ enabled: !!on }, on ? "已启用" : "已关闭");
}

// 播放时机:每个会话一次 / 每次都播 / 每天一次。
async function bootSetMode(mode) {
  await bootSavePlay({ mode: mode }, "播放时机已更新");
}

// 静默:勾上以后主题连音频文件都不会请求。
async function bootSetMute(on) {
  await bootSavePlay({ mute: !!on }, on ? "已静音" : "已开启声音");
}

// bootPlay 立刻播一遍(不做播放策略判断)。
function bootPlay(t) {
  if (!window.__mcBoot || !t || t.error) return;
  const play = bootPlaySettings();
  const ok = window.__mcBoot.play({
    entry: "/bootanim/" + t.id + "/" + (t.entry || "index.html"),
    mute: play.mute,
    interactive: false,
    durationMs: t.durationMs,
    maxMs: t.maxMs,
    minViewport: t.minViewport,
  });
  if (!ok) toast("这个动画播不了(入口文件缺失)", "err");
}
function bootPreview(id) {
  bootPlay(bootState.themes.find((t) => t.id === id));
}

// initBootSettings 不再绑表单(外观页只剩摘要),保留这个入口是因为
// settings.js 会调用它;真正的事件绑定在插件详情里。
function initBootSettings() {
  if (typeof paintBootSummary === "function") paintBootSummary();
}
