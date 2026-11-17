// 无边框标题条:Windows 式右上窗口钮(最小化/最大化/关闭)。
// jsdom 里没有 mcWin* 桥,initTitlebar 必须直接躺平;补上假桥后断言:
// 钮按 Windows 顺序(左→右 min/max/close)渲染、点击回调对应函数、还原态换图,
// macOS 三点与旧的「设置/切换」文字钮都不许回来。文件尾还钉着一条
// layers.css 的布局契约 —— 那是桌面白屏事故的回归哨兵,动布局前先看它。
import test, { after } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { document, window } from "./helpers/dom.mjs";
import { initTitlebar, isFrameless } from "../js/lib/titlebar.js";

after(() => {
  document.body.innerHTML = "";
  delete window.mcWinMin;
  delete window.mcWinMax;
  delete window.mcWinClose;
  delete window.mcWinDrag;
  document.documentElement.classList.remove("frameless");
  try { window.close(); } catch (e) {}
});

test("frameless: web 模式(无桥函数)什么都不渲染", () => {
  initTitlebar();
  assert.equal(document.querySelector("#mc-titlebar"), null);
  assert.equal(isFrameless(), false);
});

test("frameless: 右上 Windows 式窗口钮 + 点击走原生回调", () => {
  const hits = { min: 0, max: 0, close: 0 };
  window.mcWinMin = () => { hits.min++; };
  window.mcWinMax = () => { hits.max++; };
  window.mcWinClose = () => { hits.close++; };
  window.mcWinDrag = () => {};

  initTitlebar();
  assert.ok(document.querySelector("#mc-titlebar"), "标题条渲染了");
  assert.ok(document.documentElement.classList.contains("frameless"));

  const btns = Array.from(document.querySelectorAll("#mc-titlebar .tb-dot"));
  assert.equal(btns.length, 4, "恰好四颗钮(v3.8:主题/min/max/close)");
  // 顺序:主题在最左,然后 Windows 顺序 = 最小化、最大化、关闭(关闭永远最右角)。
  assert.equal(btns[0].id, "tb-theme");
  assert.equal(btns[1].id, "tb-min");
  assert.equal(btns[2].id, "tb-max");
  assert.equal(btns[3].id, "tb-close");
  assert.ok(btns[1].classList.contains("tb-dot-n"), "最小化");
  assert.ok(btns[2].classList.contains("tb-dot-z"), "最大化");
  assert.ok(btns[3].classList.contains("tb-dot-x"), "关闭(hover 红由 CSS 管)");
  assert.ok(btns[1].querySelector("svg"), "钮用内联 SVG 画线,不靠字体字符");

  // v3.8:品牌行进标题条(E7 徽标 + Mini Code),坐侧栏正上方。
  const brand = document.querySelector("#mc-titlebar .tb-brand");
  assert.ok(brand, "标题条左侧是品牌标");
  assert.ok(brand.querySelector("svg.tb-e7"), "品牌标带 E7 徽标 svg");
  assert.match(brand.textContent, /Mini Code/);

  // 旧形态不许复活:macOS 三点在左、文字符号、右侧旧按钮组。
  assert.equal(document.querySelector("#tb-settings"), null, "标题条不放设置钮");
  assert.equal(document.querySelector(".tb-ctl"), null, "旧右侧按钮组不复存在");
  assert.equal(document.querySelector(".tb-app"), null, "纯文字应用名被品牌标取代");
  assert.ok(!/✕|−|＋/.test(document.querySelector("#mc-titlebar").textContent), "不再有字体字符符号");

  // 主题钮:图标已画 + 点击循环三态并更新 tooltip。
  assert.ok(btns[0].querySelector("svg"), "主题钮带图标");
  assert.match(btns[0].title, /切换主题/);

  btns[1].click();
  btns[2].click();
  btns[3].click();
  assert.deepEqual(hits, { min: 1, max: 1, close: 1 }, "三颗窗控钮分别走 min/max/close");

  // Go 侧 __mcZoom:还原态换 tooltip 和钮图形(□→⧉),不动结构。
  window.__mcZoom(true);
  assert.equal(document.querySelectorAll("#mc-titlebar .tb-dot").length, 4);
  assert.match(document.querySelector("#tb-max").title, /还原/);
  assert.equal(document.querySelectorAll("#tb-max rect").length, 1, "还原态仍是单 svg");
  assert.equal(document.querySelectorAll("#tb-max path").length, 1, "还原态比最大化多画一道错位边(path)");
  window.__mcZoom(false);
  assert.match(document.querySelector("#tb-max").title, /最大化/);
  assert.equal(document.querySelectorAll("#tb-max path").length, 0, "回到最大化:错位边收掉");
});

