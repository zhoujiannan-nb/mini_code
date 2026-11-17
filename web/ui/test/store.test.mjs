import test from "node:test";
import assert from "node:assert/strict";

import { initialState, applyEvent, applyResync, setState, getState } from "../js/lib/store.js";

function makeState(extra) {
  return Object.assign({}, initialState, {
    current: "s1",
    sessions: [],
    rows: [],
    run: { steps: [] },
    approvals: {},
    recentCreated: [],
    live: null,
    pending: null,
    running: false,
    sending: false,
  }, extra || {});
}

test("store: content_delta 是累积全文,assistant 收尾落成一条消息", () => {
  let s = makeState();
  s = applyEvent(s, { kind: "content_delta", session_id: "s1", content: "你" });
  assert.equal(s.live.content, "你");
  assert.equal(s.pending, null, "首个 token 到达后转圈要撤掉");

  s = applyEvent(s, { kind: "content_delta", session_id: "s1", content: "你好呀" });
  assert.equal(s.live.content, "你好呀", "delta 携带的是累积全文,不是增量");

  s = applyEvent(s, { kind: "assistant", session_id: "s1", content: "你好呀", reasoning: "想一下" });
  assert.equal(s.rows.length, 1);
  assert.equal(s.rows[0].kind, "assistant");
  assert.equal(s.rows[0].text, "你好呀");
  assert.equal(s.rows[0].reasoning, "想一下");
  assert.equal(s.live, null);
  assert.equal(s.running, false);
  assert.equal(s.rows[0].steps.length, 0);
});

test("store: reasoning_delta 累积到 live.reasoning,正文开始后两条都在", () => {
  let s = makeState();
  s = applyEvent(s, { kind: "reasoning_delta", session_id: "s1", reasoning: "先想" });
  s = applyEvent(s, { kind: "reasoning_delta", session_id: "s1", reasoning: "先想清楚" });
  s = applyEvent(s, { kind: "content_delta", session_id: "s1", content: "答" });
  assert.equal(s.live.reasoning, "先想清楚");
  assert.equal(s.live.content, "答");
});

test("store: tool_start/tool_end 按工具名配对,重复 tool_end 不重复落步", () => {
  let s = makeState();
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "exec", tool_args: '{"command":"echo hi"}' });
  assert.equal(s.run.steps.length, 1);
  assert.equal(s.run.steps[0].phase, "run");
  assert.equal(s.run.steps[0].name, "exec");

  s = applyEvent(s, { kind: "tool_output", session_id: "s1", tool_name: "exec", content: "h" });
  s = applyEvent(s, { kind: "tool_output", session_id: "s1", tool_name: "exec", content: "i" });
  assert.equal(s.run.steps[0].output, "hi");

  s = applyEvent(s, { kind: "tool_end", session_id: "s1", tool_name: "exec", tool_result: "hi\n" });
  assert.equal(s.run.steps[0].phase, "done");
  assert.equal(s.run.steps[0].result, "hi\n");

  const again = applyEvent(s, { kind: "tool_end", session_id: "s1", tool_name: "exec", tool_result: "别覆盖" });
  assert.equal(again.run.steps.length, 1);
  assert.equal(again.run.steps[0].result, "hi\n");
});

test("store: 工具步骤在批次结束时并入正文,纯过程轮独立成条", () => {
  let s = makeState();
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "read_file", tool_args: '{"path":"a.js"}' });
  s = applyEvent(s, { kind: "tool_end", session_id: "s1", tool_name: "read_file", tool_result: "body" });
  s = applyEvent(s, { kind: "assistant", session_id: "s1", content: "读完了" });
  assert.equal(s.rows.length, 1);
  assert.equal(s.rows[0].steps.length, 1, "这一轮的步骤并进了答案卡");
  assert.equal(s.run.steps.length, 0, "新一轮从空过程开始");

  let t = makeState();
  t = applyEvent(t, { kind: "tool_start", session_id: "s1", tool_name: "exec", tool_args: "{}" });
  t = applyEvent(t, { kind: "assistant", session_id: "s1", content: "", reasoning: "只想了想" });
  assert.equal(t.rows.length, 1);
  assert.equal(t.rows[0].kind, "steps");
  assert.ok(t.rows[0].steps.some((x) => x.kind === "think"), "只有思考时收成思考步骤");
});

