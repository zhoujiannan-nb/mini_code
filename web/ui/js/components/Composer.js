// 输入区:发送/停止、权限模式、模型选择(单击)、思考强度(双击)、截图、
// @ 文件夹选择器、token/缓存/速度、发送快捷键切换。
//
// 和旧实现最大的不同:输入框内容、待发图片、下拉开合都是组件 state,
// 不再有 $("#input").value / innerHTML 这类跨模块的命令式补丁。
import { h } from "preact";
import { useState, useEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState, getState, toast } from "../lib/store.js";
import { ROUTES, api } from "../lib/api.js";
import {
  send, stop, toggleMode, toggleContextReminders, compactSession,
  pickModel, persistReasoning, thinkScale, thinkIndex, thinkingSupported,
  reasoningLabel, currentProvider,
} from "../lib/actions.js";
import { fsTokenAt, fsRecent, fsRemember } from "../lib/files.js";
import { fileToDataURL, MAX_IMAGES, MAX_IMAGE_BYTES } from "../lib/images.js";
import { fmtK, fmtK2, paintCacheHit, paintSpeed } from "../lib/format.js";
import { FsPicker } from "./FsPicker.js";

const html = htm.bind(h);

const LS_SEND_MODE_KEY = "mc-send-mode";

function readSendMode() {
  try { return localStorage.getItem(LS_SEND_MODE_KEY) === "shift" ? "shift" : "enter"; }
  catch (e) { return "enter"; }
}

function inputPlaceholder(sendMode) {
  return (sendMode === "shift" ? "输入任务,Shift+Enter 发送,Enter 换行" : "输入任务,Enter 发送,Shift+Enter 换行")
    + ",粘贴截图,@ 插入文件夹路径";
}

