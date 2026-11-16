// 侧栏(v3 样图版):品牌 + 主题切换、新建会话(⌘K)、历史会话(ⓘ + 搜索 +
// 按工作区分流,顺序由聊天头「排序」按钮轮转)、设置入口(列底)。
//
// 按样图取舍:不放「工作台 / 插件市场」导航、底部宣传卡与在线状态 —— 这些是
// 用户明确划掉的东西。worker(工作区)不再占用一条常显的切换条,而是收进
// 「历史会话」行尾的漏斗弹层里:列表按当前工作区过滤,入口只有一个。
// 主题切换按钮从顶栏搬进品牌行右侧(样图那颗太阳)。
//
// 列表的三种状态(加载骨架 / 拉取失败 / 空)都画出来 —— 静默空白会让人以为
// "会话没了"。空态配了一张文件夹 + 挂钟的插画。
import { h } from "preact";
import { useRef, useState } from "preact/hooks";
import htm from "htm";

import { useStore, setState, getState, toast } from "../lib/store.js";
import { AgentAvatar } from "../lib/avatars.js";
import { relTime } from "../lib/format.js";
import { cycleThemeMode, themeLabel } from "../lib/theme.js";
import { openSettings } from "../lib/settings.js";
import {
  selectSession, renameSession, deleteSession, detachWorkerForNewSession, resetToNew,
  openSuperSession,
  workerEmptyHint, workerBucketId, workerLabel, WK_OTHER,
} from "../lib/actions.js";

const html = htm.bind(h);

// 拉列表中的占位:三行会话形状的骨架,真行落下来时面板不会跳。
export function sessSkeleton() {
  return html`<div class="sess-list-skeleton">
    ${[0, 1, 2].map((i) => html`<div class="sess-item sess-skeleton" aria-hidden="true" key=${i}>
      <div class="sk sk-title"></div><div class="sk sk-meta"></div>
    </div>`)}
  </div>`;
}

// 目录尾段:取工作目录最后两级,用来区分名字相同的实例
// (每个 mc 实例一个 worker 时,标题雷同,目录尾两级才是身份)。
function tailPath(p) {
  if (!p) return "";
  const segs = p.replace(/\\/g, "/").split("/").filter(Boolean);
  return segs.slice(-2).join("/");
}

function SessionItem({ s }) {
  const current = useStore((x) => x.current);
  // 执行中只看服务端的实时标志:持久化的 status 在进程崩溃/重启后可能停留在
  // "running",不可信。后台子 agent 在跑也算执行中;会话自己的后台命令还活着时
  // 按"后台运行中"显示(agent 空闲等通知,但活确实还在干)。
  const busy = s.active || !!s.children_active || (s.jobs_running > 0);
  const dotClass = busy ? "running" : (["completed", "failed", "interrupted"].includes(s.status) ? s.status : "idle");
  const statusText = s.active ? "执行中" : (s.children_active ? "子任务中"
    : (s.jobs_running > 0 ? "后台运行中"
      : ({ completed: "已完成", failed: "失败", interrupted: "已中断" }[s.status] || "")));
  return html`<div class=${"sess-item" + (s.session_id === current ? " active" : "")}
    onClick=${(e) => { if (e.target.closest("button")) return; selectSession(s.session_id); }}>
    <div class="sess-title-row">
      <${AgentAvatar} cls="sess-ava" tag="span" />
      <div class="sess-title">${s.title || "未命名会话"}</div>
      <span class="sess-time">${relTime(s.updated_at)}</span>
    </div>
    <div class="sess-meta">
      <span class=${"s " + dotClass}></span>
      ${s.mode === "cautious" ? html`<span title="谨慎模式">🛡</span>` : null}
      ${statusText ? html`<span>${statusText}</span>` : null}
      <span>${s.message_count || 0} 条</span>
      ${tailPath(s.work_dir) ? html`<span class="sess-path" title=${s.work_dir}>🗂 ${tailPath(s.work_dir)}</span>` : null}
    </div>
    <button class="rename-btn" title="重命名会话"
      onClick=${(e) => { e.stopPropagation(); renameSession(s.session_id, s.title); }}>✎</button>
    <button class="del-btn" title="删除会话"
      onClick=${(e) => { e.stopPropagation(); deleteSession(s.session_id, s.title); }}>✕</button>
  </div>`;
}

