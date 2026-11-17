// 真渲染测试:在 jsdom 里 mount App,fetch / EventSource 都用假实现注入,
// 断言的是 DOM 与请求 payload,不是源码字符串。
//
// 覆盖:会话列表 → 打开会话 → 发消息 → 流式 delta 只动最后一条;
// 路径可点 + 点击 POST /api/fs/open;运行时注入 notice 不冒充用户;
// 断线对账去重(同一 call_id 的步骤只出现一次)。
import test, { after } from "node:test";
import assert from "node:assert/strict";

import { document, window, flush } from "./helpers/dom.mjs";
import { h, render } from "preact";
import htm from "htm";

import { App } from "../js/components/App.js";
import { summarize } from "../js/components/SummaryPanel.js";
import { setEventSourceFactory, disconnectEvents } from "../js/lib/sse.js";
import { setState, getState, initialState } from "../js/lib/store.js";

const html = htm.bind(h);
const settle = async () => { for (let i = 0; i < 8; i++) await flush(); };

// jsdom 窗口自己持有时钟:不关掉它,node --test 跑完用例也退不出去。
after(() => {
  for (const root of roots) { try { render(null, root); } catch (e) {} }
  roots = [];
  disconnectEvents();
  try { window.close(); } catch (e) {}
});

/* ---------------- 假 fetch ---------------- */
function jsonResponse(data, status = 200, statusText = "OK") {
  return { ok: status < 400, status, statusText, json: async () => data };
}

function makeFetch(routes) {
  const calls = [];
  const fn = async (url, opts = {}) => {
    calls.push({ url: String(url), opts });
    for (const [pattern, handler] of routes) {
      if (pattern.test(String(url))) return handler(String(url), opts);
    }
    return jsonResponse({});
  };
  fn.calls = calls;
  fn.callsTo = (needle) => calls.filter((c) => c.url.includes(needle));
  return fn;
}

/* ---------------- 假 EventSource ---------------- */
class FakeEventSource {
  constructor(url) {
    this.url = url;
    this.listeners = {};
    this.closed = false;
    FakeEventSource.instances.push(this);
  }
  addEventListener(name, fn) { (this.listeners[name] = this.listeners[name] || []).push(fn); }
  close() { this.closed = true; }
  open() { if (this.onopen) this.onopen(); }
  emit(msg) { if (this.onmessage) this.onmessage({ data: JSON.stringify(msg) }); }
  ping() { for (const fn of this.listeners.ping || []) fn(); }
}
FakeEventSource.instances = [];

/* ---------------- 夹具 ---------------- */
const WORKDIR = "D:\\work\\s1";

function sessionA(extra) {
  return Object.assign({
    session_id: "s1",
    title: "测试会话 A",
    status: "idle",
    active: false,
    children_active: false,
    mode: "full",
    model: "m1",
    work_dir: WORKDIR,
    worker_id: "w-other",
    updated_at: new Date(Date.now() - 60000).toISOString(),
    message_count: 2,
    jobs_running: 0,
    jobs_total: 0,
  }, extra || {});
}

function sessionB() {
  return sessionA({ session_id: "s2", title: "测试会话 B", updated_at: new Date().toISOString() });
}

const HISTORY = [
  { role: "user", content: "你好" },
  { role: "assistant", content: "你好,我是 Mini Code" },
];

function routesFor({ sessions, messages, events, extra } = {}) {
  const list = sessions || [sessionA(), sessionB()];
  const msgs = messages || HISTORY;
  const routes = [
    [/^\/api\/status$/, () => jsonResponse({
      web_port: 4321,
      models: [{ name: "m1", provider: "vllm", context_window: 100000 }],
      default_model: "m1",
      context_window: 100000,
    })],
    [/^\/api\/sessions\?/, () => jsonResponse(list)],
    [/^\/api\/sessions\/[^/]+\/children$/, () => jsonResponse([])],
    [/^\/api\/sessions\/[^/]+\/jobs$/, () => jsonResponse({ jobs: [] })],
    [/^\/api\/sessions\/[^/]+\/jobs\/output/, () => jsonResponse({ output: "" })],
    [/^\/api\/sessions\/[^/]+\/events/, () => jsonResponse(events || { active: false, events: [] })],
    [/^\/api\/sessions\/[^/]+$/, (url) => {
      const id = url.split("/").pop();
      const s = list.find((x) => x.session_id === id) || list[0];
      return jsonResponse({
        session: s,
        messages: msgs,
        tokens: 123,
        context_window: 100000,
        last_usage: null,
      });
    }],
    [/^\/api\/workers$/, () => jsonResponse({
      workers: [{ worker_id: "w-other", name: "其他", reserved: true, work_dir: "D:\\work", sessions: list.length }],
      unassigned: 0,
      unassigned_active: 0,
    })],
    [/^\/api\/avatar$/, () => jsonResponse({ avatar: "" })],
    [/^\/api\/agent-avatar$/, () => jsonResponse({ avatar: "" })],
    [/^\/api\/approvals$/, () => jsonResponse([])],
    [/^\/api\/plugins$/, () => jsonResponse({ plugins: [], dir: "", slots: [] })],
    [/^\/api\/bootanim$/, () => jsonResponse({ dir: "", legacy_dir: "", themes: [], play: null })],
    [/^\/api\/config$/, () => jsonResponse({ web_port: 4321, models: [], default_model: "", user_avatar: "", agent_avatar: "" })],
    [/^\/api\/fs\/open$/, () => jsonResponse({ ok: true })],
    [/^\/api\/fs\/search/, () => jsonResponse({ dirs: [] })],
    [/^\/api\/messages$/, () => jsonResponse({ task_id: "t1", session_id: "s1" }, 202, "Accepted")],
  ];
  return (extra || []).concat(routes);
}

let roots = [];
function mountApp() {
  const root = document.createElement("div");
  root.id = "app";
  document.body.appendChild(root);
  roots.push(root);
  render(h(App, {}), root);
  return root;
}

async function resetAll(options) {
  // 清掉上一个用例的 DOM / 监听 / SSE
  for (const root of roots) { try { render(null, root); } catch (e) {} root.remove(); }
  roots = [];
  disconnectEvents();
  FakeEventSource.instances = [];
  document.body.innerHTML = "";
  try { localStorage.clear(); } catch (e) {}
  setState(Object.assign({}, initialState, {
    rows: [], run: { steps: [] }, approvals: {}, sessions: [], workers: [],
    children: [], jobs: [], toast: null, live: null, pending: null,
    userAvatar: "", agentAvatar: "", current: null, running: false, sending: false,
  }));
  const mock = makeFetch(routesFor(options));
  globalThis.fetch = mock;
  setEventSourceFactory((url) => new FakeEventSource(url));
  return mock;
}

