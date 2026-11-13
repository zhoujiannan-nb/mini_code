// 插件中心:两个页签(已安装 / 插件市场),左边列表或网格,选中才展开详情。
// 数据全部现读后端(/api/plugins 每次现扫目录),前端不缓存"装了什么"。
import { h } from "preact";
import { useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, setState } from "../lib/store.js";
import {
  PLUGIN_CATEGORIES, plCatMeta, loadPluginCentre, plFetchCatalog, plStartPoll,
  plStopPoll, plToggle, plProcess, plLogs, plInstall, plUninstall, plMakePlugin,
} from "../lib/settings.js";
import { fmtBytes, fmtUptime } from "../lib/format.js";
import { BootPlayPanel, bootActions } from "./BootAnim.js";

const html = htm.bind(h);

function plStateClass(st) {
  return ({ running: "ok", starting: "wait", failed: "bad", stopped: "" })[st] || "";
}
function plStateLabel(st) {
  return ({ running: "运行中", starting: "启动中", failed: "失败", stopped: "已停止" })[st] || (st || "未知");
}

function PluginsTabs({ tab }) {
  return html`<div class="pl-tabs">
    <button class=${"pl-tab" + (tab === "installed" ? " active" : "")} data-tab="installed"
      onClick=${() => setState((s) => ({ ...s, plugins: { ...s.plugins, tab: "installed", sel: "" } }))}>已安装</button>
    <button class=${"pl-tab" + (tab === "market" ? " active" : "")} data-tab="market"
      onClick=${() => {
        setState((s) => ({ ...s, plugins: { ...s.plugins, tab: "market", mSel: "" } }));
        plFetchCatalog();
      }}>插件市场</button>
  </div>`;
}

function MakeBox() {
  return html`<details class="pl-make" id="pl-make-box">
    <summary class="pl-make-title">＋ 造一个插件
      <span class="pl-make-sub">让 agent 按标准契约给你写</span>
    </summary>
    <div class="pl-make-body">
      <div class="pl-make-sub">点一下会新建会话并把提示词填进输入框,你改完再发。</div>
      <div class="pl-make-row">
        ${[["process", "⚙️", "程序插件", "启动外部 exe / python 服务,可暴露给模型查状态"],
           ["tool", "🔧", "工具插件", "给模型追加一个工具(像网页搜索那样)"],
           ["appearance", "🎨", "外观插件", "皮肤 / 壁纸 / 插槽 / 动画 / 开机画面"]].map(([kind, icon, name, hint]) => html`
          <button class="pl-make-btn" data-make=${kind} key=${kind} onClick=${() => plMakePlugin(kind)}>
            <span class="pm-icon">${icon}</span>
            <span class="pm-name">${name}</span>
            <span class="pm-hint">${hint}</span>
          </button>`)}
      </div>
    </div>
  </details>`;
}

function Tile({ p }) {
  // 无条件订阅(不能写在 `p.boot &&` 后面,那会让 hook 调用顺序随插件类型变化)。
  const picked = useStore((s) => s.boot.picked);
  const isCur = !!p.boot && picked === p.id;
  const cat = plCatMeta(p.category);
  let stat = null;
  if (p.process) {
    stat = html`<span>
      <span class=${"pl-dot " + plStateClass(p.process.state)}></span>${plStateLabel(p.process.state)}
    </span>`;
  } else if (p.error) stat = "加载失败";
  else if (isCur) stat = html`<span><b>开机动画</b> · 正在使用</span>`;
  else if (p.boot) stat = "开机动画";
  return html`<div class=${"pl-tile" + (p.enabled ? "" : " is-off") + (isCur ? " has-cur" : "")}
    data-id=${p.id} data-category=${p.category || ""} role="button" tabIndex=${0}
    title=${(p.description || p.id) + " —— 点击查看详情"}
    onClick=${() => setState((s) => ({ ...s, plugins: { ...s.plugins, sel: p.id } }))}>
    ${isCur ? html`<span class="pl-tile-cur">当前</span>` : null}
    <span class="pl-tile-top">
      <span class="pl-tile-ico">${cat.icon}</span>
      <span class="pl-tile-name">${p.name || p.id}</span>
      <span class="pl-tagcat" title=${cat.hint}>${cat.title}</span>
    </span>
    <span class="pl-tile-desc">${p.description || p.id}</span>
    <span class="pl-tile-stat" style=${p.error ? "color:var(--red)" : ""}>${stat}</span>
    <span class="pl-tile-go">详情 ›</span>
  </div>`;
}

