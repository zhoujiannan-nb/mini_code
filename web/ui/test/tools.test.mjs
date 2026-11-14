// lib/tools.js 的纯逻辑测试:参数摘要、路径判定、超时识别、运行中预览、
// 待办清单与 goal 状态行的读法。
//
// 工具面切到 deepseek-harness 标准模式(tools/dsh_tools.json 是唯一事实源)之后,
// 这张表就是"界面认不认得这批新名字"的唯一判据,所以每个新参数名都要有断言钉住;
// 历史名(read_file / exec / search_files …)的断言同样留着 —— 旧会话还得照原样渲染。
import test from "node:test";
import assert from "node:assert/strict";

import {
  TOOL_ARG_KEY, toolNoArg, toolArgsOf, toolArgOne, toolArgDisplay, isPathKey,
  toolWorkHint, toolLivePreview, argsStreamPath, argsStreamBody,
  toolTimeoutSec, timeoutSecOf, isShellTool, SHELL_TIMEOUT_DEFAULT_SEC, SHELL_TIMEOUT_MAX_SEC,
  isTodoTool, todoItemsOf, todoStatusOf, TODO_ICON, TODO_STATUS_TEXT,
  isGoalTool, goalLineOf,
} from "../js/lib/tools.js";

test("tools: dsh 工具名各取到自己那个关键参数(文件类一律 file_path)", () => {
  const cases = [
    ["read", '{"file_path":"D:/proj/src/a.go"}', "file_path", "D:/proj/src/a.go"],
    ["write", '{"file_path":"a/b/c.txt","content":"x"}', "file_path", "a/b/c.txt"],
    ["edit", '{"file_path":"a.go","old_string":"x","new_string":"y"}', "file_path", "a.go"],
    ["read_image", '{"file_path":"D:/img/shot.png"}', "file_path", "D:/img/shot.png"],
    ["grep", '{"pattern":"TOOL_ARG_KEY","path":"web/ui"}', "pattern", "TOOL_ARG_KEY"],
    ["glob", '{"pattern":"**/*.go","path":"web"}', "pattern", "**/*.go"],
    ["pwsh", '{"description":"List running processes","command":"Get-Process"}', "description", "List running processes"],
    ["skill", '{"name":"web-search"}', "name", "web-search"],
    ["web_fetch", '{"url":"https://example.com/a"}', "url", "https://example.com/a"],
    ["working_directory", '{"cd":"D:/work/s1"}', "cd", "D:/work/s1"],
    ["subagent", '{"description":"审一遍并发","prompt":"…","name":"reviewer"}', "description", "审一遍并发"],
    ["subagent_fork", '{"description":"接着上文复盘","prompt":"…"}', "description", "接着上文复盘"],
    ["send_message", '{"agent":"reviewer","message":"继续"}', "agent", "reviewer"],
    ["create_goal", '{"objective":"把 web/ui 的测试跑绿"}', "objective", "把 web/ui 的测试跑绿"],
    ["job_output", '{"job_id":"exec-1a2b","wait":true}', "job_id", "exec-1a2b"],
    ["job_kill", '{"job_id":"exec-1a2b","reason":"不再需要"}', "job_id", "exec-1a2b"],
  ];
  for (const [name, args, k, v] of cases) {
    assert.deepEqual(toolArgOne(name, args), { k: k, v: v }, name + " 应该取 " + k);
    assert.ok(TOOL_ARG_KEY[name], name + " 得在 TOOL_ARG_KEY 里登记");
  }
});

test("tools: pwsh 只带命令时退回 command(description 是首选而不是唯一)", () => {
  assert.deepEqual(toolArgOne("pwsh", '{"command":"npm test"}'), { k: "command", v: "npm test" });
});

test("tools: workflow 的 meta 是对象,取它的 name / description", () => {
  assert.deepEqual(toolArgOne("workflow", '{"meta":{"name":"audit-all","description":"审计全部文件"},"script":"return 1"}'),
    { k: "meta", v: "audit-all" });
  assert.deepEqual(toolArgOne("workflow", '{"meta":{"description":"审计全部文件"},"script":"return 1"}'),
    { k: "meta", v: "审计全部文件" });
});

