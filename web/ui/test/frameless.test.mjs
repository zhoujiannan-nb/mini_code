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

// v3.11 背景定稿(用户):浅色主题外框/缝隙/内框一律 #ffffff——色差错位线不
// 许再出现;水印星环严格按官方 .ico 复刻:左蓝右紫渐变细环 + 发光底层 +
// 白色发光卫星,居中,带两枚流星。
test("frameless: v3.11 背景定稿 —— 通体纯白 + .ico 复刻发光星环", () => {
  const css = readFileSync(new URL("../css/layers.css", import.meta.url), "utf8");
  const base = readFileSync(new URL("../css/base.css", import.meta.url), "utf8");
  // 浅色不许再有发灰底或冷灰衬底,通体纯白。
  assert.doesNotMatch(base, /--side: #fafafa|--side-a: #fafafa|--card-a: #fafafa/,
    "浅色侧栏/卡片不许再用发灰的 #fafafa");
  assert.match(base, /--bg: #ffffff/, "浅色聊天区底 = 纯白");
  assert.match(css, /html\.frameless \{ --frame-outer: #ffffff; --seam: #ffffff; \}/,
    "浅色外框/缝隙 = #ffffff:内外的色差错位线是不许出现的");
  // 深色:内框 = 图标黑底,外框保留色差(边界沿圆角矩形,是正的)。
  assert.match(base, /--bg: #0b0b0e/, "深色聊天区底 = 图标黑底色");
  assert.match(css, /--frame-outer: #16161a/, "深色外框有独立收边值");
  // 星环水印:两主题都是 .ico 同款蓝→紫渐变细环(rx236 ry104 rot-16 同几何)。
  assert.equal((css.match(/--ring-wm: url\("data:image\/svg\+xml/g) || []).length, 2,
    "明暗主题各有一条水印 data-uri");
  assert.equal((css.match(/linearGradient id='rg'/g) || []).length, 2, "两条渐变描边");
  assert.ok((css.match(/stop-color='%237cc7ff'/g) || []).length >= 4,
    "渐变起笔 = .ico 浅蓝(环 + 流星,两主题共 4 处以上)");
  assert.ok((css.match(/stop-color='%23b06cff'/g) || []).length >= 4,
    "渐变收笔 = .ico 紫");
  assert.equal((css.match(/rx='236' ry='104'/g) || []).length, 4, "环几何与 .ico 同款(两主题×两层)");
  // "有点发光":每主题环下都垫一条 4 倍粗的低透明光晕层(细环本身是 sw8)。
  assert.equal((css.match(/stroke-width='30'/g) || []).length, 2, "两主题各有一层光晕环");
  assert.equal((css.match(/stroke-width='8'/g) || []).length, 2, "两主题各有一层细环");
  // 卫星 = .ico 同款白色发光点(带蓝晕)。
  assert.equal((css.match(/circle cx='342' cy='358' r='9' fill='%23ffffff'/g) || []).length, 2,
    "两主题卫星都是白色发光核");
  // 流星:一大一小 + 发光头,两主题都在。
  assert.equal((css.match(/M158 112 L70 172/g) || []).length, 2, "大流星尾迹×2");
  assert.equal((css.match(/M470 142 L416 182/g) || []).length, 2, "小流星尾迹×2");
  // 居中引用。
  assert.match(css, /html\.frameless \.chat \{[^}]*background-image: var\(--ring-wm\)/,
    "内框引用水印");
  assert.match(css, /background-position: 50% 52%;/, "水印居中偏下一丢丢");
});