function DetailHead({ item, cat, actions }) {
  return html`<div class="pl-d-head">
    <span class="pl-d-icon">${cat.icon}</span>
    <div class="pl-d-title">
      <div class="pl-d-name">${item.name || item.id}</div>
      <div class="pl-d-meta">
        <span class="pl-tagcat pl-tagcat-lg">${cat.title}</span>
        <span class="pl-d-id">${item.id || ""}</span>
      </div>
    </div>
    ${actions ? html`<div class="pl-d-actions">${actions}</div>` : null}
  </div>`;
}

function InstalledDetail() {
  const plugins = useStore((s) => s.plugins);
  const p = plugins.list.find((x) => x.id === plugins.sel);
  if (!p) return null;
  const cat = plCatMeta(p.category);
  const picked = useStore((s) => s.boot.picked);
  const boot = p.boot ? bootActions(p, picked) : null;
  const actions = [];
  if (p.process) {
    const st = p.process;
    actions.push(html`<button key="start" class="mode-btn" data-act="start" disabled=${st.state === "running"}
      onClick=${() => plProcess(p.id, "start")}>启动</button>`);
    actions.push(html`<button key="stop" class="mode-btn" data-act="stop" disabled=${st.state !== "running" && st.state !== "starting"}
      onClick=${() => plProcess(p.id, "stop")}>停止</button>`);
    actions.push(html`<button key="restart" class="mode-btn" data-act="restart"
      onClick=${() => plProcess(p.id, "restart")}>重启</button>`);
    actions.push(html`<button key="logs" class="mode-btn" data-act="logs"
      onClick=${() => plLogs(p.id)}>日志</button>`);
  }
  if (boot) {
    actions.push(html`<button key="prev" class="mode-btn" data-act="boot-preview"
      onClick=${() => boot.preview()}>预览</button>`);
    if (!boot.isCur) actions.push(html`<button key="set" class="mode-btn primary-ghost" data-act="boot-set"
      onClick=${() => boot.set()}>设为开机动画</button>`);
  }

  const caps = [];
  if (p.ui) {
    if (p.ui.slots && p.ui.slots.length) caps.push(["插槽", p.ui.slots.join(" / ")]);
    if (p.ui.motion && p.ui.motion.length) caps.push(["动效", p.ui.motion.join(" / ")]);
    if (p.ui.accent) caps.push(["强调色", p.ui.accent]);
  }
  if (p.has_styles) caps.push(["皮肤", "CSS 注入"]);
  if (p.has_wallpaper) caps.push(["壁纸", "有"]);
  if (p.boot) caps.push(["开机动画", (p.boot.entry || "index.html") +
    (p.boot.duration_ms ? " · " + (Math.round(p.boot.duration_ms / 100) / 10) + " 秒" : "")]);
  if (p.tools && p.tools.length) caps.push(["工具", p.tools.join(", ")]);
  if (p.has_memory) caps.push(["记忆", "memory.md"]);

  const facts = [];
  if (p.category === "process" && p.process) {
    facts.push(["命令", p.process.command || "—"]);
    facts.push(["接口", p.process.endpoint || "—"]);
  }
  if (p.kinds && p.kinds.length) facts.push(["声明", p.kinds.join(", ")]);
  if (p.dir) facts.push(["目录", p.dir]);

  return html`<div id="pl-detail" class="pl-detail">
    <button class="pl-back" data-act="back" type="button"
      onClick=${() => setState((s) => ({ ...s, plugins: { ...s.plugins, sel: "" } }))}>‹ 全部插件</button>
    <${DetailHead} item=${p} cat=${cat} actions=${actions} />
    <div class="pl-d-desc">${p.description || p.id}</div>
    ${p.error ? html`<div class="pl-err">加载失败:${p.error}</div>` : null}
    ${p.reason ? html`<div class="pl-note">${p.reason}</div>` : null}
    ${p.process ? html`<div class="pl-d-sec">
      <div class="pl-d-sec-title">状态</div>
      <div class="pl-proc">
        <span class=${"pl-dot " + plStateClass(p.process.state)}></span>
        <span class="pl-proc-state">${plStateLabel(p.process.state)}</span>
        ${p.process.pid ? html`<span class="hint">PID ${p.process.pid}</span>` : null}
        ${p.process.uptime_seconds ? html`<span class="hint">已运行 ${fmtUptime(p.process.uptime_seconds)}</span>` : null}
        ${p.process.restarts ? html`<span class="hint">重启 ${p.process.restarts} 次</span>` : null}
      </div>
      ${p.process.endpoint ? html`<div class="pl-endpoint">接口 <code>${p.process.endpoint}</code></div>` : null}
      ${p.process.last_error ? html`<div class="pl-err">${p.process.last_error}</div>` : null}
    </div>` : null}
    ${p.boot ? html`<${BootPlayPanel} plugin=${p} />` : null}
    ${caps.length ? html`<div class="pl-d-sec">
      <div class="pl-d-sec-title">能力</div>
      <div class="pl-facts">${caps.map(([k, v], i) => html`<div class="pl-fact" key=${i}>
        <span>${k}</span><code>${v}</code></div>`)}</div>
    </div>` : null}
    ${facts.length ? html`<details class="pl-more pl-d-more"><summary>细节</summary>
      <div class="pl-facts">${facts.map(([k, v], i) => html`<div class="pl-fact" key=${i}>
        <span>${k}</span><code>${v}</code></div>`)}</div>
    </details>` : null}
    <div class="pl-foot">
      <label class="ck-row pl-toggle"><input type="checkbox" data-act="toggle" checked=${p.enabled}
        onChange=${(e) => plToggle(p.id, e.target.checked)} />启用</label>
      <button class="mode-btn danger-ghost" data-act="uninstall"
        onClick=${() => plUninstall(p.id, p.name)}>卸载</button>
    </div>
  </div>`;
}