test("tools: 没有关键参数的工具留空兜底,不把整段 JSON 摊到标题行", () => {
  // wait_agent 只有 timeout_ms;todo_write 的参数是要画成清单的 todos;
  // list_agents 压根没有参数。三者都登记成空串 = 标题行什么都不显示。
  for (const n of ["wait_agent", "todo_write", "list_agents"]) {
    assert.equal(toolNoArg(n), true, n + " 登记成\"标题行留空\"");
    const e = TOOL_ARG_KEY[n];
    assert.ok(e === "" || (Array.isArray(e) && e.indexOf("") >= 0), n + " 的登记值带一个空串");
  }
  // todo_write 的关键参数还是照实登记成 todos(只是它不是标量,取不到值就留空)
  assert.deepEqual(TOOL_ARG_KEY.todo_write, ["todos", ""]);
  assert.deepEqual(TOOL_ARG_KEY.wait_agent, "");
  assert.deepEqual(TOOL_ARG_KEY.list_agents, "");
  assert.equal(toolArgDisplay("wait_agent", '{"timeout_ms":30000}'), null, "不显示 timeout_ms");
  assert.equal(toolArgDisplay("todo_write", '{"todos":[{"content":"a","status":"pending"}]}'), null);
  assert.equal(toolArgDisplay("list_agents", "{}"), null);
  // get_goal 没有参数:查得到 goal_id 也照样什么都取不到 → 空串参数不摊 JSON
  assert.equal(toolArgDisplay("get_goal", ""), null);
});

test("tools: 没登记的工具仍按第一个标量字段兜底,再退到参数原文", () => {
  assert.deepEqual(toolArgOne("mystery_tool", '{"query":"天气","n":3}'), { k: "query", v: "天气" });
  assert.deepEqual(toolArgDisplay("mystery_tool", '{"query":"天气"}'), { value: "天气", brief: "天气" });
  // 只有对象/数组时:整段参数压成一行(以前就这样,别改行为)
  assert.deepEqual(toolArgDisplay("mystery_tool", '{"todos":[{"a":1}]}'),
    { value: '{"todos":[{"a":1}]}', brief: '{"todos":[{"a":1}]}' });
  assert.equal(toolArgDisplay("mystery_tool", ""), null);
  assert.equal(toolArgsOf("{坏掉的 JSON"), null);
});

test("tools: 历史工具名(read_file / exec / search_files …)照旧取得到参数", () => {
  assert.deepEqual(toolArgOne("read_file", '{"path":"src/a.go"}'), { k: "path", v: "src/a.go" });
  assert.deepEqual(toolArgOne("write_file", '{"path":"src/a.go","content":"x"}'), { k: "path", v: "src/a.go" });
  assert.deepEqual(toolArgOne("edit_file", '{"path":"src/a.go"}'), { k: "path", v: "src/a.go" });
  assert.deepEqual(toolArgOne("list_dir", '{"path":"src"}'), { k: "path", v: "src" });
  assert.deepEqual(toolArgOne("search_files", '{"pattern":"foo"}'), { k: "pattern", v: "foo" });
  assert.deepEqual(toolArgOne("exec", '{"command":"dir /b"}'), { k: "command", v: "dir /b" });
  // 新工具名也认后端同时接受的历史拼写(后端 file_path / path 两套都收)
  assert.deepEqual(toolArgOne("read", '{"path":"src/a.go"}'), { k: "path", v: "src/a.go" });
});

test("tools: file_path / workdir 是路径,命令行不是(路径只留末两段)", () => {
  assert.equal(isPathKey("file_path"), true);
  assert.equal(isPathKey("workdir"), true);
  assert.equal(isPathKey("working_dir"), true);
  assert.equal(isPathKey("path"), true);
  assert.equal(isPathKey("command"), false);
  assert.equal(isPathKey("description"), false);
  assert.deepEqual(toolArgDisplay("read", '{"file_path":"D:\\\\proj\\\\src\\\\deep\\\\a.go"}'),
    { value: "D:\\proj\\src\\deep\\a.go", brief: "…/deep/a.go" });
  // 命令里满屏反斜杠也不能被当成路径截断(路径才会被压成末两段)
  const cmd = "cd D:\\proj\\src\\deep\\node_modules\\x && npm test";
  assert.deepEqual(toolArgDisplay("pwsh", JSON.stringify({ command: cmd })), { value: cmd, brief: cmd });
  const long = "Get-ChildItem " + "D:\\proj\\src\\deep\\node_modules\\".repeat(3) + "| Select-Object Name";
  assert.equal(toolArgDisplay("pwsh", JSON.stringify({ command: long })).brief, long.slice(0, 58) + "…");
});