// 品牌徽标:与 .ico 完全同款的 E7「卫星穿环」—— 墨块 + 斜体 M + 前后穿环 +
// 卫星点。512 坐标空间的 mini 版(块 64..448 rx88 / 环 rx236 ry104 stroke18 /
// 卫星 r40@342,358):30px 下粗描边才看得见。环在块两侧出界,viewBox 横向
// 留了余量(-26 起宽 564),元素本身 44×30。配色走 --ink/--ink-fg,与欢迎页
// Hero 同一张脸,暗色主题自动翻。渐变 id 用 bm-* 前缀,别和 Hero 的 hl-* 撞。
function BrandMark() {
  return html`<span class="brand-mark" aria-hidden="true">
    <svg viewBox="-26 64 564 384" fill="none">
      <defs>
        <linearGradient id="bm-g" x1="20" y1="343" x2="483" y2="213" gradientUnits="userSpaceOnUse">
          <stop offset="0" stop-color="#5ec5ff"/><stop offset="1" stop-color="#a06bff"/>
        </linearGradient>
        <radialGradient id="bm-sat" cx="0.5" cy="0.5" r="0.5">
          <stop offset="0" stop-color="#ffffff"/><stop offset="0.35" stop-color="#9fd0ff"/>
          <stop offset="1" stop-color="#4f7dff" stop-opacity="0"/>
        </radialGradient>
      </defs>
      <ellipse cx="256" cy="278" rx="236" ry="104" stroke="url(#bm-g)" stroke-opacity="0.38"
        stroke-width="18" transform="rotate(-16 256 278)"/>
      <rect x="64" y="64" width="384" height="384" rx="88" fill="var(--ink)"/>
      <rect x="65.5" y="65.5" width="381" height="381" rx="86.5" stroke="var(--border)"
        stroke-opacity="0.6" stroke-width="3"/>
      <g transform="translate(256 256) scale(0.66) skewX(-9) translate(-256 -256)">
        <polygon fill="var(--ink-fg)"
          points="148,356 148,156 212,156 256,244 300,156 364,156 364,356 316,356 316,232 256,320 196,232 196,356"/>
      </g>
      <path d="M482.8 213 A236 104 -16 0 1 29.2 343" stroke="url(#bm-g)" stroke-width="22" stroke-linecap="round"/>
      <circle cx="342" cy="358" r="40" fill="url(#bm-sat)"/>
      <circle cx="342" cy="358" r="15" fill="#f2f8ff"/>
    </svg></span>`;
}

// 快捷键角标:样图是 mac 的 ⌘K,Windows/Linux 显示 Ctrl K。
const IS_MAC = typeof navigator !== "undefined"
  && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");

// 空态插画:文件夹 + 挂钟(样图左侧那张灰调小画)。纯描边,跟着主题灰阶走。
function EmptyHistory({ sub }) {
  return html`<div class="sess-empty-state">
    <svg class="ses-art" viewBox="0 0 96 72" fill="none" aria-hidden="true">
      <path d="M10 20c0-3.3 2.7-6 6-6h16l6 7h32c3.3 0 6 2.7 6 6v27c0 3.3-2.7 6-6 6H16c-3.3 0-6-2.7-6-6V20Z"
        fill="var(--surface)" stroke="var(--border2)" stroke-width="2"/>
      <path d="M10 30h66" stroke="var(--border2)" stroke-width="2"/>
      <circle cx="66" cy="46" r="13" fill="var(--side)" stroke="var(--border2)" stroke-width="2"/>
      <path d="M66 39.5V46l4.5 3" stroke="var(--text3)" stroke-width="2" stroke-linecap="round"/>
    </svg>
    <div class="ses-title">还没有历史会话</div>
    <div class="ses-sub">${sub}</div>
  </div>`;
}

