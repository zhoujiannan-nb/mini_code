# dsh 标准模式 → MiniCode 迁移台账（定稿）

> 目标：把 deepseek-harness（dsh）`standard` preset 的全部逻辑迁到 MiniCode，
> 保留 MiniCode 的 Go 与前端风格。工具面（名字/描述/参数 schema）以
> `tools/dsh_tools.json`（dsh 实盘请求抓取，23 工具）为唯一事实源，
> 底层实现全部是 MiniCode 自己的机制。dsh 真源参照：
> `packages/bundle/web-app/presets/standard.patch.yml` 与 `packages/*/src`。
> 本轮迁移已完成；下文即最终落点记录。

## 已落地 · 工具面与循环（A 类及以前）

- 23 工具 schema 原样外包 + 实现落地：read/write/edit/glob/grep/pwsh/read_image/
  skill/todo_write/working_directory/web_fetch + job 三件套 + subagent 域 5 件套
  （subagent/subagent_fork/send_message/list_agents/wait_agent）+ goal 三件套
  （create/get/update）。workflow 见 B4（唯一未注册项）。
- 改名兼容层：`tools/dsh_surface.go`（`strParam` 双写拼写）、policy/observed 表双名。
- pwsh 一次性 PowerShell 语义（持久 shell 池不进标准面）。
- 循环/压缩/纪律全套：3 次重试、0.80/0.85/0.90 压缩阈值、prune+spill、token 校准、
  write-before-read 门、MC_EXEC_GATE 纪律门、churn/stall nudge、通知注入（10 类，含 goal）。
- 系统提示词 build.txt 为 dsh 纪律版（persona 用 `{{model}}`）。
- A1 wait_agent：无 `agent` 参数 = 等本会话注入队列（QueueUserMessage 门铃
  `injectWake`；HasPendingInput/WaitForInput 不消费）；`agent` = 隐藏兼容参数
  （Output()，超时 clamp 10s–3600s 照 schema）；EnableAgentTools 用 mgr.Peek 接线。
- A2 working_directory：preparePromptTools 注册 sharedExec 后重绑
  cd.SetExecHost(ex)+cd.SetDir(ex.WorkingDir())；ExecTool 加 WorkingDir() getter。
- A3 prune：豁免名单 `read/glob`（含历史名 read_file/list_dir）；marker 文案用
  read offset/limit + grep 新名。
- A4 旧名清零：subagent.go 三处、shell.go/shell_async.go 提示、requestview.go
  （stubArgTools 双名、toolArgStub 双 key、read_image）。
- A5 grep/glob 对齐 schema 承诺：grep 默认 250、单行 2000B rune-safe 截断、超量
  spool 落盘报路径；glob 默认 100、只出文件、mtime 新→旧（tie 按路径）、无 '/'
  模式 = 任意深度 basename、超量 spool 全量；`tools/spool.go`（TempDir/mini_code_spool，
  30 天清扫）。

## 已落地 · 机制（B 类）

- B1 todo/goal 提到 session 生命周期：`tools/dsh_newtools.go` 的 TodoTool/GoalStore
  带 SetOnChanged/Snapshot/Restore；session 持每会话唯一实例（GoalState/TodoState 懒建
  +从 record metadata 恢复 +变更即持久化，metadata keys `goal_store`/`todos`）。
  HTTP 面扩 GET /api/sessions/{id}/goal（body = 快照或 null），POST /goal 兼容接口不变。
- B2 goal 轮次驱动（`session/goalround.go`）：
  * 成功任务收尾时若注入队列已空，driveGoalRound 决策：active goal 且 rounds < cap
    → BeginRound + deliverIdle 投递 `<goal_round round="N" max="M">` 自动轮通知；
    cap 用尽 → 只向历史追加 `<goal_round status="cap">`（不起任务）；无 deliver hook
    只记日志不计数（计数=投递成功，重启不丢：BeginRound 先计数后投递）。
  * 轮次来源：Prompt 顶部 markGoalRoundOrigin(goal) 按 `<goal_round` 前缀判自动轮；
    GoalStore 据此在 update_goal 里执行 dsh 权限规则——edit/pause/resume 自动轮拒绝、
    blocked 需 ≥MinBlockedRounds(3) 已投轮次、complete 任何轮次可过；
    MaxGoalRounds 未指明时 DefaultGoalMaxRounds=8。
  * 通知分类：notices.go 新增 NoticeGoal；前端 transcript.js 同步标签「目标续跑轮」
    与 `<goal_round` 兜底探测。
  * 有意偏差：进程重启后不自动续驱（下一条人类消息重新点火轮次）——dsh 宿主层在
    重启时重挂 driver，mc 判定"重启后静默起任务"惊吓大于收益，记录为偏差。