function MarketDetail() {
  const plugins = useStore((s) => s.plugins);
  const e = plugins.catalog.find((x) => x.id === plugins.mSel);
  if (!e) return null;
  const cat = plCatMeta(e.category);
  let btn;
  if (plugins.busy === e.id) btn = html`<button class="mode-btn" disabled>下载中…</button>`;
  else if (e.installed && e.up_to_date) btn = html`<button class="mode-btn" disabled>已安装</button>`;
  else if (e.installed) btn = html`<button class="mode-btn primary-ghost" data-act="install"
    onClick=${() => plInstall(e.id, e.kind)}>更新到 v${e.version || "?"}</button>`;
  else btn = html`<button class="mode-btn primary-ghost" data-act="install"
    onClick=${() => plInstall(e.id, e.kind)}>安装</button>`;

  const facts = [];
  if (e.license) facts.push(["许可", e.license]);
  if (e.bytes) facts.push(["大小", fmtBytes(e.bytes) + " · " + e.files + " 个文件"]);
  if (e.author) facts.push(["作者", e.author]);
  return html`<div id="pl-market-detail" class="pl-detail">
    <button class="pl-back" data-act="back-market" type="button"
      onClick=${() => setState((s) => ({ ...s, plugins: { ...s.plugins, mSel: "" } }))}>‹ 全部插件</button>
    <${DetailHead} item=${e} cat=${cat} actions=${btn} />
    <div class="pl-d-desc">${e.description || ""}</div>
    ${facts.length ? html`<div class="pl-d-sec"><div class="pl-d-sec-title">信息</div>
      <div class="pl-facts">${facts.map(([k, v], i) => html`<div class="pl-fact" key=${i}>
        <span>${k}</span><code>${v}</code></div>`)}</div></div>` : null}
    ${(e.tags || []).length ? html`<div class="pl-look">${e.tags.map((t, i) => html`<span class="bt-badge" key=${i}>${t}</span>`)}</div>` : null}
  </div>`;
}