test("store: context_usage / usage / status 各自归约到该去的字段", () => {
  let s = makeState({ running: false, sending: true });
  s = applyEvent(s, { kind: "context_usage", session_id: "s1", tokens: 12345 });
  assert.equal(s.tokens, 12345);

  s = applyEvent(s, { kind: "usage", session_id: "s1", prompt_tokens: 100, cached_tokens: 40, cache_reported: true, speed: 12.5 });
  assert.deepEqual(s.cacheHit, { prompt: 100, cached: 40, reported: true });
  assert.equal(s.speed, 12.5);
  assert.equal(s.speedAccurate, true);

  s = applyEvent(s, { kind: "status", session_id: "s1", status: "task_started" });
  assert.equal(s.running, true);
  assert.equal(s.sending, false);

  s = applyEvent(s, { kind: "status", session_id: "s1", status: "task_done" });
  assert.equal(s.running, false);
});

test("store: 其他会话的事件不写入当前视图", () => {
  let s = makeState();
  s = applyEvent(s, { kind: "content_delta", session_id: "other", content: "别人的输出" });
  assert.equal(s.live, null);
  assert.equal(s.rows.length, 0);
});

test("store: 审批请求/解决跨会话处理,队列与展示 id 同步", () => {
  let s = makeState();
  const p = { id: "ap-1", tool_name: "delete", args: "{}", reason: "删文件", deadline: new Date(Date.now() + 60000).toISOString() };
  s = applyEvent(s, { kind: "approval_request", content: JSON.stringify(p) });
  assert.deepEqual(Object.keys(s.approvals), ["ap-1"]);
  assert.equal(s.approvalShown, "ap-1");

  s = applyEvent(s, { kind: "approval_resolved", content: JSON.stringify({ id: "ap-1", approved: true, reason: "危险操作" }) });
  assert.deepEqual(Object.keys(s.approvals), []);
  assert.equal(s.approvalShown, null);
});

test("store: 断线 resync 回填已在账本里的结果,同一事件不会重复", () => {
  let s = makeState({ running: true });
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "exec", tool_args: '{"command":"sleep 5"}' });
  assert.equal(s.run.steps[0].phase, "run");

  const events = [
    { type: "tool/call", created_at: "2026-11-13T10:00:00.123456789Z", payload: { name: "exec", args: '{"command":"sleep 5"}' } },
    { type: "tool/result", call_id: "c1", created_at: "2026-11-13T10:00:05Z", payload: { name: "exec", result: "done" } },
  ];
  let r = applyResync(s, events, false);
  assert.equal(r.run.steps.length, 1, "对账只填已有的步骤,不新增");
  assert.equal(r.run.steps[0].phase, "done");
  assert.equal(r.run.steps[0].result, "done");

  const again = applyEvent(r, { kind: "tool_end", session_id: "s1", tool_name: "exec", tool_result: "重复" });
  assert.equal(again.run.steps.length, 1);
  assert.equal(again.run.steps[0].result, "done");
  const twice = applyResync(again, events, false);
  assert.equal(twice.run.steps.length, 1);
});

test("store: resync 用账本 created_at 恢复计时起点,没有账本就不编数字", () => {
  let s = makeState({ running: true });
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "exec", tool_args: "{}" });
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "read_file", tool_args: '{"path":"x"}' });

  const t0 = "2026-11-13T10:00:00.123456789Z";
  const events = [{ type: "tool/call", created_at: t0, payload: { name: "exec", args: "{}" } }];
  const r = applyResync(s, events, true);
  const exec = r.run.steps.find((x) => x.name === "exec");
  const read = r.run.steps.find((x) => x.name === "read_file");
  assert.equal(exec.phase, "run");
  assert.equal(exec.t0, Date.parse("2026-11-13T10:00:00.123Z"), "纳秒截到毫秒再解析");
  assert.equal(read.phase, "run", "账本里没有的调用保持原样");
  assert.equal(read.t0, s.run.steps.find((x) => x.name === "read_file").t0, "不覆盖已有计时");
});

test("store: 任务已结束的 resync 把剩余步骤标成已执行未收到回执", () => {
  let s = makeState({ running: true });
  s = applyEvent(s, { kind: "tool_start", session_id: "s1", tool_name: "exec", tool_args: "{}" });
  const r = applyResync(s, [], false);
  assert.equal(r.run.steps[0].phase, "lost");
  assert.equal(r.run.steps[0].folded, true);
});

test("store: setState 的函数式 patch 也只当补丁合并,不许整份替换", () => {
  const keep = getState();
  try {
    setState((s) => ({ current: "s9", rows: s.rows }));
    const st = getState();
    assert.equal(st.current, "s9", "写了的字段生效");
    for (const k of Object.keys(initialState)) {
      assert.ok(k in st, "字段没被丢掉: " + k);
    }
    assert.ok(st.boot && typeof st.boot === "object", "boot 还在(读 boot.play 的地方不再炸)");
    assert.ok(Array.isArray(st.models), "models 还在(读 models.find 的地方不再炸)");
  } finally {
    setState(() => keep); 
  }
  assert.equal(getState().current, keep.current, "用例之间不留残留");
});
