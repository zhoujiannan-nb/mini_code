// 输入区:发送/停止、模型选择(单击)、思考强度(双击)、截图、@ 文件夹选择器。
// 底栏(v4,学 dsh 三件套):「N 轮 · M 步 · X tok/s」点击弹会话统计浮层;
// 「总 tok · 缓存命中%」点击开上下文管理抽屉;右侧百分比小仪表(占用)。
// 手动压缩按钮与 tips 行已移除,发送快捷键/提示语在设置里改。
//
// 和旧实现最大的不同:输入框内容、待发图片、下拉开合都是组件 state,
// 不再有 $("#input").value / innerHTML 这类跨模块的命令式补丁。
import { h } from "preact";
import { useState, useEffect, useRef } from "preact/hooks";
import htm from "htm";

import { useStore, setState, getState, toast, toggleCtxPanel } from "../lib/store.js";
import { ROUTES, api } from "../lib/api.js";
import {
  send, sendSteer, stop, loadContext,
  pickModel, persistReasoning, thinkScale, thinkIndex, thinkingSupported,
  reasoningLabel, currentProvider,
} from "../lib/actions.js";
import { fsTokenAt, fsRecent, fsRemember } from "../lib/files.js";
import { fileToDataURL, MAX_IMAGES, MAX_IMAGE_BYTES } from "../lib/images.js";
import { fmtK, fmtK2, fmtTok, fmtDurMs, paintSpeed } from "../lib/format.js";
import { FsPicker } from "./FsPicker.js";

const html = htm.bind(h);