export function Sidebar() {
  const sessions = useStore((s) => s.sessions);
  const loading = useStore((s) => s.sessionsLoading);
  const error = useStore((s) => s.sessionsError);
  const menuOpen = useStore((s) => s.workerMenuOpen);
  const sessSort = useStore((s) => s.sessSort);
  const themeMode = useStore((s) => s.themeMode);
  const currentRole = useStore((s) => s.currentRole);
  const superOwned = useStore((s) => s.superOwned);
  const [query, setQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const pickRef = useRef(null);
  const searchRef = useRef(null);

  const onTheme = () => {
    const next = cycleThemeMode();
    setState({ themeMode: next });
    toast("主题:" + themeLabel(next), "ok");
  };
  const tmode = themeMode || "system";

  // 漏斗 = 按工作区分流(打开 worker 弹层);行尾的放大镜 = 标题搜索。
  const openFilter = (e) => {
    e.stopPropagation();
    const el = pickRef.current;
    const st = getState();
    if (st.workerMenuOpen) { setState({ workerMenuOpen: false }); return; }
    const r = el ? el.getBoundingClientRect() : { left: 12, bottom: 120, width: 200 };
    setState({ workerMenuOpen: true, workerMenuAnchor: { left: r.left, bottom: r.bottom, width: r.width } });
  };
  const toggleSearch = () => {
    setSearchOpen((v) => {
      if (!v) requestAnimationFrame(() => searchRef.current && searchRef.current.focus());
      return !v;
    });
  };

  const q = query.trim().toLowerCase();
  // 分区标题显示当前工作区名(用户要求「历史会话」改成工作区名);
  // 「其他」桶没有具体工作区可报,退回「历史会话」。
  const wid = workerBucketId();
  const wkRec = (getState().workers || []).find((x) => x.worker_id === wid);
  const wkName = wid === WK_OTHER || !wkRec ? "历史会话" : wkRec.name;
  let shown = q ? (sessions || []).filter((s) => String(s.title || "").toLowerCase().includes(q)) : (sessions || []).slice();
  // 「排序」按钮的三种顺序:最近活跃 = 服务端原序(updated_at 倒序),不动它。
  if (sessSort === "oldest") shown.reverse();
  else if (sessSort === "title") shown.sort((a, b) => String(a.title || "").localeCompare(String(b.title || ""), "zh"));

  let list;
  if (loading) list = sessSkeleton();
  else if (error) {
    list = html`<div class="empty-hint">会话列表没刷出来：${error}<br />30s 轮询会自动重试,换个工作区也能立刻重来</div>`;
  } else if (!shown.length) {
    if (q) list = html`<div class="empty-hint">没有标题含「${query.trim()}」的会话</div>`;
    else {
      // 默认桶(其他)按样图给两句人话;具体 worker 桶保留"先切到它再新建"的指路。
      const hint = workerEmptyHint();
      const sub = workerBucketId() === WK_OTHER ? "开始一个新的对话吧" : (hint.split("\n")[1] || "");
      list = html`<${EmptyHistory} sub=${sub} />`;
    }
  } else {
    list = shown.map((s) => html`<${SessionItem} key=${s.session_id} s=${s} />`);
  }

  return html`<aside>
    <div class="brand">
      <${BrandMark} />
      <div class="logo">Mini Code</div>
      <span class="brand-flex"></span>
      <button class="brand-theme" id="btn-theme" title=${"切换主题:浅色 / 深色 / 跟随系统(当前:" + themeLabel(tmode) + ")"}
        onClick=${onTheme}>
        ${tmode === "dark" ? html`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor"
          stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M20 13.5A8.5 8.5 0 1 1 10.5 4 6.8 6.8 0 0 0 20 13.5Z" /></svg>`
          : tmode === "system" ? html`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor"
          stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <rect x="3" y="4.5" width="18" height="12.5" rx="2" /><path d="M9 20.5h6" /></svg>`
          : html`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor"
          stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <circle cx="12" cy="12" r="4.2" />
          <path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M18.5 5.5l-1.7 1.7M7.2 16.8l-1.7 1.7" /></svg>`}
      </button>
    </div>
    <button class="new-btn" id="btn-new" title="新建会话（落在当前选中的工作区;快捷键 Ctrl/⌘+K）"
      onClick=${() => { detachWorkerForNewSession(); resetToNew(); }}>
      <span class="nb-plus">＋</span><span class="nb-label">新建会话</span>
      <kbd class="nb-kbd">${IS_MAC ? "⌘ K" : "Ctrl K"}</kbd>
    </button>
    <button class=${"super-entry" + (currentRole === "super" || superOwned ? " on" : "")} id="btn-super"
      title="Super-Agent:分派任务的总控会话(worker / 定时任务 / 消息通道 / 进程 / 审计)"
      onClick=${openSuperSession}>
      <svg class="nb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="5" r="2.3" /><circle cx="5" cy="18" r="2.3" /><circle cx="19" cy="18" r="2.3" />
        <path d="M12 7.3 6.4 15.9M12 7.3l5.6 8.6M7.3 18h9.4" />
      </svg>
      Super-Agent
    </button>
    <div class="sess-head">
      <span class="sh-label" title=${"历史会话 · 当前工作区:" + wkName}>${wkName}</span>
      <span class="sh-info" title=${"共 " + (sessions || []).length + " 个会话;顺序可点聊天区右上角「排序」切换"}>
        <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor"
          stroke-width="1.8" stroke-linecap="round" aria-hidden="true">
          <circle cx="12" cy="12" r="8.6" /><path d="M12 11v5.2" /><circle cx="12" cy="7.8" r=".4" fill="currentColor" />
        </svg>
      </span>
      <span class="sh-flex"></span>
      <button class="sh-btn" id="btn-search-toggle" title=${searchOpen ? "收起搜索" : "搜索会话标题"}
        onClick=${toggleSearch}>
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor"
          stroke-width="1.8" stroke-linecap="round" aria-hidden="true">
          <circle cx="11" cy="11" r="6.5" /><path d="m16 16 4.5 4.5" />
        </svg>
      </button>
      <button class=${"sh-btn" + (menuOpen ? " on" : "")} id="btn-wk-filter"
        ref=${pickRef} title="按工作区分流 / 管理工作区" onClick=${openFilter}>
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor"
          stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M4 5.5h16l-6.2 7.2v5.3l-3.6-1.8v-3.5Z" />
        </svg>
      </button>
    </div>
    ${searchOpen ? html`<div class="sess-search">
      <input ref=${searchRef} type="text" value=${query} placeholder="搜索会话标题…"
        onInput=${(e) => setQuery(e.target.value)}
        onKeyDown=${(e) => { if (e.key === "Escape") { setQuery(""); setSearchOpen(false); } }} />
      ${query ? html`<button class="ss-clear" title="清空" onClick=${() => setQuery("")}>✕</button>` : null}
    </div>` : null}
    <div class="sess-list" id="sess-list">${list}</div>
    <button class="nav-btn" id="btn-settings" title="打开设置" onClick=${openSettings}>
      <svg class="nb-ico" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="12" r="3.1" />
        <path d="M12 2.8v3M12 18.2v3M2.8 12h3M18.2 12h3M5.5 5.5l2.1 2.1M16.4 16.4l2.1 2.1M18.5 5.5l-2.1 2.1M7.6 16.4l-2.1 2.1" />
      </svg>
      设置
    </button>
  </aside>`;
}
