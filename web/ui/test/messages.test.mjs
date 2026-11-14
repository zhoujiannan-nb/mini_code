// 消息分组 / 步骤折叠 / notice 分类的纯函数测试。
// 这些函数是历史和实时两条渲染路径共用的形状来源,所以它们对了,界面就不会
// 出现"刷新之后步骤跑进别的卡片"那类错位。
import test from "node:test";
import assert from "node:assert/strict";

import {
  NOTICE_LABELS, noticeOf, contentText, contentImages,
  makeToolStep, makeThinkStep, findOpenStep, groupSteps, historyRows, detectNoticeKind,
} from "../js/lib/transcript.js";

test("messages: 十类注入 notice 各有中文标签,未知类型落到「系统注入」", () => {
  assert.deepEqual(Object.keys(NOTICE_LABELS).sort(), [
    "compact", "context", "goal", "job", "persist", "retry", "steer", "subagent", "summary", "worker",
  ]);
  assert.equal(noticeOf("worker", "x").label, "工作区公约");
  assert.equal(noticeOf("context", "x").label, "上下文提醒");
  assert.equal(noticeOf("subagent", "x").label, "子代理回执");
  assert.equal(noticeOf("job", "x").label, "后台命令回执");
  assert.equal(noticeOf("steer", "x").label, "继续守卫轻推");
  assert.equal(noticeOf("retry", "x").label, "空回复提醒");
  assert.equal(noticeOf("compact", "x").label, "压缩指令");
  assert.equal(noticeOf("summary", "x").label, "压缩摘要");
  assert.equal(noticeOf("persist", "x").label, "持久化告警");
  assert.equal(noticeOf("goal", "x").label, "目标续跑轮");
  assert.equal(noticeOf("mystery", "x").label, "系统注入");
});

test("messages: notice 摘要按 90 字截断,全文完整保留", () => {
  const long = "字".repeat(120);
  const n = noticeOf("worker", long);
  assert.equal(Array.from(n.brief).length, 91, "90 字 + 省略号");
  assert.ok(n.brief.endsWith("…"));
  assert.equal(n.full, long);
  assert.equal(noticeOf("worker", "短").brief, "短");
});

test("messages: 多模态内容分别取文字与图片", () => {
  const m = {
    role: "user",
    content: [
      { type: "text", text: "看这张图" },
      { type: "text", text: "还有这张" },
      { type: "image_url", image_url: { url: "data:image/png;base64,AAA" } },
    ],
  };
  // contentText 是通用读取(非用户轮也会用),图片位置保留旧版的占位符;
  // 用户轮在 historyRows 里只取 text part,所以截图不会显示成"📷 [图片]"。
  assert.equal(contentText(m), "看这张图\n还有这张\n📷 [图片]");
  assert.deepEqual(contentImages(m), ["data:image/png;base64,AAA"]);
  assert.equal(contentText({ content: "纯文本" }), "纯文本");
  assert.deepEqual(contentImages({ content: "纯文本" }), []);

  const rows = historyRows([{ role: "user", content: m.content }]);
  assert.equal(rows[0].kind, "user");
  assert.equal(rows[0].text, "看这张图\n还有这张", "用户截图的文字部分不带图片占位");
  assert.deepEqual(rows[0].images, ["data:image/png;base64,AAA"]);
});

test("messages: 历史分组 — user 是任务边界,assistant 吸收之前的步骤", () => {
  const msgs = [
    { role: "user", content: "跑一下" },
    { role: "assistant", content: "", tool_calls: [{ function: { name: "exec", arguments: '{"command":"ls"}' } }] },
    { role: "tool", name: "exec", content: "a.js\nb.js" },
    { role: "assistant", content: "两个文件" },
    { role: "user", content: "继续" },
    { role: "assistant", content: "", reasoning_content: "想想" },
  ];
  const rows = historyRows(msgs);
  assert.deepEqual(rows.map((r) => r.kind), ["user", "assistant", "user", "steps"]);

  // 第二条 assistant 吸收了 exec 那一步,并且 tool 轮的正文填进了同一步。
  const answer = rows[1];
  assert.equal(answer.text, "两个文件");
  assert.equal(answer.steps.length, 1);
  assert.equal(answer.steps[0].name, "exec");
  assert.equal(answer.steps[0].result, "a.js\nb.js");
  assert.equal(answer.steps[0].phase, "done");

  // 最后只有思考、没有正文 → 独立过程条,思考作为一步。
  assert.equal(rows[3].steps.length, 1);
  assert.equal(rows[3].steps[0].kind, "think");
});

test("messages: 历史里的注入 user 轮画成 notice,不冒充用户", () => {
  const rows = historyRows([
    { role: "user", content: "我是用户", notice: "" },
    { role: "user", content: "公约正文", notice: "worker" },
  ]);
  assert.equal(rows[0].kind, "user");
  assert.equal(rows[1].kind, "notice");
  assert.equal(rows[1].notice, "worker");
  assert.equal(noticeOf(rows[1].notice, rows[1].text).label, "工作区公约");
});

test("messages: 工具回执按名字配对,并行同名调用各拿各的,配不上才独立成步", () => {
  const run = [
    makeToolStep({ name: "exec", args: "a", phase: "soft" }),
    makeToolStep({ name: "exec", args: "b", phase: "soft" }),
    makeToolStep({ name: "read_file", args: "c", phase: "soft" }),
  ];
  const first = findOpenStep(run, "exec");
  assert.equal(first.args, "a", "从前往后找第一个还没回执的同名步骤");

  const rows = historyRows([
    { role: "assistant", content: "", tool_calls: [
      { function: { name: "exec", arguments: "{}" } },
      { function: { name: "exec", arguments: "{}" } },
    ] },
    { role: "tool", name: "exec", content: "第一个结果" },
    { role: "tool", name: "exec", content: "第二个结果" },
    { role: "tool", name: "other", content: "孤儿结果" },
    { role: "assistant", content: "完" },
  ]);
  const steps = rows[rows.length - 1].steps;
  assert.equal(steps.length, 3, "两个同名调用 + 一个配不上的孤儿");
  assert.deepEqual(steps.filter((s) => s.name === "exec").map((s) => s.result), ["第一个结果", "第二个结果"]);
  assert.equal(steps[2].name, "other");
  assert.equal(steps[2].result, "孤儿结果");
});

test("messages: 步骤摘要数步数、数思考、取最近一步", () => {
  const steps = [
    makeToolStep({ name: "exec", args: "", phase: "done", result: "ok" }),
    makeThinkStep("想一想"),
    makeToolStep({ name: "write_file", args: "", phase: "done", result: "写完" }),
  ];
  const g = groupSteps(steps);
  assert.equal(g.count, 3, "折叠条上的 N 步包含思考步骤(与旧 DOM 数法一致)");
  assert.equal(g.thinkCount, 1);
  assert.equal(g.tools.length, 2);
  assert.equal(g.lastHint, "最近 write_file · ✓ 2 字");
  assert.equal(groupSteps([]).lastHint, "");
});

test("messages: 前端兜底按标记认 notice,goal_round 块算「goal」", () => {
  assert.equal(detectNoticeKind('<goal_round round="2" max="8">\n自动续跑\n</goal_round>'), "goal");
  assert.equal(detectNoticeKind('<goal_round status="cap">\n预算用尽\n</goal_round>'), "goal");
  assert.equal(detectNoticeKind("我来改一下代码"), "");
});