export function Composer() {
  const running = useStore((s) => s.running);
  const childrenActive = useStore((s) => s.childrenActive);
  const model = useStore((s) => s.model);
  const models = useStore((s) => s.models);
  const defaultModel = useStore((s) => s.defaultModel);
  const ctxWindow = useStore((s) => s.ctxWindow);
  const tokens = useStore((s) => s.tokens);
  const cacheHit = useStore((s) => s.cacheHit);
  const speed = useStore((s) => s.speed);
  const speedAccurate = useStore((s) => s.speedAccurate);
  const current = useStore((s) => s.current);
  const mode = useStore((s) => s.mode);
  const contextReminders = useStore((s) => s.contextReminders);
  const thinking = useStore((s) => s.thinking);
  const reasoningEffort = useStore((s) => s.reasoningEffort);
  const seed = useStore((s) => s.composerSeed);

  const [value, setValue] = useState("");
  const [images, setImages] = useState([]);
  const [modelMenu, setModelMenu] = useState(false);
  const [thinkPop, setThinkPop] = useState(false);
  const [sendMode, setSendMode] = useState(readSendMode);
  const [fs, setFs] = useState({ open: false, q: "", hits: [], sel: 0, indexing: false, err: "" });

  const taRef = useRef(null);
  const fileRef = useRef(null);
  const clickTimer = useRef(null);
  const fsTimer = useRef(null);
  const fsSeq = useRef(0);

  // 输入框自增高(最多 160px),旧 autoGrow 的行为。
  useEffect(() => {
    const ta = taRef.current;
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, 160) + "px";
  }, [value]);

  // 「造一个插件」把提示词塞进输入框:由 store 的 composerSeed 驱动,不直接改 DOM。
  useEffect(() => {
    if (!seed || !taRef.current) return;
    setValue(seed.text || "");
    taRef.current.focus();
    const n = (seed.text || "").length;
    taRef.current.setSelectionRange(n, n);
  }, [seed]);

  // 点模型按钮/菜单之外时收起下拉和思考浮层
  useEffect(() => {
    const onDoc = (e) => {
      if (e.target.closest && e.target.closest(".model-menu-wrap")) return;
      setModelMenu(false);
      setThinkPop(false);
    };
    document.addEventListener("click", onDoc);
    return () => document.removeEventListener("click", onDoc);
  }, []);

  /* ---------------- 发送 ---------------- */
  const doSend = async () => {
    const text = value.trim();
    const imgs = images.slice();
    if ((!text && imgs.length === 0) || running) return;
    // 先清输入框(旧行为):发送失败时错误会以消息卡出现,不会把同一段话留在框里。
    setValue("");
    setImages([]);
    closeFs();
    await send(text, imgs);
  };

  const onKeyDown = (e) => {
    if (fs.open) {
      if (e.key === "ArrowDown") { e.preventDefault(); fsMove(1); return; }
      if (e.key === "ArrowUp") { e.preventDefault(); fsMove(-1); return; }
      if (e.key === "Escape") { e.preventDefault(); closeFs(); return; }
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        if (fs.hits.length) fsInsert(fs.sel);
        else { closeFs(); doSend(); } // 无命中:直接按原逻辑发送
        return;
      }
    }
    // 发送键随回车模式(点底部提示切换):默认 Enter 发送;反向模式 Shift+Enter 发送。
    const wantSend = sendMode === "shift" ? (e.key === "Enter" && e.shiftKey)
      : (e.key === "Enter" && !e.shiftKey);
    if (wantSend) { e.preventDefault(); doSend(); }
  };

  /* ---------------- 截图 ---------------- */
  const addImageFiles = async (files) => {
    const next = images.slice();
    for (const f of files) {
      if (!f || !f.type || !f.type.startsWith("image/")) continue;
      if (next.length >= MAX_IMAGES) { toast("最多 " + MAX_IMAGES + " 张截图", "err"); break; }
      try {
        const dataUrl = await fileToDataURL(f);
        if (dataUrl.length > MAX_IMAGE_BYTES) { toast("图片过大,已跳过", "err"); continue; }
        next.push(dataUrl);
      } catch (e) { toast(e.message, "err"); }
    }
    setImages(next);
  };

  const onPaste = (e) => {
    const items = e.clipboardData && e.clipboardData.items;
    if (!items) return;
    const files = [];
    for (const it of items) {
      if (it.type && it.type.startsWith("image/")) {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (files.length) { e.preventDefault(); addImageFiles(files); }
  };

  /* ---------------- @ 文件夹选择器 ---------------- */
  const openFs = (query) => {
    setFs((s) => ({ ...s, open: true, sel: 0, q: query, err: "" }));
    clearTimeout(fsTimer.current);
    fsTimer.current = setTimeout(() => fsDoFetch(query), 150);
  };
  const closeFs = () => {
    clearTimeout(fsTimer.current);
    fsSeq.current++;
    setFs((s) => (s.open ? { ...s, open: false } : s));
  };
  const fsCheckInput = () => {
    const ta = taRef.current;
    if (!ta) return;
    const tok = fsTokenAt(ta.value, ta.selectionStart);
    if (tok) openFs(tok.query);
    else if (fs.open) closeFs();
  };
  const fsDoFetch = async (query) => {
    const seq = ++fsSeq.current;
    let data;
    try {
      data = await api(ROUTES.fsSearch(query, 20));
    } catch (e) {
      if (seq === fsSeq.current) setFs((s) => ({ ...s, open: true, err: "搜索失败:" + e.message, hits: [] }));
      return;
    }
    if (seq !== fsSeq.current) return;
    let hits = data.dirs || [];
    if (query === "") {
      // 最近使用过的路径置顶
      const rec = fsRecent().filter((p) => !hits.some((h) => h.path === p));
      hits = rec.map((p) => ({ path: p, name: p.split(/[\\/]/).filter(Boolean).pop() || p, parent: "" })).concat(hits);
    }
    setFs((s) => ({ ...s, open: true, hits, sel: 0, indexing: !!data.indexing, err: "" }));
    if (data.indexing) {
      // 目录索引还没建完:1.5s 后自动重查一次
      fsTimer.current = setTimeout(() => { if (fsSeq.current === seq) fsDoFetch(query); }, 1500);
    }
  };
  const fsMove = (d) => {
    setFs((s) => {
      if (!s.hits.length) return s;
      const sel = (s.sel + d + s.hits.length) % s.hits.length;
      return { ...s, sel };
    });
  };
  // 把命中的完整路径替换掉输入框里的 "@查询词"
  const fsInsert = (i) => {
    const h = fs.hits[i];
    const ta = taRef.current;
    if (!h || !ta) return;
    const caret = ta.selectionStart;
    const tok = fsTokenAt(ta.value, caret);
    const start = tok ? tok.start : caret;
    const before = ta.value.slice(0, start);
    const after = ta.value.slice(caret);
    const nextValue = before + h.path + " " + after;
    setValue(nextValue);
    const pos = (before + h.path + " ").length;
    requestAnimationFrame(() => {
      const el = taRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(pos, pos);
    });
    fsRemember(h.path);
    closeFs();
  };

  const toggleSendMode = () => {
    const next = sendMode === "shift" ? "enter" : "shift";
    setSendMode(next);
    try { localStorage.setItem(LS_SEND_MODE_KEY, next); } catch (e) {}
    toast(next === "shift" ? "已切换: Shift+Enter 发送, Enter 换行" : "已切换: Enter 发送, Shift+Enter 换行", "ok");
  };

  /* ---------------- 模型 / 思考 ---------------- */
  const modelName = (models.find((m) => m.name === model) || {}).name || model || "默认";
  const cap = ctxWindow || 0;
  const tokenText = !current
    ? "Token 0K / " + fmtK(cap)
    : "Token " + fmtK2(tokens) + " / " + (cap ? fmtK2(cap) : "—") +
      (cap ? " · " + Math.min(999, Math.round((tokens || 0) * 100 / cap)) + "%" : "");
  const tokenWarn = cap > 0 && (tokens || 0) / cap >= 0.8;

  // 双击模型按钮打开思考强度浮层(单击延迟 230ms 才切换模型下拉)。
  const openThinkingPop = () => {
    setThinkPop(true);
    setModelMenu(false);
  };

  const cacheInfo = paintCacheHit(cacheHit && cacheHit.prompt, cacheHit && cacheHit.cached, cacheHit && cacheHit.reported);
  const cacheText = cacheInfo.text;
  const cacheClass = cacheInfo.className;

  const speedText = paintSpeed(speed, speedAccurate);

  const scale = thinkScale();
  const idx = thinkIndex();
  const max = scale ? scale.length - 1 : 0;
  const pct = max > 0 ? Math.round(idx / max * 100) : 0;
  const provider = currentProvider();

  const onModelClick = () => {
    if (clickTimer.current) {
      clearTimeout(clickTimer.current);
      clickTimer.current = null;
      openThinkingPop();
      return;
    }
    clickTimer.current = setTimeout(() => {
      clickTimer.current = null;
      setModelMenu((v) => !v);
      setThinkPop(false);
    }, 230);
  };

  const onEffortInput = (e) => {
    if (!scale) return;
    let i = parseInt(e.target.value, 10);
    if (isNaN(i) || i < 0) i = 0;
    if (i >= scale.length) i = scale.length - 1;
    setState({ thinking: scale[i].thinking, reasoningEffort: scale[i].effort });
  };

  const modelTitle = (current
    ? "当前会话模型:" + modelName + "(切换后下次任务生效)"
    : "新会话使用的模型(下一条消息生效)") + " · 单击切换模型,双击调整思考强度";

  return html`<div class="composer">
    <${FsPicker} open=${fs.open} q=${fs.q} hits=${fs.hits} sel=${fs.sel}
      indexing=${fs.indexing} err=${fs.err} onPick=${fsInsert}
      onHover=${(i) => setFs((s) => ({ ...s, sel: i }))} />
    <div class="composer-box">
      <div class="img-previews" id="img-previews" hidden=${images.length === 0}>
        ${images.map((u, i) => html`<div class="img-prev" key=${i}>
          <img src=${u} alt="截图" />
          <button class="rm" title="移除" onClick=${() => setImages(images.filter((_, k) => k !== i))}>×</button>
        </div>`)}
      </div>
      <textarea id="input" rows="1" ref=${taRef} value=${value}
        placeholder=${inputPlaceholder(sendMode)}
        onInput=${(e) => { setValue(e.target.value); fsCheckInput(); }}
        onKeyDown=${onKeyDown}
        onPaste=${onPaste}
        onBlur=${closeFs}></textarea>
      <div class="composer-row">
        <button class="plus-btn" id="btn-plus" title="添加图片(也可直接 Ctrl+V 粘贴)"
          onClick=${() => fileRef.current && fileRef.current.click()}>
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M8 2.5v11M2.5 8h11" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
        </button>
        <span class="row-flex"></span>
        <div class="model-menu-wrap">
          <button class="model-inline-btn" id="btn-model" title=${modelTitle} onClick=${onModelClick}>
            <span id="model-name">${modelName}</span>
            <span id="think-badge" class="think-badge" hidden=${!thinkingSupported()}>${reasoningLabel()}</span>
            <span class="chev">⌄</span>
          </button>
          <div class=${"model-menu" + (modelMenu ? " open" : "")} id="model-menu">
            ${modelMenu ? (models.length
              ? models.map((m) => html`<div class=${"mm-item" + (m.name === model ? " cur" : "")} data-name=${m.name}
                  key=${m.name} onClick=${() => { setModelMenu(false); pickModel(m.name); }}>
                  ${m.name}<span class="mm-sub">${m.name === defaultModel ? "默认" : fmtK(m.context_window)}</span>
                </div>`)
              : html`<div class="mm-item" style="cursor:default;color:var(--text3)">未配置模型(设置里添加)</div>`) : null}
          </div>
          <div class=${"think-pop" + (thinkPop ? " open" : "")} id="think-pop">
            <div class="tp-head">思考强度 · <b id="tp-model">${modelName}${provider ? " · " + provider : ""}</b></div>
            <div id="tp-body" style=${scale ? "" : "display:none"}>
              <input type="range" id="tp-effort" min="0" max=${max} step="1" value=${idx}
                style=${{ "--tp-pct": pct + "%" }} onInput=${onEffortInput} onChange=${persistReasoning} />
              <div class="tp-scale" id="tp-scale">
                ${scale ? scale.map((s) => html`<span key=${s.label}>${s.label}</span>`) : null}
              </div>
            </div>
            <div class="tp-note" id="tp-note">${scale
              ? (current ? "调整后从下次任务生效。" : "新会话将按此思考强度创建。")
              : "当前模型(" + (provider || "未知 provider") + ")不支持思考强度调整,仅 deepseek / sglang / vllm 支持。"}</div>
          </div>
        </div>
        <button class="send-btn" id="btn-send" style=${running ? "display:none" : ""} onClick=${doSend}>发送</button>
        <button class="stop-btn" id="btn-stop" style=${(running || childrenActive) ? "display:inline-block" : "display:none"}
          onClick=${stop}>停止</button>
      </div>
      <input type="file" id="file-shot" accept="image/*" multiple hidden ref=${fileRef}
        onChange=${(e) => { addImageFiles(Array.from(e.target.files || [])); e.target.value = ""; }} />
    </div>
    <div class="composer-foot">
      <span class=${"token-usage" + (tokenWarn ? " warn" : "")} id="token-usage">${tokenText}</span>
      <span class=${cacheClass} id="cache-hit" title="上一轮 LLM 调用的 KV 前缀缓存命中(cached / prompt tokens)。n/a = 该部署未上报缓存命中(如 SGLang 未开 --enable-cache-report);0% = 已上报但本轮无命中。">${cacheText}</span>
      <span class="token-usage" id="output-speed" title="输出速度(token/秒)。生成中为实时估算(≈);任务停止后保留最后一次的展示值(最后一次 LLM 调用的实测均值,已存入数据库)">${speedText}</span>
      <div class="u-flex u-gap10">
        <label class=${"ctx-toggle" + (contextReminders ? "" : " off")} id="ctx-toggle" title="上下文占用达窗口 80%/85% 时提醒模型主动压缩;90% 强制兜底压缩(提醒按 context window 计算,并以模型输入上限兜底)">
          <input type="checkbox" id="ctx-reminders" checked=${contextReminders}
            onChange=${(e) => toggleContextReminders(e.target.checked)} /> 上下文提醒
        </label>
        <button class=${"mode-btn" + (mode === "cautious" ? " cautious" : "")} id="btn-mode" title=${current
          ? "点击切换权限模式:完整=直接执行;谨慎=危险操作(删文件/危险命令)需审批,立即生效"
          : "新会话的权限模式:完整=直接执行;谨慎=危险操作需审批"}
          onClick=${toggleMode}>${(mode === "cautious" ? "🛡 谨慎模式" : "⚡ 完整模式") + (current ? "" : "(新会话)")}</button>
        <button class="compact-btn" id="btn-compact" title="手动压缩当前会话上下文(立即调用模型总结旧对话)"
          disabled=${!current || running} onClick=${compactSession}>⚡ 压缩</button>
        <span class="foot-hint" id="foot-hint" title="点击切换发送快捷键:Enter 发送 ↔ Shift+Enter 发送"
          onClick=${toggleSendMode}>${sendMode === "shift" ? "Shift+Enter 发送 · Enter 换行" : "Enter 发送 · Shift+Enter 换行"} · Ctrl+V 粘贴截图 · @ 选文件夹</span>
      </div>
    </div>
  </div>`;
}