- B3 sandbox_permissions/justification（`session/gate.go`）：write/edit/pwsh schema 的
  提权对（enum workspace-write|danger-full-access + 必填 justification）在 gate 校验——
  任何模式先验 pair（enum/justification 缺失直接拒），cautious 下带 pair 的调用一律走
  一次性审批且审批卡首行渲染 `[sandbox: asking for <mode>] <justification>`；
  hard-block 加 pair 直接拒（"wider mode 不放宽禁令"）；用户拒绝回显
  `[sandbox: ...] Do NOT retry`；full 模式 pair 合法即放行（无 OS 沙箱可绕，语义一致）。
  一次性批准 = dsh 的 perCall 优先级；会话级覆写对应 mc 的 full/cautious 模式本身。
- B4 workflow：**诚实化摘除**（决策=不可行）。无 JS 引擎、goja 未 vendor 且不在范围；
  描述满口 agent/parallel/pipeline 承诺而 Execute 必失败属于说谎的工具面，故
  EnableAgentTools 不再注册 workflow，build.txt 同步删除 workflow 段；
  占位实现保留在 tools/dsh_newtools.go（一行可重注册），role 表保留 allow。
- B5 AGENTS.md 装载（`session/agentsmd.go`）：用户全局（~/.agents、~/.mini_code）
  在前，仓库内 .git 根→cwd 祖先链在后（越近越优先）；每目录 AGENTS.md 优先、
  CLAUDE.md 兜底；单文件 65536B 上限、rune 边界截断加 marker；装入静态 system
  prompt（每任务重读文件，热 splice 不做——mc 静态前缀缓存哲学，记录偏差）。

## 已落地 · 前端（C 类，零构建 Preact+htm）

- C1 `lib/tools.js`：TOOL_ARG_KEY 全面认 dsh 参数名（file_path/pattern/cd/objective/
  job_id/timeoutMs…，历史拼写并列同收）；toolLivePreview/argsStreamPath/isPathKey
  路径链加 file_path/workdir；超时表加 timeoutms/timeout_ms。
- C2 新组件 `TodoCard.js`：todo_write 步骤渲染清单卡（含进度徽标）；goal 三工具步骤
  渲染状态行；Steps.js 接线；layers.css 加 .todo-card/.goal-line 样式。
- C3 测试并入 web/ui/test：tools.test.mjs 14 条 + render.test.mjs 3 条 +
  messages.test.mjs notice 表更新（10 类含 goal）+ detectNoticeKind goal_round 钉住。

## 明确偏差（不做，保持记录）

- `exit_plan_mode` / `ask_user_question` / `schedule_*` / `present` / `office_*` /
  `plugin_manager`：dsh 实盘 23 工具面里没有，不新增模型面工具；cautious 审批承接。
- OS 级沙箱（bwrap/Seatbelt/ACL）：不移植，mc 走审批制；提权语义按 B3 映射。
- workflow JS 编排引擎：不 vendor goja；工具从面摘除（见 B4）。
- 运行时上下文快照/时钟上下文：静态提示词保 KV 缓存 + notices 机制承接。
- 写前读观察守卫 fs-observation-policy：dsh 默认关；保留 mc 更强的 write-before-read 门。
- goal 重启续驱：不自动续（见 B2）。

## 已知环境噪声

- webview cgo 头文件缺（EventToken.h）：`go build ./...` 全量会炸，用包集 build。
- vendor.test.mjs preact 字节差 CRLF——忽略。
- findstr 长行截断、cmd one-liner 偶发 shell 退出——重跑即可。

## 验证口径（本轮终态）

- `go build -mod=vendor ./agent ./approval ./channels ./config ./job ./plugin
  ./provider ./session ./tools ./util ./web .` → BUILD_OK；
  `go vet` 同包集 → VET_ALL_OK。
- `go test -mod=vendor -count=1 ./agent/... ./session/... ./tools/... ./web/...`
  → 全绿（session 含 goalround/agentsmd/gate_sandbox 新测试；tools 含 glob/grep
  新契约；web 含 reasoning box）。
- `web/ui && npm test` → 48 pass / 1 fail，唯一失败为 vendor.test.mjs CRLF 字节差
  （与基线逐字节相同）。