test("tools: 写入量与运行中预览都认 file_path(dsh 的写类工具)", () => {
  assert.equal(toolWorkHint("write", '{"file_path":"a.txt","content":"一行\\n两行"}'),
    "写入 13 B · 2 行");
  const prev = toolLivePreview("edit", '{"file_path":"src/a.go","new_string":"改成这样"}');
  assert.match(prev.split("\n")[0], /^改动 src\/a\.go · .* \(new_string\)$/);
  assert.match(toolLivePreview("write", '{"file_path":"a/b/c.txt","content":"x"}'), /^写入 a\/b\/c\.txt ·/);
  // 命令工具:预览把本次参数逐行列出,命令在里头
  assert.match(toolLivePreview("pwsh", '{"description":"跑测试","command":"npm test"}'), /command: npm test/);
  // 待办清单:参数本身就是清单,组件画 .todo-card,这里让路
  assert.equal(toolLivePreview("todo_write", '{"todos":[{"content":"a","status":"pending"}]}'), "");
  // 清单参数还没生成完(还不是合法 JSON)时照旧摊出来,别一片空白
  assert.match(toolLivePreview("todo_write", '{"todos":[{"content":"甲段'), /\[\{"content":"甲段/);
});

test("tools: 参数流里的路径认 file_path(path/target/dest 照旧)", () => {
  assert.equal(argsStreamPath('{"content":"x","file_path":"D:\\\\work\\\\a.txt"}'), "D:\\work\\a.txt");
  assert.equal(argsStreamPath('{"file_path":"src/a.go","content":"ab'), "src/a.go");
  assert.equal(argsStreamPath('{"path":"src/old.go","content":"ab'), "src/old.go");
  assert.equal(argsStreamPath('{"workdir":"D:\\\\work"},"command":"dir'), "D:\\work");
  assert.equal(argsStreamPath('{"command":"dir'), "");
  assert.equal(argsStreamBody('{"file_path":"a.txt","content":"正文一'), "正文一");
});

test("tools: timeoutMs 是毫秒,timeout_s 是秒,裸 timeout 超 3600 才当毫秒填", () => {
  assert.equal(timeoutSecOf(300000, "timeoutMs"), 300);
  assert.equal(timeoutSecOf(2000, "timeoutMs"), 2, "键名带 ms 就不靠数值大小猜单位");
  assert.equal(timeoutSecOf(30000, "timeout_ms"), 30);
  assert.equal(timeoutSecOf(300, "timeout_s"), 300);
  assert.equal(timeoutSecOf(300, "timeout"), 300);
  assert.equal(timeoutSecOf(300000, "timeout"), 300, "没写单位又超过 3600:按毫秒填的秒数理解");
  assert.equal(timeoutSecOf("45"), 45);
  assert.equal(timeoutSecOf(0), 0);
  assert.equal(timeoutSecOf("很久"), 0);
});

test("tools: pwsh 步骤的超时上限与 dsh 一致(默认 120000ms / 上限 600000ms)", () => {
  assert.equal(SHELL_TIMEOUT_DEFAULT_SEC, 120);
  assert.equal(SHELL_TIMEOUT_MAX_SEC, 600);
  assert.equal(isShellTool("pwsh"), true);
  assert.equal(isShellTool("exec"), true, "历史名也算命令工具");
  assert.equal(isShellTool("read"), false);

  assert.equal(toolTimeoutSec('{"description":"跑测试","command":"npm test","timeoutMs":300000}', "pwsh"), 300);
  assert.equal(toolTimeoutSec('{"description":"长跑","command":"npm run build","timeoutMs":600000}', "pwsh"), 600);
  // 模型不写 timeoutMs:执行器套它自己的默认值,计时栏不该空着
  assert.equal(toolTimeoutSec('{"description":"跑测试","command":"npm test"}', "pwsh"), 120);
  assert.equal(toolTimeoutSec('{"command":"npm test"}', "pwsh"), 120);
  // 超过上限的调用执行器会压到 600s,界面报一个跑不到的上限是骗人
  assert.equal(toolTimeoutSec('{"command":"npm test","timeoutMs":900000}', "pwsh"), 600);
  // 命令文本里自己写的 timeout 照旧认(历史 exec 会话)
  assert.equal(toolTimeoutSec('{"command":"curl --timeout=30 https://x"}', "exec"), 30);
  // 非命令工具没写超时就是没有,别编一个 120s 出来
  assert.equal(toolTimeoutSec('{"file_path":"a.txt","content":"x"}', "write"), 0);
  assert.equal(toolTimeoutSec('{"job_id":"j1","wait":true,"timeout_ms":120000}', "job_output"), 120);
  assert.equal(toolTimeoutSec('{"timeout":45}', "mystery"), 45);
});

test("tools: todo_write 的 todos 读成一份清单(三态 + 完成数统计)", () => {
  assert.equal(isTodoTool("todo_write"), true);
  assert.equal(isTodoTool("todo"), true, "历史名也算清单");
  assert.equal(isTodoTool("read"), false);

  const args = '{"todos":[' +
    '{"content":"读 dsh_tools.json","status":"completed"},' +
    '{"content":"改 lib/tools.js","status":"in_progress"},' +
    '{"content":"补渲染测试","status":"pending"},' +
    '{"content":"跑 npm test","status":"pending"},' +
    '{"content":"收尾","status":"completed"}]}';
  const t = todoItemsOf("todo_write", args);
  assert.equal(t.total, 5);
  assert.equal(t.done, 2);
  assert.equal(t.doing, 1);
  assert.equal(t.label, "2/5", "统计写成 完成数/总数");
  assert.deepEqual(t.items.map((i) => i.status),
    ["completed", "in_progress", "pending", "pending", "completed"]);
  assert.deepEqual(t.items.map((i) => i.text), ["读 dsh_tools.json", "改 lib/tools.js", "补渲染测试", "跑 npm test", "收尾"]);
  // 三态各有图标与文案(待办空心 / 进行中实心 / 完成打勾)
  assert.deepEqual(TODO_ICON, { pending: "○", in_progress: "●", completed: "✓" });
  assert.deepEqual(TODO_STATUS_TEXT, { pending: "待办", in_progress: "进行中", completed: "已完成" });
});

test("tools: 清单读不出来的时候交回普通步骤画法", () => {
  assert.equal(todoItemsOf("read", '{"file_path":"a"}'), null, "非清单工具");
  assert.equal(todoItemsOf("todo_write", ""), null, "没有参数");
  assert.equal(todoItemsOf("todo_write", '{"todos":[{"content":"甲","stat'), null, "参数还是半截 JSON");
  assert.equal(todoItemsOf("todo_write", '{"todos":[]}'), null, "空清单不算清单");
  assert.equal(todoItemsOf("todo_write", '{"todos":[{"status":"pending"}]}'), null, "一条文本都没有");
  // 状态认不出的当待办:顶少一格高亮,不会把没做的事说成做完了
  assert.equal(todoStatusOf("WAT"), "pending");
  assert.equal(todoStatusOf("done"), "completed");
  assert.equal(todoStatusOf("in_progress"), "in_progress");
  const t = todoItemsOf("todo_write", '{"todos":[{"content":" 甲  ","status":"completed"},{"content":"乙"}]}');
  assert.deepEqual(t.items, [{ text: "甲", status: "completed" }, { text: "乙", status: "pending" }]);
  assert.equal(t.label, "1/2");
});

test("tools: goal 工具读成一行状态文案(objective / action)", () => {
  assert.equal(isGoalTool("create_goal"), true);
  assert.equal(isGoalTool("update_goal"), true);
  assert.equal(isGoalTool("get_goal"), true);
  assert.equal(isGoalTool("todo_write"), false);

  assert.equal(goalLineOf("create_goal", '{"objective":"把 web/ui 测试跑绿"}'), "新建目标：把 web/ui 测试跑绿");
  assert.equal(goalLineOf("create_goal", "{}"), "新建目标");
  assert.equal(goalLineOf("update_goal", '{"goal_id":"g1","revision":2,"action":"complete"}'), "标记完成");
  assert.equal(goalLineOf("update_goal", '{"goal_id":"g1","revision":3,"action":"edit","objective":"换个目标"}'),
    "编辑目标：换个目标");
  assert.equal(goalLineOf("update_goal", '{"goal_id":"g1","revision":4,"action":"blocked","blocked_reason":"缺凭据"}'),
    "标记受阻：缺凭据");
  assert.equal(goalLineOf("update_goal", '{"goal_id":"g1","revision":2,"action":"pause"}'), "暂停目标");
  assert.equal(goalLineOf("update_goal", '{"goal_id":"g1","revision":2}'), "更新目标", "没有 action 也要有话说");
  assert.equal(goalLineOf("get_goal", "{}"), "查看当前目标");
  assert.equal(goalLineOf("read", '{"file_path":"a"}'), "", "非 goal 工具空串");
});