// 白屏事故契约(桌面整页空白、零报错):body 是 flex-direction:row,
// 标题条 width:100% 落进行主轴后独占整行,aside/main 被挤成 0 宽。
// 钉死修复:frameless 下 body 必须转列,#app 收回真正的行容器。
test("frameless: layers.css 布局契约 —— body 列、#app 行(白屏回归哨兵)", () => {
  const css = readFileSync(new URL("../css/layers.css", import.meta.url), "utf8");
  assert.match(css, /html\.frameless body \{ flex-direction: column; \}/,
    "frameless 下 body 必须是列方向,否则标题条独占一行、内容被挤没");
  assert.match(css, /html\.frameless #app \{[^}]*flex-direction: row;[^}]*flex: 1 1 auto/,
    "#app 必须显式接管行布局(web 模式的 display:contents 不能沿用)");
});

// v3.5 侧栏两改的源码契约:①徽标换成与 .ico 同款的完整 E7(穿环+卫星点),
// ②设置入口挪到侧栏列的最底部(渲染顺序上必须在会话列表之后)。
test("frameless: 侧栏源码契约 —— E7 徽标 + 设置沉底", () => {
  const src = readFileSync(new URL("../js/components/Sidebar.js", import.meta.url), "utf8");
  const mark = src.slice(src.indexOf("function BrandMark"), src.indexOf("function BrandMark") + 2200);
  assert.match(mark, /<ellipse /, "徽标带后环椭圆");
  assert.match(mark, /A236 104 -16/, "徽标带前穿环弧(与 .ico mini 版同款几何)");
  assert.match(mark, /#bm-sat/, "徽标带卫星辉光");
  assert.ok(src.indexOf('id="sess-list"') < src.indexOf('id="btn-settings"'),
    "设置按钮渲染在会话列表之后 = 侧栏列最底部");
});

// v3.7 定稿(用户样图):整个页面就两个圆角大框 —— 外框(侧栏+标题条+衬底
// 通体一色)+ 内框(聊天卡片)。不要透明、不要玻璃、不要多余切割线。
test("frameless: v3.7 两框定稿 —— 不透明 + 外框一色 + 聊天卡片", () => {
  const css = readFileSync(new URL("../css/layers.css", import.meta.url), "utf8");
  assert.doesNotMatch(css, /mc-glass/, "透视桌面机制已整体回退,layers.css 里不该再有 mc-glass");
  assert.match(css, /html\.frameless main \{ padding: \d+px \d+px \d+px \d+px; \}/,
    "main 内衬 = 外框包住内框的那圈");
  assert.match(css, /html\.frameless \.chat \{[^}]*border-radius: 18px; overflow: hidden/,
    "内框:聊天列整块圆角卡片");
  assert.match(css, /html\.frameless \.chat \{[^}]*background: var\(--bg\)/,
    "内框实色底,不是半透明");
  assert.match(css, /html\.frameless #mc-titlebar \{ background: var\(--surface\); border-bottom: none; \}/,
    "外框:标题条与侧栏同色(--surface)且无底边框");
  assert.match(css, /html\.frameless aside \{ border-right: none; background: var\(--surface\); \}/,
    "外框:侧栏同色、右框下线");
  // 标题条钮组宽度是 Win32 命中测试的 HTCAPTION 排除区,CSS 与 Go 必须一个数。
  const go = readFileSync(new URL("../../../desktop/windows/frameless.go", import.meta.url), "utf8");
  const cssW = css.match(/--tb-ctl-w: (\d+)px/);
  const goW = go.match(/tbControlsW = (\d+)/);
  assert.ok(cssW && goW && cssW[1] === goW[1],
    `--tb-ctl-w(${cssW && cssW[1]}) 与 Go tbControlsW(${goW && goW[1]}) 必须同步`);
});

// v3.11 背景定稿(用户):浅色主题外框/缝隙/内框一律 #ffffff——色差错位线不许再
// 出现。v3.12 深色搬进 VS Code 那套中灰(内框 #2b2b2b / 外框和缝隙 #3c3f41),
// 并把 .ico 同款星环水印整段删除;浅色通体纯白这条没动。
test("frameless: 背景定稿 —— 浅色通体纯白 + 深色 VS Code 中灰 + 无星环水印", () => {
  const css = readFileSync(new URL("../css/layers.css", import.meta.url), "utf8");
  const base = readFileSync(new URL("../css/base.css", import.meta.url), "utf8");
  // 浅色不许再有发灰底或冷灰衬底,通体纯白。
  assert.doesNotMatch(base, /--side: #fafafa|--side-a: #fafafa|--card-a: #fafafa/,
    "浅色侧栏/卡片不许再用发灰的 #fafafa");
  assert.match(base, /--bg: #ffffff/, "浅色聊天区底 = 纯白");
  assert.match(css, /html\.frameless \{ --frame-outer: #ffffff; --seam: #ffffff; \}/,
    "浅色外框/缝隙 = #ffffff:内外的色差错位线是不许出现的");
  // v3.12 深色 VS Code 化:内框 = 编辑器中灰 #2b2b2b,外框/缝隙 = 侧栏灰
  // #3c3f41,缝隙和外框同色(不再有一圈色差)。深色依旧保留外框/内框的明暗
  // 色差——那条边界沿圆角矩形走,是正的,和浅色通体纯白的处理是两回事。
  assert.match(base, /--bg: #2b2b2b/, "深色聊天区底 = VS Code 编辑器灰(旧值 #0b0b0e 是对齐官方图标的黑底,已让位)");
  assert.match(css, /html\.frameless\[data-theme="dark"\] \{ --frame-outer: #3c3f41; --seam: #3c3f41; \}/,
    "深色外框 = VS Code 侧栏灰,缝隙同色:不许有色差错位线");
  // v3.12:星环水印(发光环 + 白卫星 + 两枚流星)整体删除,内框就是纯色底。它是
  // 给近黑底设计的,底色抬到中灰后压在正文后面糊成一片脏影 —— 不许再以任何形式回来。
  assert.doesNotMatch(css, /--ring-wm/, "星环水印已下线,不许复活");
  assert.doesNotMatch(css, /html\.frameless \.chat \{[^}]*background-image/,
    "内框不贴图,只剩圆角 + 一条发丝描边");
  // 内框四周那圈衬底:文件开头 main 自带 background: var(--bg),不把外框色盖上去,
  // 侧栏和聊天卡之间就会糊出一条深色带(用户圈图点名的色差)。
  assert.match(css, /html\.frameless main \{ background: var\(--frame-outer\); \}/,
    "衬底必须跟标题条/侧栏同色,「两框」才只剩两个框");
});

// 步骤行的两条排版铁律(2026-11 用户圈图:① "write" 被断成两截;② 并行执行时
// 「正在执行」那一长列没收起来)。两处都是 CSS 与 JS 各写一半、只对得上一次改动
// 的那种坑,所以两条一起钉死。
test("frameless: 步骤行排版 —— 工具名不许拆字、正在执行卡的收起类名要和 CSS 对得上", () => {
  const css = readFileSync(new URL("../css/layers.css", import.meta.url), "utf8");
  const js = readFileSync(new URL("../js/components/Steps.js", import.meta.url), "utf8");
  const nameRule = (css.match(/\.ms-step \.t-name \{[^}]*\}/) || [""])[0];
  assert.match(nameRule, /white-space:\s*nowrap/, "工具名不许换行");
  assert.match(nameRule, /flex:\s*0 0 auto/, "工具名不许参与 flex 收缩:该被截断的是参数,不是名字");
  assert.match(css, /\.ms-live\.collapsed \.ms-live-body \{ display: none; \}/,
    "「正在执行」收起靠这条规则");
  assert.match(js, /class=\$\{"m-card ms-live" \+ \(open \? "" : " collapsed"\)\}/,
    "collapsed 落在带 .ms-live 的那张卡上 —— 挂到外层 .msg 上谁都匹配不到");
});