export function Composer() {
  const running = useStore((s) => s.running);
  const childrenActive = useStore((s) => s.childrenActive);
  const model = useStore((s) => s.model);
  const models = useStore((s) => s.models);
  const defaultModel = useStore((s) => s.defaultModel);
  const ctxWindow = useStore((s) => s.ctxWindow);
  const tokens = useStore((s) => s.tokens);
  const speed = useStore((s) => s.speed);
  const speedAccurate = useStore((s) => s.speedAccurate);
  const current = useStore((s) => s.current);
  const thinking = useStore((s) => s.thinking);
  const reasoningEffort = useStore((s) => s.reasoningEffort);
  const seed = useStore((s) => s.composerSeed);

  const [value, setValue] = useState("");
  const [images, setImages] = useState([]);
  const [modelMenu, setModelMenu] = useState(false);
  const [thinkPop, setThinkPop] = useState(false);
  const sendMode = useStore((s) => s.sendMode);
  const inputTip = useStore((s) => s.inputTip);
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

  /* ---------------- 发送 / 插话 ---------------- */
  const hasDraft = value.trim() !== "" || images.length > 0;
  const doSend = async () => {
    const text = value.trim();
    const imgs = images.slice();
    if (!text && imgs.length === 0) return;
    if (running) {
      // 插话:运行中只注入纯文本。图要等任务结束走普通发送。
      if (!text) return;
      if (imgs.length) { toast("插话不能带图片,请等任务结束后再发", "err"); return; }
      setValue("");
      closeFs();
      await sendSteer(text);
      return;
    }
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

  /* ---------------- 模型 / 思考 ---------------- */
  const modelName = (models.find((m) => m.name === model) || {}).name || model || "默认";
  const cap = ctxWindow || 0;
  // 需求④:token 不再报"多少 K / 多少 K",只画一个百分比小仪表。
  const ctxPct = cap > 0 ? Math.min(999, Math.round((tokens || 0) * 100 / cap)) : 0;
  const tokenWarn = cap > 0 && (tokens || 0) / cap >= 0.8;
  const meterTitle = "上下文占用:" + (tokens ? fmtK2(tokens) : "0") + (cap ? " / " + fmtK2(cap) : "")
    + "(" + ctxPct + "%),接近上限时自动压缩";

  // 双击模型按钮打开思考强度浮层(单击延迟 230ms 才切换模型下拉)。
  const openThinkingPop = () => {
    setThinkPop(true);
    setModelMenu(false);
  };

  // 底栏芯片要显示累计用量:每轮任务结束(running true→false)刷新一次快照,
  // 抽屉开没开都刷 —— 芯片数字要跟得上。
  const ctxInfo = useStore((s) => s.ctxInfo);
  const prevRun = useRef(false);
  useEffect(() => {
    if (prevRun.current && !running && getState().current) loadContext();
    prevRun.current = running;
  }, [running]);
  useEffect(() => {
    if (current && !getState().ctxInfo) loadContext();
  }, [current]);
  const totals = ctxInfo && ctxInfo.totals;
  const stats = (ctxInfo && ctxInfo.stats) || {};
  const speedText = paintSpeed(speed, speedAccurate);
  const speedShort = speed > 0 ? (speed >= 100 ? Math.round(speed) : Math.round(speed * 10) / 10) + " tok/s" : "—";
  const roundsN = ((ctxInfo && ctxInfo.rounds) || []).length;
  const stepsN = (ctxInfo && ctxInfo.steps) || 0;
  // 缓存命中率:供应商不报就明说"未上报",不拿 0% 冒充。
  const hitPct = totals && totals.reported && totals.input > 0
    ? Math.round(totals.cached * 100 / totals.input) + "%" : null;

  // 「会话统计」浮层(dsh 风):轮次/步/模型用时/工具调用用时/TTFT/输出速度。
  const [sessPop, setSessPop] = useState(false);
  useEffect(() => {
    if (!sessPop) return undefined;
    const onDoc = (e) => {
      if (!e.target || !e.target.closest || !e.target.closest(".sess-pop-wrap")) setSessPop(false);
    };
    document.addEventListener("click", onDoc, true);
    return () => document.removeEventListener("click", onDoc, true);
  }, [sessPop]);

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
        placeholder=${running ? "插话一句,下一步生效…(Enter 注入)" : (inputTip || "输入任务…")}
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
        <button class="chip-btn" id="btn-file-pick" title="选择文件夹路径插入输入框(也可直接输入 @)"
          onClick=${() => { openFs(""); taRef.current && taRef.current.focus(); }}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M4 7.2A2.2 2.2 0 0 1 6.2 5h3.4l1.8 2.2H17.8A2.2 2.2 0 0 1 20 9.4v7.4A2.2 2.2 0 0 1 17.8 19H6.2A2.2 2.2 0 0 1 4 16.8V7.2Z" />
          </svg>
          选择文件
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
        <button class="send-btn" id="btn-send" style=${running && !hasDraft ? "display:none" : ""} onClick=${doSend}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M3.5 12 20 4.5 15 20.5l-3.4-6.2L3.5 12Z" />
          </svg>
          ${running ? "插话" : "发送"}
        </button>
        <button class="stop-btn" id="btn-stop" style=${(running || childrenActive) && !(running && hasDraft) ? "display:inline-block" : "display:none"}
          onClick=${stop}>停止</button>
      </div>
      <input type="file" id="file-shot" accept="image/*" multiple hidden ref=${fileRef}
        onChange=${(e) => { addImageFiles(Array.from(e.target.files || [])); e.target.value = ""; }} />
    </div>
    <div class="composer-foot">
      <span class="sess-pop-wrap">
        <button class=${"foot-chip" + (sessPop ? " on" : "")} id="sess-stats"
          title="会话统计:轮次 / 步 / 模型用时 / 工具调用用时 / 首 token 平均"
          onClick=${() => setSessPop(!sessPop)}>${roundsN} 轮 · ${stepsN} 步 · ${speedShort}</button>
        ${sessPop ? html`<div class="foot-pop" id="sess-pop">
          <div class="fp-title">会话统计</div>
          <div class="fp-row"><span>轮次</span><b>${roundsN}</b></div>
          <div class="fp-row"><span>步数</span><b>${stepsN}</b></div>
          <div class="fp-row"><span>模型用时</span><b>${stats.model_ms ? fmtDurMs(stats.model_ms) : "—"}</b></div>
          <div class="fp-row"><span>工具调用用时</span><b>${stats.tool_ms ? fmtDurMs(stats.tool_ms) : "—"}</b></div>
          <div class="fp-row"><span>首 token 平均(TTFT)</span><b>${stats.ttft_avg_ms ? fmtDurMs(stats.ttft_avg_ms) : "未测"}</b></div>
          <div class="fp-row"><span>输出速度(TPS)</span><b>${speedText || "—"}</b></div>
        </div>` : null}
      </span>
      <button class="foot-chip" id="ctx-total"
        title="本会话累计用掉的上下文(历次请求输入之和)· 点击打开上下文管理"
        onClick=${() => toggleCtxPanel()}>${totals ? (fmtTok(totals.input) || "0 tok") : "—"} · 缓存命中 ${hitPct || "未上报"}</button>
      <span class="row-flex"></span>
      <span class=${"ctx-meter" + (tokenWarn ? " warn" : "")} id="token-usage" title=${meterTitle}>
        <i class="cm-bar"><i style=${{ width: Math.min(100, ctxPct) + "%" }}></i></i><b>${ctxPct}%</b>
      </span>
    </div>
  </div>`;
}
