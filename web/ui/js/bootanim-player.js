
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
    if (!d || typeof d !== "object" || d.mc !== "boot-anim") return;
    if (d.type === "ready") {
      if (readyTimer) { clearTimeout(readyTimer); readyTimer = null; }
      if (skipBtn) skipBtn.classList.add("show");
    } else if (d.type === "handoff" || d.type === "done") {
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
    frame.setAttribute("sandbox", "allow-scripts");
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
      e.preventDefault();
      e.stopPropagation();
    }
  }, true);
  window.addEventListener("pointerdown", function (e) {
    if (finished || !el) return;
    if (cfg && cfg.interactive) return;
    if (e.target === el || e.target === frame) finish(false);
  }, true);

  var api = {
    get cfg() { return cfg; },
    get playing() { return !!el && !finished; },
    skip: function () { finish(false); },
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

export const bootPlayer = createBootPlayer();
window.__mcBoot = bootPlayer;
export default bootPlayer;