export function PluginCentre() {
  const plugins = useStore((s) => s.plugins);

  // 打开这一页时轮询进程状态(程序插件的状态会自己变),离开就停。
  useEffect(() => {
    loadPluginCentre();
    plStartPoll();
    return () => plStopPoll();
  }, []);

  const kinds = Array.from(new Set((plugins.catalog || []).map((e) => e.kind))).sort();
  const installed = plugins.list.filter((p) => !plugins.filter || p.category === plugins.filter);
  const market = plugins.catalogKind
    ? plugins.catalog.filter((e) => e.kind === plugins.catalogKind)
    : plugins.catalog;

  return html`<div>
    <${PluginsTabs} tab=${plugins.tab} />
    <div class=${"pl-pane" + (plugins.tab === "installed" ? " active" : "")} id="pl-pane-installed">
      ${plugins.tab === "installed" ? html`<div>
        <${MakeBox} />
        <div class="pl-bar">
          <span class="pl-bar-title">已安装 <b id="pl-count">${plugins.list.length}</b></span>
          <select id="pl-filter" class="pl-filter" value=${plugins.filter}
            onChange=${(e) => setState((s) => ({ ...s, plugins: { ...s.plugins, filter: e.target.value, sel: "" } }))}>
            <option value="">全部</option>
            ${PLUGIN_CATEGORIES.map((c) => html`<option value=${c.key} key=${c.key}>${c.title}</option>`)}
          </select>
          <button class="mode-btn" id="pl-reload" onClick=${() => loadPluginCentre("已重新扫描插件目录")}>重新扫描</button>
        </div>
        <div class="pl-dirbar">目录 <code id="pl-dir-now">${plugins.dir || "~/.mini_code/plugins"}</code></div>
        ${(!plugins.list.length || !installed.length)
          ? (plugins.sel ? html`<${InstalledDetail} />` : html`<div class="pl-empty">
              <div class="pl-empty-sub">${plugins.list.length
                ? "这个分类下还没有插件 —— 把筛选项改回「全部」试试。"
                : "还没有装插件。去「插件市场」挑一个,或用「造一个插件」让 agent 写一个。"}</div>
            </div>`)
          : (plugins.sel
            ? html`<${InstalledDetail} />`
            : html`<div id="pl-list" class="pl-grid" role="list">
                ${installed.map((p) => html`<${Tile} key=${p.id} p=${p} />`)}
              </div>`)}
      </div>` : null}
    </div>
    <div class=${"pl-pane" + (plugins.tab === "market" ? " active" : "")} id="pl-pane-market">
      ${plugins.tab === "market" ? html`<div>
        <div class="pl-bar">
          <span class="pl-bar-title">插件市场</span>
          <select id="pl-market-kind" class="pl-filter" value=${plugins.catalogKind}
            onChange=${(e) => setState((s) => ({ ...s, plugins: { ...s.plugins, catalogKind: e.target.value, mSel: "" } }))}>
            <option value="">全部</option>
            ${kinds.map((k) => html`<option value=${k} key=${k}>${k}</option>`)}
          </select>
          <button class="mode-btn" id="pl-market-fetch" onClick=${() => plFetchCatalog()}>刷新</button>
        </div>
        <div class="pl-dirbar" id="pl-market-msg">${plugins.catalog.length
          ? "来自 " + (plugins.repo || "插件仓库") + " · " + plugins.catalog.length + " 个"
          : (plugins.catalogMsg || "从插件仓库读取可下载的插件")}</div>
        ${(!plugins.catalog.length || !market.length)
          ? (plugins.mSel ? html`<${MarketDetail} />` : html`<div class="pl-empty">
              <div class="pl-empty-sub">${plugins.catalog.length
                ? "这个分类下没有可下载的插件 —— 把筛选项改回「全部」试试。"
                : (plugins.catalogMsg || "还没有读取插件仓库") + "(点上面的「刷新」重新读取;" +
                  (plugins.repo || "zhoujiannan-nb/mini_code-plugins") + ")"}</div>
            </div>`)
          : (plugins.mSel ? html`<${MarketDetail} />` : html`<div id="pl-market-list" class="pl-grid" role="list">
              ${market.map((e) => html`<div class="pl-tile" data-id=${e.id} data-category=${e.category || ""}
                  role="button" tabIndex=${0} key=${e.id}
                  title=${(e.description || e.id) + " —— 点击查看详情"}
                  onClick=${() => setState((s) => ({ ...s, plugins: { ...s.plugins, mSel: e.id } }))}>
                  <span class="pl-tile-top">
                    <span class="pl-tile-ico">${plCatMeta(e.category).icon}</span>
                    <span class="pl-tile-name">${e.name || e.id}</span>
                    <span class="pl-tagcat">${plCatMeta(e.category).title}</span>
                  </span>
                  <span class="pl-tile-desc">${e.description || e.id}</span>
                  <span class="pl-tile-stat">${e.installed && e.up_to_date
                    ? html`<b>已安装</b>`
                    : (e.installed ? html`<span><b>可更新</b> · v${e.version || "?"}</span>` : "v" + (e.version || "?"))}</span>
                  <span class="pl-tile-go">详情 ›</span>
                </div>`)}
            </div>`)}
      </div>` : null}
    </div>
  </div>`;
}
