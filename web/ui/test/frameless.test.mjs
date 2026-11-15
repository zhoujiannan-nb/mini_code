// 无边框标题条 v2:macOS 三点窗控(需求①)。
// jsdom 里没有 mcWin* 桥,initTitlebar 必须直接躺平;补上假桥后断言:
// 三点按 红(关闭)/黄(最小化)/绿(最大化) 顺序渲染、点击回调对应函数,
// 旧的「设置 / 切换」文字钮与右侧按钮组都不许回来。
import test, { after } from "node:test";
import assert from "node:assert/strict";

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

test("frameless: 三点窗控渲染 + 点击走原生回调", () => {
  const hits = { min: 0, max: 0, close: 0 };
  window.mcWinMin = () => { hits.min++; };
  window.mcWinMax = () => { hits.max++; };
  window.mcWinClose = () => { hits.close++; };
  window.mcWinDrag = () => {};

  initTitlebar();
  assert.ok(document.querySelector("#mc-titlebar"), "标题条渲染了");
  assert.ok(document.documentElement.classList.contains("frameless"));

  const dots = Array.from(document.querySelectorAll("#mc-titlebar .tb-dot"));
  assert.equal(dots.length, 3, "恰好三颗点");
  assert.equal(dots[0].id, "tb-close");
  assert.equal(dots[1].id, "tb-min");
  assert.equal(dots[2].id, "tb-max");
  assert.ok(dots[0].classList.contains("tb-dot-x"), "红=关闭");
  assert.ok(dots[1].classList.contains("tb-dot-n"), "黄=最小化");
  assert.ok(dots[2].classList.contains("tb-dot-z"), "绿=最大化");

  // 旧形态不许复活:两颗文字钮 + 右侧按钮组。
  assert.equal(document.querySelector("#tb-settings"), null, "标题条不再放设置钮");
  assert.equal(document.querySelector("#tb-theme"), null, "标题条不再放主题钮");
  assert.equal(document.querySelector(".tb-ctl"), null, "右侧按钮组已移除");

  dots[0].click();
  dots[1].click();
  dots[2].click();
  assert.deepEqual(hits, { min: 1, max: 1, close: 1 }, "三颗点分别走 close/min/max");

  // Go 侧 __mcZoom 的还原态只改 tooltip,不动结构。
  window.__mcZoom(true);
  assert.equal(document.querySelectorAll("#mc-titlebar .tb-dot").length, 3);
  assert.match(document.querySelector("#tb-max").title, /还原/);
});