/* ================= 1) 会话列表 / 打开 / 发消息 / 流式 ================= */
test("render: 会话列表、点击会话渲染消息、发送写入 DOM、流式只动最后一条", async () => {
  const fetchMock = await resetAll();
  mountApp();
  await settle();

  const items = Array.from(document.querySelectorAll("#sess-list .sess-item"));
  assert.equal(items.length, 2, "两个会话都渲染出来");
  assert.match(document.querySelector("#sess-list").textContent, /测试会话 A/);

  // 点击第二个会话(会话 B) → 选中并渲染它的消息
  items[1].click();
  await settle();
  assert.equal(getState().current, "s2", "点击会话切换 current");
  assert.match(document.querySelector("#chat-title").textContent, /测试会话 B/);
  assert.ok(document.querySelectorAll("#messages .msg").length >= 2, "历史消息渲染出来了");
  assert.match(document.querySelector("#messages").textContent, /你好,我是 Mini Code/);

  // 发一条消息:填输入框 → 点发送 → POST /api/messages 带内容,且用户消息进 DOM
  const ta = document.querySelector("#input");
  ta.value = "请继续";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  document.querySelector("#btn-send").click();
  await settle();

  const posts = fetchMock.callsTo("/api/messages");
  assert.equal(posts.length, 1, "发了一次消息");
  assert.deepEqual(JSON.parse(posts[0].opts.body), { content: "请继续", session_id: "s2" });
  assert.match(document.querySelector("#messages").textContent, /请继续/);

  // 记录一条历史消息的 DOM 节点,流式 delta 之后它必须还是同一个节点
  // (keyed + memo:只重渲"正在生成的那一条")。
  const firstMsg = document.querySelector("#messages .msg");
  const mdBefore = document.querySelectorAll("#messages .m-text.md").length;
  const es = FakeEventSource.instances[0];
  assert.ok(es, "App 启动时连了 SSE");
  es.emit({ kind: "content_delta", session_id: "s2", content: "流" });
  await settle();
  es.emit({ kind: "content_delta", session_id: "s2", content: "流式输出" });
  await settle();

  assert.equal(document.querySelector("#messages .msg"), firstMsg, "历史消息节点没有被重建");
  const mds = Array.from(document.querySelectorAll("#messages .m-text.md"));
  assert.equal(mds.length, mdBefore + 1, "只多出一条正在生成的消息");
  assert.match(mds[mds.length - 1].textContent, /流式输出/);
  assert.equal(document.querySelectorAll("#messages .msg.ai .live-ind").length, 1,
    "只有正在生成的那一条带 live 指示器");
});

/* ================= 2) 路径转链接 + 点击 POST /api/fs/open ================= */
// 对应旧 Go 测试 TestUILinksChatPathsToTheOpener:绝对/相对路径与「文件:行号」
// 都要变成可点元素,点击把真实路径(行号已剥离)与当前会话工作目录发给服务端。
test("render: 聊天里的文件路径可点,点击 POST /api/fs/open(带 base)", async () => {
  const fetchMock = await resetAll({
    messages: [
      { role: "user", content: "看看这两个文件" },
      { role: "assistant", content: "改好了:D:\\proj\\src\\a.go 和 src/foo.go:12 都处理了" },
    ],
  });
  mountApp();
  await settle();
  // 打开第一个会话,历史消息才会被渲染进 #messages
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const links = Array.from(document.querySelectorAll("#messages .md-file"));
  assert.ok(links.length >= 2, "绝对路径与相对路径都渲染成可点链接, got " + links.length);
  const files = links.map((a) => a.dataset.file);
  assert.ok(files.includes("D:\\proj\\src\\a.go"), "绝对路径的 data-file 保留完整路径");
  assert.ok(files.includes("src/foo.go"), "带行号的路径:data-file 去掉 :12");
  assert.ok(links.some((a) => /foo\.go:12/.test(a.textContent)), "显示文字保留行号");

  // 点击绝对路径 → POST /api/fs/open,payload 带当前会话的 work_dir
  const abs = links.find((a) => a.dataset.file === "D:\\proj\\src\\a.go");
  abs.click();
  await settle();
  const opens = fetchMock.callsTo("/api/fs/open");
  assert.equal(opens.length, 1, "点击发了一次打开请求");
  assert.deepEqual(JSON.parse(opens[0].opts.body), { path: "D:\\proj\\src\\a.go", base: WORKDIR });
});

