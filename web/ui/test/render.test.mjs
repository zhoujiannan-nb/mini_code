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
    context_reminders: true,
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
