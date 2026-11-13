/* 开机动画播放器。
   旧版是 index.html <head> 里的 212 行内联 IIFE;重构后 index.html 不再有内联脚本,
   这块原样搬成独立模块(逻辑不动,只把返回值收进一个具名工厂并在模块加载时执行)。

   它必须跑得尽可能早:main.js 的第一条 import 就是它,这样主题的
   soft-handoff 帧不会等到应用真正渲染才开始。它读 #mc-boot-cfg
   (web/bootanim.go 注入的 JSON),在配置关闭时立即收场。

   交还节奏由主题决定:主题发 handoff 就开始淡出(它继续画到最后一帧),
   发 done 或被 maxMs/就绪看门狗兜住就强制结束 —— 主题再怎么坏也不会把界面锁死。 */

function createBootPlayer() {
  var cfg = null;
  try { cfg = JSON.parse((document.getElementById("mc-boot-cfg") || {}).textContent || "null"); } catch (e) {}
  var SKEY = "mc-boot-session", DKEY = "mc-boot-day";
  var el = null, frame = null, skipBtn = null, timer = null, readyTimer = null;
  var finished = !cfg || !cfg.enabled;

  function today() {
    var d = new Date();
    return d.getFullYear() + "-" + (d.getMonth() + 1) + "-" + d.getDate();
  }
  function stamp(k) {
    try { return (k === SKEY ? sessionStorage : localStorage).getItem(k); } catch (e) { return null; }
  }
  function mark(k) {
    try { (k === SKEY ? sessionStorage : localStorage).setItem(k, k === SKEY ? "1" : today()); } catch (e) {}
  }
  // 播放策略:服务端配置说了算(每次开页都会重新注入,所以"每次"就是每次)。
  function shouldPlay() {
    if (!cfg || !cfg.enabled || !cfg.entry) return false;
    if (window.innerWidth < (cfg.minViewport || 320) || window.innerHeight < 300) return false;
    try {
      if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return false;
    } catch (e) {}
    var mode = cfg.mode || "session";
    if (mode === "always") return true;
    if (mode === "daily") return stamp(DKEY) !== today();
    return stamp(SKEY) !== "1";
  }
  function entryURL() {
    // 宿主说了算的参数放前面,主题自己的 options 只补缺:同名键不重复拼,
    // 免得 URLSearchParams.get(取第一个)读到两份值。
    var q = "?interactive=" + (cfg.interactive ? 1 : 0) + "&mute=" + (cfg.mute ? 1 : 0);
    if (cfg.name) q += "&name=" + encodeURIComponent(cfg.name);
    var seen = { interactive: 1, mute: 1, name: 1 };
    var o = cfg.options || {};
    Object.keys(o).sort().forEach(function (k) {
      if (seen[k]) return;
      q += "&" + encodeURIComponent(k) + "=" + encodeURIComponent(o[k]);
    });
    return cfg.entry + q;
  }
  function clear() {
    if (timer) { clearTimeout(timer); timer = null; }
    if (readyTimer) { clearTimeout(readyTimer); readyTimer = null; }
    if (el && el.parentNode) el.parentNode.removeChild(el);
    if (skipBtn && skipBtn.parentNode) skipBtn.parentNode.removeChild(skipBtn);
    el = frame = skipBtn = null;
    var c = document.documentElement.className.replace(/\bmc-boot(-out)?\b/g, "").replace(/\s+/g, " ");
    document.documentElement.className = c.replace(/^\s+|\s+$/g, "");
  }
  // fade=true 走淡出(主题已经交还画面),false 立即撤掉(跳过/超时)。
  function finish(fade) {
    if (finished) return;
    finished = true;
    if (readyTimer) { clearTimeout(readyTimer); readyTimer = null; }
    if (timer) { clearTimeout(timer); timer = null; }
    if (!fade || !el) { clear(); return; }
    try { document.documentElement.classList.add("mc-boot-out"); } catch (e) {}
    if (el) el.classList.add("out");
    setTimeout(clear, 800);
  }
  function onMessage(e) {
    if (!frame || !e || e.source !== frame.contentWindow) return;
    var d = e.data;
    // 主题的通信协议(见插件仓库 docs/theme-contract.md):
    // parent.postMessage({mc:"boot-anim", type:"ready|step|handoff|done", id, ...}, "*")
    if (!d || typeof d !== "object" || d.mc !== "boot-anim") return;
    if (d.type === "ready") {
      if (readyTimer) { clearTimeout(readyTimer); readyTimer = null; }
      if (skipBtn) skipBtn.classList.add("show");
    } else if (d.type === "handoff" || d.type === "done") {
      // handoff = 主题刚开始淡出(此刻就叠化,别等它淡完);done = 收尾
      finish(true);
    }
  }
  function start(force) {
    if (!cfg || !cfg.entry || el) return false;
    if (!force && !shouldPlay()) return false;
    finished = false;
    if (!force) { mark(SKEY); mark(DKEY); }
    var cls = document.documentElement.className;
    document.documentElement.className = (cls ? cls + " " : "") + "mc-boot";
    el = document.createElement("div");
    el.id = "mc-boot";
    frame = document.createElement("iframe");
    frame.setAttribute("src", entryURL());
    // 只给 allow-scripts:主题读不到本地存储、拿不到 cookie、也开不了新窗口。
    frame.setAttribute("sandbox", "allow-scripts");
    // 开机动画没有"用户手势",不给这条带语音的主题就出不了声。主题自己
    // 也必须 catch 播放失败(播放策略随时可能反悔)。
    frame.setAttribute("allow", "autoplay");
    frame.setAttribute("title", "开机动画");
    frame.setAttribute("aria-hidden", "true");
    el.appendChild(frame);
    skipBtn = document.createElement("button");
    skipBtn.id = "mc-boot-skip";
    skipBtn.type = "button";
    skipBtn.textContent = "跳过 Esc";
    skipBtn.onclick = function () { finish(false); };
    window.addEventListener("message", onMessage);
    // 主题没能在 3 秒内报 ready(资源缺失、CSP 拦了、目录被删了)就别把界面挡住。
    readyTimer = setTimeout(function () { finish(false); }, 3000);
    var max = cfg.maxMs || 15000;
    if (max < 1000) max = 1000;
    timer = setTimeout(function () { finish(true); }, max + 400);
    if (document.body) {
      document.body.appendChild(el);
      document.body.appendChild(skipBtn);
    } else {
      document.addEventListener("DOMContentLoaded", function () {
        if (finished) return;
        document.body.appendChild(el);
        document.body.appendChild(skipBtn);
      });
    }
    return true;
  }
  window.addEventListener("keydown", function (e) {
    if (!finished && el && (e.key === "Escape" || e.key === "Esc")) {
      finish(false);
      // 别让"跳过动画"顺手把底下的配置弹窗也关掉
      e.preventDefault();
      e.stopPropagation();
    }
  }, true);
  // 点击空白处也能跳过(主题自己声明 interactive 时不抢)
  window.addEventListener("pointerdown", function (e) {
    if (finished || !el) return;
    if (cfg && cfg.interactive) return;
    if (e.target === el || e.target === frame) finish(false);
  }, true);

  var api = {
    get cfg() { return cfg; },
    get playing() { return !!el && !finished; },
    skip: function () { finish(false); },
    // 设置页的「预览一次」:无视播放策略和开关立刻播一遍,也不写策略标记。
    // over 覆盖本次要播的主题与参数(设置里改了还没保存也能所见即所得)。
    play: function (over) {
      finish(false);
      if (el) clear();
      if (!cfg) cfg = {};
      if (over) {
        for (var k in over) {
          if (Object.prototype.hasOwnProperty.call(over, k)) cfg[k] = over[k];
        }
      }
      if (!cfg.entry) return false;
      cfg.enabled = true;
      return start(true);
    }
  };
  if (cfg && cfg.enabled) start(false);
  return api;
}

// 模块加载即执行:main.js 的第一条 import 就是本文件(index.html 里不再有内联脚本)。
export const bootPlayer = createBootPlayer();
window.__mcBoot = bootPlayer;
export default bootPlayer;