/* ================= 3) 注入 notice 渲染 ================= */
// 对应旧 Go 测试 TestUITranscriptRendersNotices:role=user 但带运行时 marker 的轮
// 必须画成"运行时提示",不能画成用户自己说的话。
test("render: 七类运行时注入的 user 轮渲染成 notice,不冒充用户", async () => {
  await resetAll({
    messages: [
      { role: "user", content: "真正的人类发言" },
      { role: "user", content: "__CTX_USAGE_80__ 上下文已用 80%" },
      { role: "user", content: "<agent id=\"a1\" state=\"completed\">子代理完成</agent>" },
      { role: "user", content: "<job id=\"j1\" state=\"completed\">后台命令完成</job>" },
      { role: "user", content: "__MC_PERSIST_FAILED__ 会话落盘失败" },
      { role: "user", content: "公约内容\n\n__MC_WORKER_NOTE__w-other:3" },
      { role: "user", content: "压缩指令 __CTX_COMPACT_REQUEST__" },
      { role: "user", content: "<previous-summary>旧摘要</previous-summary>" },
    ],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const notices = Array.from(document.querySelectorAll("#messages .msg.notice"));
  assert.equal(notices.length, 7, "七类 marker 各一条 notice");
  assert.equal(document.querySelectorAll("#messages .msg.user").length, 1, "只有真正的人类发言算用户消息");

  const text = notices.map((n) => n.textContent).join("\n");
  for (const label of ["上下文提醒", "子代理回执", "后台命令回执", "持久化告警", "工作区公约", "压缩指令", "压缩摘要"]) {
    assert.ok(text.includes(label), "notice 分类标签缺失: " + label);
  }
  // 可展开:details + 折叠全文,且 notice 行不带用户头像/「你」
  assert.equal(notices[0].querySelectorAll("details").length, 1);
  assert.ok(notices[0].querySelector("pre.notice-full").textContent.includes("上下文已用 80%"));
  for (const n of notices) {
    assert.equal(n.querySelector("[data-uav]"), null, "notice 不能带用户头像");
    assert.equal(n.textContent.includes("你"), false, "notice 不能冒充用户");
  }
});

test("render: 后端已标 notice 的轮优先级高于 marker 探测", async () => {
  await resetAll({
    messages: [{ role: "user", content: "任意文本", notice: "job" }],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  const n = document.querySelector("#messages .msg.notice");
  assert.ok(n, "带 notice 字段的轮画成 notice");
  assert.match(n.textContent, /后台命令回执/);
  assert.equal(document.querySelectorAll("#messages .msg.user").length, 0);
});

/* ================= 4) SSE 对账去重 ================= */
// 对应旧 Go 测试 ui_resync_test.go:断线重连后用事件账本回填"正在执行"的步骤,
// 同一个 call_id 的 tool 步骤只能出现一次(重放不能重复渲染/重复计数)。
test("render: resync 回填 + 重放 tool_end,步骤只出现一次", async () => {
  const events = {
    active: true,
    events: [
      { type: "tool/call", created_at: "2026-11-13T10:00:00.123456789Z", payload: { name: "exec", args: '{"command":"sleep 5"}' } },
      { type: "tool/result", call_id: "c1", created_at: "2026-11-13T10:00:05Z", payload: { name: "exec", result: "done" } },
    ],
  };
  await resetAll({
    // 历史里有一次已经发出的工具调用,结果在断流窗口里丢了 → 服务端账本里有。
    messages: [
      { role: "user", content: "跑一下" },
      { role: "assistant", content: "", tool_calls: [{ function: { name: "exec", arguments: '{"command":"sleep 5"}' } }] },
    ],
    events,
    sessions: [sessionA({ active: true })],
  });
  mountApp();
  await settle();

  // 打开这个仍在运行的会话 → 触发 tail_run 对账
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const steps = () => document.querySelectorAll("#messages .ms-step");
  assert.equal(steps().length, 1, "整个过程只有一步");
  assert.match(document.querySelector("#messages .m-steps summary").textContent, /1 步/);
  assert.match(document.querySelector("#messages .ms-step").textContent, /done/);

  // 重放同一批 SSE 事件 + 迟到的 tool_end:不能多出一步,也不能改结果。
  const es = FakeEventSource.instances[0];
  es.emit({ kind: "tool_end", session_id: "s1", tool_name: "exec", tool_result: "重复" });
  await settle();
  es.emit({ kind: "tool_end", session_id: "s1", tool_name: "exec", tool_result: "又重复" });
  await settle();

  assert.equal(steps().length, 1, "重放之后步骤数仍然是 1");
  assert.match(document.querySelector("#messages .m-steps summary").textContent, /1 步/);
});

/* ================= 6) background sub-agents light the stop button only ===== */
// Ported from the retired Go test TestComposerIgnoresBackgroundSubagents: the
// composer belongs to the session's OWN task. A background sub-agent may show
// the stop button, it must never take the input away.
test("render: background sub-agent shows stop but does not lock the composer", async () => {
  // childrenActive is derived from the children endpoint (a child row with
  // active:true), not from the session row, so the child list has to say so.
  await resetAll({
    sessions: [sessionA({ active: false, children_active: true })],
    extra: [[/^\/api\/sessions\/[^/]+\/children$/, () => jsonResponse([
      { session_id: "c1", name: "agent-1", role: "work", status: "running", active: true },
    ])]],
  });
  mountApp();
  await settle();

  document.querySelector("#sess-list .sess-item").click();
  await settle();

  assert.equal(getState().running, false, "the session's own task is not running");
  assert.equal(getState().childrenActive, true, "the sub-agent flag reached the store");

  const send = document.querySelector("#btn-send");
  const stop = document.querySelector("#btn-stop");
  assert.ok(send, "send button missing");
  assert.notEqual(send.style.display, "none", "background sub-agents must not hide the send button");
  assert.equal(stop.style.display, "inline-block", "the stop button must show while a sub-agent runs");

  // Typing and sending stay possible: the refusal path keys on state.running.
  const ta = document.querySelector("#input");
  ta.value = "still works";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  const fetchMock = globalThis.fetch;
  document.querySelector("#btn-send").click();
  await settle();
  assert.equal(fetchMock.callsTo("/api/messages").length, 1, "send must work while only a sub-agent runs");
});

/* ================= 7) 思考过程:淡色小框,live 展开、其余折叠,流式跟着长 ================= */
// reasoning_content 的展示回归测试。三条路(历史消息、执行步骤、正在生成的那条)
// 都走 js/components/ThinkBox.js。折叠是 2026-11 的显式设计:只有 live 那一个默认
// 展开,其余默认折叠(.collapsed 只藏框体,正文仍在 DOM)—— 但绝不允许再出现
// <details> 二次折叠或 display:none 把内容从 DOM 里抹掉。
test("render: reasoning 画成折叠小框,live 自动展开、历史折叠但内容仍在 DOM", async () => {
  await resetAll({
    messages: [
      { role: "user", content: "为什么" },
      { role: "assistant", content: "因为如此", reasoning_content: "先想想背景\n\n再给结论" },
    ],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const boxes = Array.from(document.querySelectorAll("#messages .think-box"));
  assert.equal(boxes.length, 1, "带 reasoning_content 的历史消息要画出思考框");
  const box = boxes[0];
  assert.equal(box.tagName, "DIV", "思考框是普通盒子,不是 details");
  assert.equal(box.querySelectorAll("details").length, 0, "不能再套一层折叠(折叠 = 没展示)");
  assert.equal(box.querySelector("summary"), null);
  assert.ok(box.classList.contains("collapsed"), "历史思考框默认折叠(一轮里只留 live 展开)");
  assert.match(box.textContent, /思考过程/);
  assert.match(box.textContent, /先想想背景/, "折叠只藏框体,正文仍在 DOM");
  assert.ok(box.querySelector(".think-box-body"), "正文装在可滚动的框体里");
  // 思考与正文是两块:正文照常渲染
  assert.match(document.querySelector("#messages .msg.ai .m-text.md").textContent, /因为如此/);

  // 流式:reasoning_delta 一到,正在生成的那条就出现**展开的**小框,并跟着增量继续长。
  const es = FakeEventSource.instances[0];
  es.emit({ kind: "reasoning_delta", session_id: "s1", reasoning: "让我先" });
  await settle();
  const liveSel = "#messages .msg.ai .think-box.live .think-box-body";
  assert.ok(document.querySelector(liveSel), "流式思考必须有可见小框(以前是 display:none)");
  assert.equal(document.querySelector("#messages .msg.ai .think-box.live").classList.contains("collapsed"), false, "live 框不折叠");
  assert.match(document.querySelector(liveSel).textContent, /让我先/);
  es.emit({ kind: "reasoning_delta", session_id: "s1", reasoning: "让我先算一下" });
  await settle();
  assert.equal(document.querySelectorAll("#messages .think-box.live").length, 1, "只有一条在想的框");
  assert.match(document.querySelector(liveSel).textContent, /让我先算一下/);

  // 正文开始后思考框不消失:两段都在同一条正在生成的消息里。
  es.emit({ kind: "content_delta", session_id: "s1", content: "结论是" });
  await settle();
  assert.match(document.querySelector(liveSel).textContent, /让我先算一下/);
  const liveCard = document.querySelector("#messages .think-box.live").closest(".m-card");
  assert.match(liveCard.querySelector(".m-text.md").textContent, /结论是/);
});

/* ================= 8) todo_write:参数本身就是一张清单 ================= */
// dsh 的 todo_write 收 todos=[{content,status}],status ∈ pending/in_progress/completed。
// 把这段 JSON 摊在展开区里等于什么都没画 —— 两边(「执行过程」折叠条里的历史步骤、
// 「正在执行」卡里的实时步骤)都得画成 .todo-card:三态图标 + 文本 + "3/5" 统计,默认展开。
test("render: todo_write 画成三态清单卡,带完成数统计,历史与实时都生效", async () => {
  const todos = [
    { content: "读 dsh_tools.json", status: "completed" },
    { content: "改 lib/tools.js 的参数表", status: "completed" },
    { content: "给 todo_write 补渲染测试", status: "completed" },
    { content: "跑 npm test 到全绿", status: "in_progress" },
    { content: "整理改动清单", status: "pending" },
  ];
  const args = JSON.stringify({ todos });
  await resetAll({
    messages: [
      { role: "user", content: "按计划改" },
      { role: "assistant", content: "", tool_calls: [{ function: { name: "todo_write", arguments: args } }] },
      { role: "tool", name: "todo_write", content: "已记录 5 项" },
      { role: "assistant", content: "计划已记录" },
    ],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const card = document.querySelector("#messages .todo-card");
  assert.ok(card, "todo_write 那一步要画成清单卡(不是 ms-args 里的 JSON)");
  assert.match(card.querySelector(".td-n").textContent, /3\/5/, "完成数统计");
  const items = Array.from(card.querySelectorAll(".todo-item"));
  assert.equal(items.length, 5, "五条待办");
  assert.deepEqual(items.map((li) => li.className.replace("todo-item ", "")),
    ["completed", "completed", "completed", "in_progress", "pending"]);
  assert.equal(items[0].querySelector(".td-ico").textContent, "✓", "完成打勾");
  assert.equal(items[3].querySelector(".td-ico").textContent, "●", "进行实心");
  assert.equal(items[4].querySelector(".td-ico").textContent, "○", "待办空心");
  assert.match(items[3].querySelector(".td-text").textContent, /跑 npm test 到全绿/);
  assert.match(card.textContent, /待办清单/);

  // 默认展开 + 折叠状态下也看得见进度
  const step = card.closest("details.ms-step");
  assert.ok(step, "清单长在待办那一步的展开区里");
  assert.equal(step.open, true, "清单卡默认展开(它是这一步在说什么,不是参数细节)");
  assert.match(step.querySelector("summary .ms-todo").textContent, /3\/5/, "标题行也报进度");
  assert.equal(step.querySelector(".ms-args"), null, "清单卡顶掉了原始 JSON 参数");

  // 实时:tool_start 一到,「正在执行」卡里就是清单,不用等它跑完
  const es = FakeEventSource.instances[0];
  es.emit({
    kind: "tool_start", session_id: "s1", tool_name: "todo_write",
    tool_args: JSON.stringify({ todos: [{ content: "读 dsh_tools.json", status: "completed" }, { content: "改 Steps.js", status: "in_progress" }] }),
  });
  await settle();
  const live = document.querySelector("#messages .ms-live .todo-card");
  assert.ok(live, "正在执行的那一步也画成清单");
  assert.match(live.querySelector(".td-n").textContent, /1\/2/);
  assert.match(live.querySelector(".td-now").textContent, /进行中 1/);
});

/* ================= 9) pwsh:dsh 的 description 与 timeoutMs(毫秒) ================= */
// 命令工具改名成 pwsh、超时改名成 timeoutMs(毫秒)以后,步骤卡上有两件事得跟着变:
// 标题行显示模型写的那句 description(dsh 明说它是"给 UI 显示的"),计时那栏按毫秒算
// —— 模型没写时执行器套它自己的默认值 120000ms、上限 600000ms,别报一个跑不到的上限。
test("render: pwsh 步骤显示 description,计时上限按 timeoutMs 毫秒读(缺省 120s、封顶 600s)", async () => {
  await resetAll();
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  const es = FakeEventSource.instances[0];

  es.emit({
    kind: "tool_start", session_id: "s1", tool_name: "pwsh",
    tool_args: '{"description":"跑前端测试","command":"npm test","timeoutMs":300000}',
  });
  await settle();
  const step = () => Array.from(document.querySelectorAll("#messages .ms-live .ms-step"));
  assert.equal(step().length, 1);
  assert.match(step()[0].querySelector(".t-name").textContent, /pwsh/);
  assert.equal(step()[0].querySelector(".ms-arg1").textContent, "跑前端测试",
    "标题行是 description,不是整段命令行");
  assert.match(step()[0].querySelector(".ms-run").textContent, /执行 \d+s \/ 最长 5m/, "300000ms = 300s");
  assert.match(step()[0].querySelector(".ms-out").textContent, /command: npm test/, "命令全文在展开区");

  // 没写 timeoutMs:报执行器的默认上限 120000ms,而不是什么都不显示
  es.emit({
    kind: "tool_start", session_id: "s1", tool_name: "pwsh",
    tool_args: '{"description":"列出目录","command":"dir /b"}',
  });
  // 超过上限的:执行器会压到 600000ms,界面跟着说 10m
  es.emit({
    kind: "tool_start", session_id: "s1", tool_name: "pwsh",
    tool_args: '{"description":"跑一次完整构建","command":"npm run build","timeoutMs":900000}',
  });
  await settle();
  const runs = step().map((s) => s.querySelector(".ms-run").textContent);
  assert.equal(runs.length, 3);
  assert.match(runs[1], /最长 2m/, "默认 120000ms");
  assert.match(runs[2], /最长 10m/, "封顶 600000ms");
});

/* ================= 10) goal 工具:一行状态文案,不做大面板 ================= */
test("render: create_goal / update_goal 步骤各给一行状态文案", async () => {
  await resetAll({
    messages: [
      { role: "user", content: "长期目标" },
      { role: "assistant", content: "", tool_calls: [
        { function: { name: "create_goal", arguments: '{"objective":"把前端适配到 dsh 工具面"}' } },
        { function: { name: "update_goal", arguments: '{"goal_id":"g1","revision":2,"action":"complete"}' } },
      ] },
      { role: "tool", name: "create_goal", content: "goal g1 已创建" },
      { role: "tool", name: "update_goal", content: "goal g1 已完成" },
      { role: "assistant", content: "收工" },
    ],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const lines = Array.from(document.querySelectorAll("#messages .goal-line"));
  assert.equal(lines.length, 2, "两个 goal 步骤各一行");
  assert.match(lines[0].textContent, /新建目标[：:]\s*把前端适配到 dsh 工具面/);
  assert.match(lines[1].textContent, /标记完成/);
  assert.equal(document.querySelectorAll("#messages .goal-panel").length, 0, "就一行字,别做面板");
});

/* ================= v4 底栏三件套 + 上下文管理抽屉 + 摘要抽屉 ================= */
const CTX_SNAPSHOT = {
  window: 100000, tokens: 12345, pct: 12,
  sections: [
    { key: "system", label: "系统提示词", tokens: 5000 },
    { key: "tools", label: "工具定义", tokens: 3000 },
    { key: "user", label: "用户消息", tokens: 1000 },
    { key: "assistant", label: "助手回复", tokens: 2000 },
    { key: "tool", label: "工具结果", tokens: 1345 },
  ],
  totals: { requests: 6, input: 1500, output: 70, cached: 800, uncached: 700, reported: true },
  stats: { model_ms: 3262000, tool_ms: 4132000, ttft_avg_ms: 1800 },
  steps: 6,
  rounds: [
    { round: 1, steps: 3, first_turn: 1, last_turn: 1, peak_tokens: 8000, reclaimed: 1200, compacted: true, at: "2026-01-01T10:00:00Z" },
    { round: 2, steps: 2, first_turn: 2, last_turn: 2, peak_tokens: 9000, reclaimed: 0, compacted: true, at: "2026-01-01T10:30:00Z" },
    { round: 3, steps: 1, first_turn: 3, last_turn: 3, peak_tokens: 11000, reclaimed: 0, compacted: false, at: "" },
  ],
  events: [{ kind: "compact", round: 1, turn: 1, tokens: 1200, detail: "auto压缩:2.0K → 0.8K tokens", at: "2026-01-01T10:00:00Z" }],
};
const CTX_DETAIL = {
  system: {
    part: "system", text: "你是编程智能体 SYSTEM 全文", note: "",
    parts: [
      { label: "人设", injected: false, tokens: 2000, text: "你是编程智能体 SYSTEM 全文" },
      { label: "插件记忆", injected: true, tokens: 300, text: "记忆内容 INJECTED" },
    ],
  },
  tools: { part: "tools", tools: [{ name: "pwsh", description: "执行命令", tokens: 900 }], total: 900 },
  message: { part: "message", index: 15, role: "user", tokens: 100, text: "完整的用户消息全文" },
};
// 消息详情分页 mock:16 条消息默认每页 4 条,role 过滤在"服务端"。
function detailPage(role, before) {
  const all = [];
  for (let i = 0; i < 16; i++) {
    all.push({ index: i, role: ["user", "assistant", "tool"][i % 3], tokens: 100 + i,
      chars: 200, preview: "消息 " + i, est: i >= 14 });
  }
  const end = before == null ? 16 : before;
  const start = Math.max(0, end - 4);
  const page = all.slice(start, end);
  const msgs = role ? page.filter((m) => m.role === role) : page;
  return { part: "messages", total: 16, returned: page.length, has_more: start > 0, role: role || "", messages: msgs };
}

async function mountWithCtx(extraCtx) {
  const ctxRoute = [/\/context$/, () => jsonResponse(extraCtx || CTX_SNAPSHOT)];
  const detailRoute = [/\/context\/detail/, (url) => {
    const u = new URL(String(url), "http://test");
    const part = u.searchParams.get("part");
    if (part === "messages") {
      if (u.searchParams.has("i")) return jsonResponse(CTX_DETAIL.message);
      const before = u.searchParams.get("before");
      return jsonResponse(detailPage(u.searchParams.get("role"), before == null ? null : Number(before)));
    }
    return jsonResponse(CTX_DETAIL[part] || { part: "?" });
  }];
  const fetchMock = await resetAll({ extra: [ctxRoute, detailRoute] });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  return fetchMock;
}

test("render: 输入区 v3.4 = 占用行居中:胶囊+ ⓘ 会话统计浮层都在输入卡下方", async () => {
  await mountWithCtx();
  const meter = document.querySelector("#token-usage");
  assert.ok(meter, "占用仪表还在(id 不变)");
  assert.match(meter.textContent, /\d+%/, "占用百分比仍在");
  assert.ok(!/预估约/.test(meter.textContent), "v3.4:「预估约」字样已删");
  assert.ok(meter.querySelector("#ctx-total"), "v3.4:会话统计胶囊挪到输入卡下方一行");
  assert.equal(document.querySelector("#btn-compact"), null, "手动压缩按钮已移除");
  assert.equal(document.querySelector("#foot-hint"), null, "tips 行已移除");
  assert.equal(document.querySelector(".meter-pop, .meter-chip"), null, "v2 弹层/芯片整体退役");

  const pill = document.querySelector("#ctx-total");
  assert.match(pill.textContent, /tok/, "token 胶囊只剩体量 N tok");
  assert.ok(!/\//.test(pill.textContent), "v3.9:胶囊不挂「/ 窗口上限」,只报本会话消耗的上下文总量");
  assert.ok(!/预估约/.test(pill.textContent), "占用% 不挤在胶囊里,归同行左邻");

  const stats = document.querySelector("#sess-stats");
  stats.click();
  await settle();
  const pop = document.querySelector("#sess-pop");
  assert.ok(pop, "点 ⓘ 弹会话统计");
  assert.match(pop.textContent, /执行轮数\s*3/);
  assert.match(pop.textContent, /执行步数\s*6/);
  assert.match(pop.textContent, /模型用时\s*54分22秒/);
  assert.match(pop.textContent, /工具调用用时\s*68分52秒/);
  assert.match(pop.textContent, /首 token 平均\(TTFT\)\s*1\.8秒/);
  stats.click();
  await settle();
  assert.equal(document.querySelector("#sess-pop"), null, "再点收起");
});

test("render: token 胶囊直接打开管理抽屉;摘要入口在抽屉头,与上下文互斥", async () => {
  const fetchMock = await mountWithCtx();
  // v3:聊天头只留四颗,「摘要」搬进上下文抽屉头(两颗文字钮)。
  document.querySelector("#btn-rail").click();
  await settle();
  assert.ok(document.querySelector("#ctx-panel.open"), "上下文先开着");
  document.querySelector("#rail-sum").click();
  await settle();
  assert.ok(document.querySelector("#summary-panel.open"), "摘要从抽屉头打开");
  assert.ok(!document.querySelector("#ctx-panel.open"), "开摘要必关上下文");

  document.querySelector("#ctx-total").click();
  await settle();
  assert.ok(document.querySelector("#ctx-panel.open"), "token 胶囊一点就开上下文抽屉");
  assert.ok(!document.querySelector("#summary-panel.open"), "开上下文必关摘要");
  assert.ok(fetchMock.callsTo("/context").length >= 1, "拉了快照");
});

test("render: 「上下文」抽屉含组成/累计/轮次审计/事件,轮次可过滤事件", async () => {
  const fetchMock = await mountWithCtx();
  const panel = document.querySelector("#ctx-panel");
  assert.ok(panel, "抽屉常驻 DOM(滑动开合)");
  assert.ok(!panel.classList.contains("open"), "默认收起");

  document.querySelector("#btn-rail").click();
  await settle();
  assert.ok(document.querySelector("#ctx-panel.open"), "点击滑入");
  const body = document.querySelector("#ctx-panel .ctx-body").textContent;
  assert.match(body, /系统提示词/);
  assert.match(body, /工具定义/);
  assert.match(body, /总上下文/, "累计小节");
  assert.match(body, /命中率/);
  assert.match(body, /3 轮 · 6 步/, "轮次审计汇总");
  assert.match(body, /第3轮[\s\S]*进行中/, "未压缩的轮标进行中");
  assert.match(body, /第2轮[\s\S]*已压缩(?! −)/, "回收为 0 的压缩轮写「已压缩」不写 −0K");
  assert.match(body, /第1轮[\s\S]*压缩 −1\.2K/, "轮里能看回收量");
  assert.ok(document.querySelectorAll("#ctx-panel .ctx-stack .cs-seg").length >= 5, "五段堆叠条");

  // 点第 3 轮(最新,列表第一行)→ 事件按 turn 范围过滤(turn=1 的那条被滤掉)。
  document.querySelector("#ctx-panel .ctx-rounds li.cr-row").click();
  await settle();
  assert.match(document.querySelector("#ctx-panel .ctx-body").textContent, /这一轮没有/, "过滤生效");
  document.querySelector("#ctx-panel .rs-filter").click();
  await settle();
  assert.match(document.querySelector("#ctx-panel .ctx-body").textContent, /auto压缩/, "过滤可清除");

  document.querySelector("#ctx-close").click();
  await settle();
  assert.ok(!document.querySelector("#ctx-panel.open"), "再点收起");
});

test("render: 组成段详情 —— 系统提示词分段(基础/注入) / 工具定义 / 消息分页与全文", async () => {
  const fetchMock = await mountWithCtx();
  document.querySelector("#btn-rail").click();
  await settle();

  const rows = document.querySelectorAll("#ctx-panel .ctx-legend li.cl-row");
  assert.equal(rows.length, 5, "五段图例都可点");

  rows[0].click(); // 系统提示词:基础 vs 注入分段
  await settle();
  let det = document.querySelector("#ctx-detail").textContent;
  assert.match(det, /人设/, "基础段");
  assert.match(det, /插件记忆/, "注入段");
  assert.match(det, /系统/, "「系统」标");
  assert.match(det, /注入/, "「注入」标");
  assert.ok(fetchMock.callsTo("/context/detail").length >= 1);
  const part = document.querySelector("#ctx-detail .cd-part summary");
  part.click(); // 展开一段看正文
  await settle();
  assert.match(document.querySelector("#ctx-detail").textContent, /SYSTEM 全文/);

  rows[1].click(); // 工具定义
  await settle();
  assert.match(document.querySelector("#ctx-detail").textContent, /pwsh/);
  assert.match(document.querySelector("#ctx-detail").textContent, /执行命令/);
  assert.doesNotMatch(document.querySelector("#ctx-detail").textContent, /插件记忆/, "同一时间只展开一段");

  // 用户消息:服务端按 role 分页,首屏一页 + 「加载更早」;超长条带 ≈ 估算标。
  rows[2].click();
  await settle();
  let msgs = document.querySelectorAll("#ctx-detail .cd-msg");
  assert.equal(msgs.length, 2, "首屏一页(user 角色过滤后 2 条)");
  assert.match(document.querySelector("#ctx-detail").textContent, /消息 15/);
  assert.match(document.querySelector("#ctx-detail .cm-n").textContent, /≈/, "估算条带 ≈");
  const more = document.querySelector("#cd-more");
  assert.ok(more, "还有更早的分页入口");
  more.click();
  await settle();
  msgs = document.querySelectorAll("#ctx-detail .cd-msg");
  assert.equal(msgs.length, 3, "加载更早后 3 条");
  assert.match(document.querySelector("#ctx-detail").textContent, /消息 9/);

  document.querySelector("#ctx-detail .cd-msg").click();
  await settle();
  assert.match(document.querySelector("#ctx-detail").textContent, /完整的用户消息全文/, "点预览出全文");

  rows[2].click(); // 再点同一行收起
  await settle();
  assert.equal(document.querySelector("#ctx-detail"), null, "详情可收起");
});

test("render: totals 为 null 时抽屉显示空态而不是 0", async () => {
  await mountWithCtx(Object.assign({}, CTX_SNAPSHOT, { totals: null, events: [], rounds: [], steps: 0 }));
  document.querySelector("#btn-rail").click();
  await settle();
  const body = document.querySelector("#ctx-panel .ctx-body").textContent;
  assert.match(body, /还没有/, "无用量数据走空态文案");
  assert.doesNotMatch(body, /命中率/);
  assert.match(body, /仍在第 1 轮/, "没有轮次就说还在第 1 轮");
});

test("render: 「摘要」抽屉(入口=上下文抽屉头)= 执行结果/相关文件/性能指标", async () => {
  await mountWithCtx();
  document.querySelector("#btn-rail").click();
  await settle();
  assert.ok(document.querySelector("#rail-sum"), "抽屉头有摘要入口(v3)");
  document.querySelector("#rail-sum").click();
  await settle();
  const panel = document.querySelector("#summary-panel");
  assert.ok(panel && panel.classList.contains("open"), "摘要抽屉滑入");
  const body = panel.textContent;
  assert.match(body, /执行结果/, "待办清单小节(agent 的目标在这)");
  assert.match(body, /性能指标/);
  assert.match(body, /缓存命中/);
  document.querySelector("#sum-close").click();
  await settle();
  assert.ok(!document.querySelector("#summary-panel.open"), "× 收起摘要");
});

test("render: 聊天头右上角恰好四颗按钮(子代理/后台/摘要/上下文),文案精简", async () => {
  await mountWithCtx();
  const ids = ["btn-tasks", "btn-jobs", "btn-summary", "btn-rail"];
  for (const id of ids) {
    const b = document.querySelector("#" + id);
    assert.ok(b, id + " 常驻可点");
    assert.ok(b.classList.contains("show"), id + " 默认可见");
  }
  assert.equal(document.querySelector("#btn-sort"), null, "v3.1:排序按钮已移除");
  const headText = document.querySelector(".chat-head").textContent;
  assert.doesNotMatch(headText, /个子智能体|个后台任务/, "长文案已精简");
});

test("render: 聊天头「摘要」按钮开摘要抽屉,与上下文互斥", async () => {
  await mountWithCtx();
  document.querySelector("#btn-rail").click(); // 先开上下文
  await settle();
  assert.ok(document.querySelector("#ctx-panel.open"), "上下文先开着");
  document.querySelector("#btn-summary").click();
  await settle();
  assert.ok(document.querySelector("#summary-panel.open"), "点摘要开摘要抽屉");
  assert.ok(!document.querySelector("#ctx-panel.open"), "开摘要必关上下文");
  assert.ok(document.querySelector("#btn-summary").classList.contains("on"), "摘要按钮亮着");
  document.querySelector("#btn-summary").click();
  await settle();
  assert.ok(!document.querySelector("#summary-panel.open"), "再点收起摘要");
});

test("render: 聊天区用户头像回来了,点头像开「用户头像」弹窗", async () => {
  await mountWithCtx();
  const userMsg = document.querySelector(".msg.user");
  assert.ok(userMsg, "有用户消息");
  const ava = userMsg.querySelector("[data-uav]");
  assert.ok(ava, "用户消息右侧带头像(未设置时是「你」字回退)");
  // .msg.user 是 row-reverse:第一个 DOM 子元素才贴右。头像必须在卡片之前。
  assert.equal(userMsg.firstElementChild, ava, "头像是行首元素(row-reverse 下贴右)");
  ava.click();
  await settle();
  assert.ok(document.querySelector("#user-avatar-mask.open"), "点击开头像弹窗");
});

/* ================= v5 轨迹抽屉(TrajPanel) ================= */
const TRAJ_PAGE = {
  steps: 6, rounds: 2, has_more: true,
  rows: [
    { seq: 11, kind: "task", turn: 1, round: 1, title: "任务: 跑一下测试", sub: "", tokens: 0, dur_ms: 0, at: "2026-01-01T10:00:00Z" },
    { seq: 12, kind: "assistant", turn: 1, round: 1, title: "好的,先看 package", sub: "先确认脚本", tokens: 210, dur_ms: 1800, at: "2026-01-01T10:00:05Z" },
    { seq: 13, kind: "tool", turn: 1, round: 1, title: 'pwsh {"command":"npm test"}', sub: "输出: OK 59 tests", tokens: 900, dur_ms: 4132000, at: "2026-01-01T10:01:05Z" },
    { seq: 14, kind: "compact", turn: 1, round: 1, title: "auto压缩 205.2K → 13.4K tokens", sub: "", tokens: 191800, dur_ms: 0, at: "2026-01-01T10:02:00Z" },
    { seq: 15, kind: "assistant", turn: 2, round: 2, title: "测试全绿", sub: "npm test 通过", tokens: 150, dur_ms: 2400, at: "2026-01-01T10:03:00Z" },
    { seq: 16, kind: "tool", turn: 2, round: 2, title: 'read {"file_path":"x.log"}', sub: "log 内容摘要", tokens: 80, dur_ms: 300, at: "2026-01-01T10:03:10Z" },
  ],
};
const TRAJ_OLD = {
  steps: 2, rounds: 1, has_more: false,
  rows: [{ seq: 5, kind: "task", turn: 1, round: 1, title: "更早的任务", sub: "", tokens: 0, dur_ms: 0, at: "2026-01-01T09:00:00Z" }],
};

test("render: 抽屉头「轨迹」打开轨迹抽屉:时间线+事件流+互斥", async () => {
  const trajRoute = [/\/trajectory/, (url) => {
    const u = new URL(String(url), "http://test");
    return jsonResponse(u.searchParams.has("before") ? TRAJ_OLD : TRAJ_PAGE);
  }];
  await resetAll({ extra: [[/\/context$/, () => jsonResponse(CTX_SNAPSHOT)], trajRoute] });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  assert.ok(document.querySelector("header #btn-traj"), "v3.1:轨迹按钮回归顶栏");
  document.querySelector("#btn-rail").click(); // 先开上下文
  await settle();
  const btn = document.querySelector("#rail-traj");
  assert.ok(btn, "上下文抽屉头也保留轨迹入口");

  btn.click();
  await settle();
  const panel = document.querySelector("#traj-panel");
  assert.ok(panel && panel.classList.contains("open"), "点抽屉头按钮滑出轨迹抽屉");
  assert.match(panel.textContent, /6 步 · 2 轮/, "头部汇总");
  const blks = panel.querySelectorAll(".tr-tl .tr-blk");
  assert.ok(blks.length >= 5, "时间线小块");
  const rowEls = panel.querySelectorAll(".tr-rows .tr-row");
  assert.equal(rowEls.length, 6, "六行轨迹");
  assert.match(rowEls[0].textContent, /工具/, "最新在前(read)");
  assert.match(panel.textContent, /auto压缩 205\.2K → 13\.4K/, "压缩行原文");

  // 展开一行看详情 sub。
  const toolRow = [...rowEls].find((r) => r.className.includes("k-tool"));
  toolRow.querySelector(".tr-main").click();
  await settle();
  assert.match(toolRow.textContent, /log 内容摘要/, "点行展开 sub 详情");

  // 过滤 + 搜索(纯客户端)。
  const chips = panel.querySelectorAll(".tr-chip");
  chips[3].click(); // 工具
  await settle();
  assert.equal(panel.querySelectorAll(".tr-rows .tr-row").length, 2, "只看工具两行");
  chips[0].click(); // 全部
  await settle();
  const search = panel.querySelector(".tr-search");
  search.value = "pwsh";
  search.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  assert.equal(panel.querySelectorAll(".tr-rows .tr-row").length, 1, "搜索 pwsh 只剩一行");

  // 与上下文抽屉互斥。
  document.querySelector("#btn-rail").click();
  await settle();
  assert.ok(!document.querySelector("#traj-panel.open"), "开上下文必关轨迹");

  // 清除过滤后重开抽屉翻更早。
  search.value = "";
  search.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  document.querySelector("#rail-traj").click();
  await settle();
  const more = document.querySelector("#traj-more");
  assert.ok(more, "has_more → 加载更早入口");
  more.click();
  await settle();
  assert.equal(document.querySelectorAll("#traj-panel .tr-rows .tr-row").length, 7, "并入更早一行");
  assert.equal(document.querySelector("#traj-more"), null, "更早到底后入口消失");
});

/* ================= 13) 点会话必须落到最新一条 ================= */
// 回归:上滑回看过历史(stick 解锁)之后点会话,视口停在旧滚动位置不跳底。
// 病根是切换 effect 用 useEffect(rAF 才跑)而本地 fetch 早于它带回 rows;
// 现在切换 effect 改 useLayoutEffect,并由 jumpTick 覆盖"重点击当前会话"。
test("render: 上滑后点会话(含重点击当前会话)跳回最新内容", async () => {
  await resetAll();
  mountApp();
  await settle();

  // jsdom 没有布局,scrollHeight/clientHeight 恒为 0 → 任何位置都算"在底部",
  // 必须伪造几何才测得出"上滑解锁"和"跳底"。
  const box = document.querySelector("#messages");
  Object.defineProperty(box, "scrollHeight", { value: 5000, configurable: true });
  Object.defineProperty(box, "clientHeight", { value: 600, configurable: true });

  // 打开会话 A,滚到底(打开时本就该在底部)。
  document.querySelectorAll("#sess-list .sess-item")[0].click();
  await settle();
  assert.equal(box.scrollTop, 5000, "刚打开会话就在最新一条");

  // 用户上滑回看 → 吸底解锁,追加内容不该抢视角。
  box.scrollTop = 500;
  box.dispatchEvent(new window.Event("scroll"));
  await settle();
  assert.equal(box.scrollTop, 500, "上滑后没有被迫跳走");

  // 点另一个会话:必须跳到底部(旧 bug:停在 500)。
  document.querySelectorAll("#sess-list .sess-item")[1].click();
  await settle();
  assert.equal(box.scrollTop, 5000, "切换会话后跳到最新");

  // 再上滑,然后重击"当前会话":current 没变,靠 jumpTick 也要跳底。
  box.scrollTop = 500;
  box.dispatchEvent(new window.Event("scroll"));
  await settle();
  document.querySelectorAll("#sess-list .sess-item")[1].click();
  await settle();
  assert.equal(box.scrollTop, 5000, "重点击当前会话同样跳到最新");
});

/* ================= 14) 运行中插话 ================= */
// 打字即解锁:运行中的「停止」让位给「插话」,发送走注入(POST 带 session_id,
// 后端回 mode:"steer"),消息立刻出现在聊天流里;清空输入后按钮换回「停止」。
test("render: 运行中打字把停止换成插话,点击注入运行中的任务", async () => {
  const fetchMock = await resetAll({
    extra: [[/^\/api\/messages$/, () => jsonResponse({ mode: "steer", session_id: "s1" }, 202, "Accepted")]],
  });
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  // 任务开跑、输入为空:只有「停止」。
  setState({ running: true });
  await settle();
  assert.match(document.querySelector("#btn-send").style.display, /none/, "运行中空输入没有发送键");
  assert.equal(document.querySelector("#btn-stop").style.display, "inline-block", "运行中显示停止");

  // 打字 → 发送键回来且写着「插话」,停止让位。
  const ta = document.querySelector("#input");
  ta.value = "等等,先别删那个文件";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  assert.doesNotMatch(document.querySelector("#btn-send").style.display, /none/, "打字后发送键出现");
  assert.match(document.querySelector("#btn-send").textContent, /插话/, "运行中标签是插话");
  assert.match(document.querySelector("#btn-stop").style.display, /none/, "有草稿时停止让位");

  // 点发送 → POST 带 session_id,聊天流立刻出现这句话,toast 提示注入成功。
  document.querySelector("#btn-send").click();
  await settle();
  const posts = fetchMock.callsTo("/api/messages");
  assert.equal(posts.length, 1, "发了一次插话");
  assert.deepEqual(JSON.parse(posts[0].opts.body), { content: "等等,先别删那个文件", session_id: "s1" });
  assert.match(document.querySelector("#messages").textContent, /等等,先别删那个文件/, "立刻回显在聊天流");
  assert.match(JSON.stringify(getState().toast), /插话/, "toast 说明注入成功");

  // 输入清空 → 又只剩「停止」。
  await settle();
  assert.match(document.querySelector("#btn-send").style.display, /none/, "清空后发送键收起");
  assert.equal(document.querySelector("#btn-stop").style.display, "inline-block", "清空后恢复停止");
});

/* ================= v3 样图版:顶栏芯片 / 欢迎页 / 选择模式 ================= */
test("render: 新会话欢迎页 = Hero + 示例卡,点示例塞进输入框", async () => {
  await resetAll();
  mountApp();
  await settle();
  const hero = document.querySelector(".placeholder.hero");
  assert.ok(hero, "新会话画欢迎页 Hero");
  assert.match(hero.textContent, /你好，我是 Mini Code/);
  assert.match(hero.textContent, /写一段代码/);
  assert.ok(document.querySelector("#btn-more-ex"), "第四颗是「更多示例」");
  const card = hero.querySelectorAll(".ex-card")[1];
  card.click();
  await settle();
  assert.match(document.querySelector("#input").value, /通读这个项目/, "点示例卡填入提示词");
});

test("render: 顶栏 = 模型芯片(点+名+窗口) / 轨迹开关;无模式、无改进、无思考芯片", async () => {
  await resetAll();
  mountApp();
  await settle();
  const chip = document.querySelector("#status-pill");
  assert.ok(chip.classList.contains("model-chip"), "模型芯片样式");
  assert.match(chip.textContent, /m1/);
  assert.match(chip.textContent, /100K/, "上下文窗口徽标");
  assert.equal(document.querySelector("header #btn-mode"), null, "权限模式按钮已不在顶栏");
  assert.equal(document.querySelector("#btn-improve"), null, "「改进」已按需求下线");
  assert.equal(document.querySelector("#btn-think-chip"), null, "深度思考不再有顶栏入口");

  // 轨迹回到顶栏:点开/再点收。
  const traj = document.querySelector("header #btn-traj");
  assert.ok(traj, "轨迹按钮回到顶栏");
  traj.click();
  await settle();
  assert.equal(getState().trajOpen, true, "点顶栏轨迹开抽屉");
  document.querySelector("header #btn-traj").click();
  await settle();
  assert.equal(getState().trajOpen, false, "再点收起");
});

test("render: 深度思考入口 = 输入区思考胶囊(点开强度浮层)", async () => {
  await resetAll();
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  await new Promise((r) => setTimeout(r, 30)); // 等 loadSession 尾巴,别拿旧快照比
  const pill = document.querySelector("#think-pill");
  assert.ok(pill, "思考胶囊在输入区");
  pill.click();
  await settle();
  const pop = document.querySelector("#think-pop");
  assert.ok(pop && pop.classList.contains("open"), "点开强度滑条浮层");
  assert.ok(document.querySelector("#tp-effort"), "浮层里有强度滑条");
});

test("render: 输入区「选择模式」下拉切换完整/谨慎,走 POST /mode", async () => {
  await resetAll({ extra: [[/\/mode$/, (u, o) => jsonResponse({ mode: JSON.parse(o.body).mode, auto_compressed: false, summary: null })]] });
  const fetchMock = globalThis.fetch;
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  document.querySelector("#btn-mode").click();
  await settle();
  assert.ok(document.querySelector("#mode-menu"), "模式下拉弹出");
  document.querySelectorAll("#mode-menu .mdm-item")[1].click(); // 谨慎
  await settle();
  const post = fetchMock.calls.find((c) => /\/mode$/.test(c[0] || c.url));
  assert.ok(post, "POST /mode");
  assert.equal(JSON.parse(post.opts.body).mode, "cautious");
  assert.equal(getState().mode, "cautious");
});

test("render: 新会话发第一条消息接管后,store 的字段一个都不许丢", async () => {
  // 后端先 emit session_created、HTTP 响应后到 —— 正是这条竞态把新会话"接管"到
  // adoptNewSession 里。它曾把整个 store 换成七个字段(boot/models 全没),
  // 于是发送后满屏 "Cannot read properties of undefined (reading 'play'/'find')"。
  const slowMessages = [[/^\/api\/messages$/, async () => {
    await new Promise((r) => setTimeout(r, 400));
    return jsonResponse({ task_id: "t1", session_id: "s-new" }, 202, "Accepted");
  }]];
  const fetchMock = await resetAll({ sessions: [], extra: slowMessages });
  mountApp();
  await settle();
  assert.equal(getState().current, null, "起始是新会话草稿");

  const ta = document.querySelector("#input");
  ta.value = "新会话的第一句话";
  ta.dispatchEvent(new window.Event("input", { bubbles: true }));
  await settle();
  document.querySelector("#btn-send").click();
  await new Promise((r) => setTimeout(r, 20)); // 只要 POST 在路上,别等它落地

  const es = FakeEventSource.instances[0];
  assert.ok(es, "App 启动时连了 SSE");
  es.emit({ kind: "status", status: "session_created", session_id: "s-new", task_id: "t1" });
  await settle();
  await new Promise((r) => setTimeout(r, 500)); // 迟到的 POST 落地 → adoptNewSession
  await settle();

  const st = getState();
  assert.equal(st.current, "s-new", "迟到的响应按 task_id 接管了新会话");
  for (const k of Object.keys(initialState)) {
    assert.ok(k in st && st[k] !== undefined, "接管后字段还在: " + k);
  }
  assert.ok(Array.isArray(st.sessions), "sessions 还是数组");
  // 界面还得活着:这两处正是当年最先炸掉的两块 —— 输入区的模型芯片读
  // state.models,设置弹窗里的开机动画摘要读 state.boot.play。
  assert.ok(document.querySelector("#model-name"), "输入区还画得出来");
  assert.ok(st.models.length >= 1, "模型列表还在");
  assert.ok(document.querySelector("#a-boot-summary"), "开机动画摘要还画得出来");
  assert.ok(document.querySelector("#think-pill"), "思考胶囊还在");
  assert.ok(fetchMock.callsTo("/api/messages").length >= 1, "发了一次消息");
});

test("render: 输出速度显示在输入卡下方一行(有速度才出现,没有就不占位)", async () => {
  await resetAll();
  mountApp();
  await settle();
  setState({ speed: 42.34, speedAccurate: true });
  await settle();
  assert.match(document.querySelector("#token-usage").textContent, /42\.3 tok\/s/, "速度落在占用一行");
  assert.ok(!/tok\/s/.test(document.querySelector("#token-pill-text").textContent), "胶囊里不再混速度");
  setState({ speed: 0 });
  await settle();
  assert.ok(!/tok\/s/.test(document.querySelector("#token-usage").textContent), "速度归零后撤掉");
});

/* ================= 24) 输出速度按会话隔离(2026-11 用户:统计会串到别的会话) ================= */
// 只有当前会话的 content_delta 能喂估算窗口,只有当前会话在跑才写表头。
// 两条 delta 之间真的等 700ms:窗口要 dt>0.5s 才吐数,不然这条用例改坏也照样绿。
test("render: 别的会话在流式输出,不许把它的 tok/s 记到当前会话头上", async () => {
  await resetAll();
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click(); // 会话 A(s1)
  await settle();
  setState({ running: true });
  const es = FakeEventSource.instances[0];

  es.emit({ kind: "content_delta", session_id: "s2", content: "x".repeat(90) });
  await new Promise((r) => setTimeout(r, 700));
  es.emit({ kind: "content_delta", session_id: "s2", content: "x".repeat(900) });
  await settle();
  assert.equal(getState().speed, 0, "s2 在生成,s1 的速度一个字都不该动");

  // 换成自己的会话:同样的节奏就必须算出来(证明上面那条不是"永远不动")。
  es.emit({ kind: "content_delta", session_id: "s1", content: "y".repeat(60) });
  await new Promise((r) => setTimeout(r, 700));
  es.emit({ kind: "content_delta", session_id: "s1", content: "y".repeat(600) });
  await settle();
  assert.ok(getState().speed > 0, "当前会话自己的增量照常估算");

  // 别的会话收尾:既不写速度,也不许把展示值抹成别人的实测值。
  const before = getState().speed;
  setState({ speed: 33.3, speedAccurate: true });
  es.emit({ kind: "assistant", session_id: "s2", content: "B 说完了" });
  es.emit({ kind: "usage", session_id: "s2", prompt_tokens: 10, cached_tokens: 5, cache_reported: true, speed: 99 });
  await settle();
  assert.equal(getState().speed, 33.3, "s2 的 usage/收尾不许改 s1 的展示值");
  assert.ok(before > 0);
});

/* ================= 25) 并行执行时「正在执行」卡默认收起 ================= */
// collapsed 必须落在带 .ms-live 的那张卡上 —— CSS 规则写的是
// .ms-live.collapsed .ms-live-body{display:none},类挂到外层 .msg 上谁都匹配不到,
// 结果并行那一长列步骤 + 思考框永远摊在聊天区里(2026-11 用户:拉很长)。
test("render: 并行执行时「正在执行」卡默认收起,collapsed 与 .ms-live 同元素", async () => {
  await resetAll();
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();
  const es = FakeEventSource.instances[0];

  es.emit({ kind: "reasoning_delta", session_id: "s1", reasoning: "先分别看看这两个文件" });
  es.emit({ kind: "tool_start", session_id: "s1", tool_name: "read", tool_args: '{"file_path":"a.go"}' });
  es.emit({ kind: "tool_end", session_id: "s1", tool_name: "read", tool_result: "ok" });
  es.emit({ kind: "tool_start", session_id: "s1", tool_name: "pwsh", tool_args: '{"description":"跑测试","command":"npm test"}' });
  await settle();

  const card = document.querySelector("#messages .m-card.ms-live");
  assert.ok(card, "画出了「正在执行」卡");
  assert.match(card.className, /collapsed/, "默认收起:collapsed 要和 .ms-live 在同一个元素上");
  assert.ok(document.querySelector("#messages .ms-live-hd"), "头部一行还在(正在执行 · N 步 · 最近 xx)");
  assert.ok(card.querySelector(".ms-live-body .ms-step"), "清单仍在 DOM 里,点开就看得见");
});

/* ================= 26) 执行摘要里的待办跟的是"最新那一份" ================= */
function todoStep(todos) {
  return { key: "k" + Math.random(), kind: "tool", name: "todo_write", phase: "done", args: JSON.stringify({ todos }) };
}

test("render: 执行摘要的待办跟着最新一份改写走,不是刚列出来的那一份", async () => {
  await resetAll();
  mountApp();
  await settle();
  document.querySelector("#sess-list .sess-item").click();
  await settle();

  const v1 = [{ content: "改 base.css", status: "pending" }, { content: "跑测试", status: "pending" }];
  const v2 = [{ content: "改 base.css", status: "completed" }, { content: "跑测试", status: "in_progress" }];
  const v3 = [{ content: "改 base.css", status: "completed" }, { content: "跑测试", status: "completed" }];
  const rows = [{
    key: "r1", kind: "assistant", text: "", reasoning: "",
    steps: [
      todoStep(v1), todoStep(v2),
      { key: "k-read", kind: "tool", name: "read", phase: "done", args: JSON.stringify({ file_path: "D:\\work\\s1\\base.css" }) },
    ],
  }];
  const live = [todoStep(v3)];

  const sum = summarize(rows, live);
  assert.equal(sum.todos.label, "2/2", "取的是当前轮最新那份(liveSteps 最后写)");
  assert.equal(sum.todos.items[1].status, "completed", "同一轮里第二次改写也要认");
  assert.equal(sum.todos.items[0].text, "改 base.css");

  setState({ rows, run: { steps: live }, summaryOpen: true });
  await settle();
  const panel = document.querySelector("#summary-panel");
  assert.ok(panel, "摘要抽屉画出来了");
  assert.match(panel.querySelector(".todo-card .td-n").textContent, /2\/2/, "抽屉里的进度跟着最新清单走");
  assert.match(panel.querySelector(".sum-files").textContent, /base\.css/, "相关文件那节不受影响");
});
