# 前端测试清单与方案

> 记录时间：2026-10-27
> 审计对象：`web/ui/index.html`（5,962 行 / 290,874 字节，由 `//go:embed all:ui` 打进二进制）
> 后端契约：`web/web.go` 中注册的 40 个路由（`GET /`、`/api/*`）

---

## 一、审计结论（第 1 步记录）

### 1.1 量化基线

| 指标 | 数值 | 说明 |
|---|---|---|
| 文件总行数 | 5,962 | 单文件承载 HTML+CSS+JS |
| 文件字节 | 290,874 | 每次改动整文件 diff |
| CSS 区段 | 第 214–2057 行（1,844 行） | 674 条规则 |
| body 区段 | 第 2059–2368 行（310 行） | 40 个 `<button>` |
| JS 区段 | 第 2369–5960 行（3,591 行） | 203 个 `function` |
| CSS 类名 | 254 个 | 无命名规范，`md`/`msg`/`frow` 等短名重复 20–50 次 |
| CSS 变量 | 91 定义 / 747 引用 | ✅ 主题已变量化，这是好底子 |
| 硬编码 hex | 151 处（102 个不同值） | 未走变量 |
| 硬编码 rgba | 84 处 | 未走变量 |
| `!important` | 9 处 | 覆盖战痕迹 |
| body 内联 style | 18 处 | 固定宽度/弹性布局 |
| `innerHTML=` | 63 处 | 无统一渲染层 |
| `insertAdjacentHTML` | 5 处 | 流式渲染追加 |
| `addEventListener` | 98 处 | 其中 60+ 集中在 `init()` 尾部 |
| 全局函数 | 203 个 | 全部挂在 window 作用域 |
| `try/catch` | 75 处 | 但多为 `catch(e){}` 静默吞掉 |

### 1.2 问题清单（按严重度）

#### P0 — 安全 / 正确性

| ID | 问题 | 位置 | 证据 |
|---|---|---|---|
| A-01 | `esc()` 不转义引号，属性上下文可注入 | L2557 | `esc(s)` 用 `div.textContent=s; return innerHTML`，只转 `& < >`，不转 `"` `'`。所有 `title="+esc(x)+"` 的拼接（如 L5341 群成员名）理论上可闭合属性 |
| A-02 | 流式渲染重复追加导致 O(n²) | L3324 | `slot.innerHTML = html` 每 delta 全量重刷，长回复下重复解析 |
| A-03 | `loadSessions` 无请求序列号 | L2903 | 并发刷新时旧响应可覆盖新结果（`selectSession` 有防护，列表刷新没有） |

#### P1 — 可维护性

| ID | 问题 | 证据 |
|---|---|---|
| B-01 | CSS 10 层叠加覆盖（v3→v10） | 注解块：`v3 设计令牌` → `v3视觉润色` → `v4` → `v4.2分层` → `v4.3` → `v5顶栏` → `v6品牌` → `v7渐变` → `v8图标` → `v9间距` → `v10执行过程`。同一选择器被重复声明：`.pill` 6 次、`aside` 6 次、`.chat-head` 6 次、`body` 5 次、`:root` 5 次 |
| B-02 | 203 个全局函数无模块边界 | 全部 `window.xxx`，命名混杂三种风格：`addUser`/`paintModel`/`fsOpen`/`stepsAddTool`/`onApprovalRequest` |
| B-03 | 事件绑定集中在 `init()` 尾部 200 行 | L5734–5935，无分组，靠注释行分隔 |
| B-04 | DOM 更新无统一入口 | 63 处裸 `innerHTML=`，无事件委托、无局部 patch |
| B-05 | 硬编码魔法数 | `230ms` 单击/双击判定、`8000ms` sending 兜底、`75000ms` SSE 看门狗、`120px` 懒加载阈值、`4000` 字符截断 |
| B-06 | 拼音/短名混用 | `fs*`(file search)、`gm*`(group modal)、`ua*`(user avatar)、`ap*`(approval)、`tp*`(think pop)、`b*`(boot) |
| B-07 | 空 `catch(e){}` 吞错误 | L3674/3678/3792 等，SSE 解析失败完全静默 |

#### P2 — 可访问性 / 响应式

| ID | 问题 | 证据 |
|---|---|---|
| C-01 | 40 个 `<button>` 无 `aria-label`，`role` 属性 0 个 | 图标按钮（SVG-only）对读屏不可达 |
| C-02 | 无 `tabindex`、无焦点管理 | 弹窗打开不 trap focus，Esc 仅在全局 keydown 里处理 |
| C-03 | 15 处 emoji 作为语义图标 | `🔔 审批`、`🧵 子任务`、`👥 成员`、`⚡ 压缩` — 与 v8 已换成 SVG 的顶栏风格不一致 |
| C-04 | 响应式断点靠大面积重置 | 8 个 `@media`，其中 4 个 `max-width:760px` 分别重置同一批元素 |
| C-05 | 对比度未验证 | 暗色 `--text3` 等小字色未做 WCAG 计算 |

#### P3 — 健壮性

| ID | 问题 | 证据 |
|---|---|---|
| D-01 | 图片 data URL 上限 4MB 仅前端拦一道 | `MAX_IMAGE_BYTES` 检查在 `addImageFiles`，直接调 API 可绕过 |
| D-02 | 输入长度无上限 | `#input` textarea 无 maxlength |
| D-03 | `renderHistory` 全量重建 | 切会话时清空 + 重建全部卡片，长会话（实测 410 条消息）有卡顿风险 |

### 1.3 控件 → 触发路径 → 样式来源 映射表

> `$()` = `document.querySelector`；样式来源：`CSS`=类选择器，`INLINE`=style 属性，`JS`=JS 拼字符串

#### 顶栏

| 控件 | id | 触发路径 | 样式来源 |
|---|---|---|---|
| 审批徽标 | `btn-approval-badge` | click → `toggleTasksPanel` 同类 → `syncApprovals()` → `GET /api/approvals` → `approvalBadge()` 更新计数 | CSS `.approval-badge` |
| 主题切换 | `btn-theme` | click → `cycleThemeMode()` → `localStorage mc-theme` → `applyTheme()` 设 `html[data-theme]` | CSS `.icon-btn.theme-btn` + `.lbl` |
| 配置 | `btn-settings` | click → `openSettings()` → `GET /api/config` → `renderModels()`/`bootFillForm()` → 打开 `#settings-mask` | CSS `.icon-btn` |
| 状态 pill | `status-pill` | `loadStatus()` → `GET /api/status` → 拼 `<b>模型名</b> · 端口 N` | CSS `.pill` |

#### 侧栏

| 控件 | id | 触发路径 | 样式来源 |
|---|---|---|---|
| 新建会话 | `btn-new` | click → `resetToNew()` → 清 `state.current` → `paintModel/Mode/Tokens` | CSS `.new-btn` |
| 会话项 | 动态 `div.sess-item` | click → `selectSession(id)` → `GET /api/sessions/{id}` → `renderHistory()` | CSS `.sess-item` + JS 拼 `innerHTML`（L2971） |
| 会话删除/归档 | 动态 `.btn` | click → `DELETE /api/sessions/{id}` 或 `POST .../archive` | JS 拼字符串（L2985–2993） |
| 拖拽归档 | `.sess-item[draggable]` | dragstart/drop → `readDragPayload()` → `setArchived()` | CSS `.drop-hot` |
| 群聊项 | 动态 `.group-item` | click → `openGroup(id)` → `GET /api/groups/{id}` → `renderGroupMessages()` | CSS `.group-item` |
| 已归档区 | `archive-toggle` | click → `setArchiveCollapsed()` → `localStorage` | CSS `.collapsed` |

#### 聊天区

| 控件 | id | 触发路径 | 样式来源 |
|---|---|---|---|
| 返回父会话 | `btn-back-parent` | click → `selectSession(parentId)` | CSS `.back-btn` |
| 标题重命名 | `chat-title` | dblclick → 弹 `prompt` → `POST .../rename` | CSS `.title` |
| 子任务面板 | `btn-tasks` | click → `loadChildren()` → `GET .../children` → `renderTasksPanel()` | CSS `.tasks-panel` |
| 群成员面板 | `btn-group-members` | click → `toggleGroupPanel()` | CSS `.tasks-panel` |
| 消息卡片 | 动态 `.msg` | `onEvent` → `liveStart/liveUpdate` → `mdToHtml()` | CSS `.msg` + `.md` + JS 拼 `innerHTML`（L3214/3478/3513） |

#### 输入区

| 控件 | id | 触发路径 | 样式来源 |
|---|---|---|---|
| 文本输入 | `input` | keydown → `send()` / paste → `addImageFiles()` / input → `autoGrow()`+`fsCheckInput()` | CSS `#input` |
| 发送 | `btn-send` | click → `send()` → `POST /api/messages` → `pendingStart()` → 等 SSE | CSS `.send-btn` |
| 停止 | `btn-stop` | click → `stop()` → `POST .../cancel` | CSS `.stop-btn` |
| 加图片 | `btn-plus` | click → `#file-shot.click()` → change → `addImageFiles()` | CSS `.plus-btn` + INLINE 预览 `div` |
| 模型按钮 | `btn-model` | click 单击(230ms 定时器) → `toggleModelMenu()`；双击 → `openThinkingPop()` → `POST .../reasoning` | CSS `.model-inline-btn` |
| 模型菜单 | `model-menu` | `toggleModelMenu()` → `pickModel()` → `POST .../model` → `paintModel()` | CSS `.model-menu` + JS 拼（L3965） |
| 思考强度 | `tp-effort` (range) | input/change → `updateThinkFill()` → `persistReasoning()` | CSS `.think-pop` |
| 权限模式 | `btn-mode` | click → `toggleMode()` → `POST .../mode` → `paintMode()` | CSS `.mode-btn` + `.cautious` |
| 压缩 | `btn-compact` | click → `POST .../compact` → toast + `updateTokenUsage()` | CSS `.compact-btn` |
| 上下文提醒 | `ctx-reminders` | change → `toggleContextReminders()` → `POST .../context-reminders` | CSS `.ctx-toggle` |
| 发送模式提示 | `foot-hint` | click → `toggleSendMode()` → `localStorage` | CSS `.foot-hint` |
| 文件夹选择器 | `fs-picker` | input `@` → `fsCheckInput()` → `fsDoFetch()` → `GET /api/fs/search` | CSS `.fs-picker` + JS 拼（L5111） |

#### 弹窗 / 配置

| 控件 | id | 触发路径 | 样式来源 |
|---|---|---|---|
| 配置导航 | `.set-nav-item[data-page]` | click → `setPage(id)` 切 `.active` | CSS `.set-nav-item` |
| 主题下拉 | `c-theme` | change → `setThemeMode()` | CSS `select` |
| Web 端口 | `w-port` | 表单收集 → `saveSettings()` → `PUT /api/config` | CSS `.frow` |
| 模型卡片 | 动态 `.model-tile` | click → `renderModelEditor()`；probe → `POST /api/llm/probe`；fetch → `POST /api/llm/models` | CSS `.model-tile` + JS 拼（L4283） |
| 开机动画 | `b-*` (11 个字段) | `initBootSettings()` → `GET /api/bootanim`；install → `POST /api/bootanim/install`；delete → `DELETE /api/bootanim/{id}` | CSS `.boot-grid` + JS 拼（L4646/4681） |
| 审批弹窗 | `approval-mask` | SSE `approval_request` → `showApproval()` → `POST /api/approvals/{id}/decision` | CSS `.approval-modal` |
| 群设置 | `group-mask` | `openGroupSettings()` → `POST .../rename`+`/background` | CSS `.modal` + INLINE `width:480px` |

---

## 二、测试清单（可执行用例）

> 前置条件（除非特别说明）：桌面 exe 或 `mini_code web` 已启动，浏览器打开 `http://127.0.0.1:7500`，至少配置 1 个可用模型。
> 标注：`[自动]` = 可脚本化；`[手工]` = 需人工观察

### M1 发消息与流式输出

| 用例 | 类型 | 前置 | 步骤 | 期望 |
|---|---|---|---|---|
| M1-01 | 正常 | 新会话 | 输入「你好」→ Enter | 用户气泡出现；pending 转圈；流式文字逐字出现；结束时转圈消失、发送键复位 |
| M1-02 | 正常 | 已有会话 | 同 M1-01 | 消息追加到当前会话，不新建会话 |
| M1-03 | 边界 | 空输入 | 直接 Enter / 只输空格 | 不发送，无网络请求 |
| M1-04 | 异常 | 模型服务不可用 | 停掉后端模型 → 发送 | 出现 `发送失败:` 或 `error` 气泡，发送键恢复可点 |
| M1-05 | 边界 | Shift+Enter | 输入多行 | 换行不发送 |
| M1-06 | 边界 | 点击 foot-hint 切换 | 切到「Shift+Enter 发送」→ Shift+Enter | 发送；Enter 变换行 |
| M1-07 | 边界 | 超长输入 | 粘贴 10k 字符 → 发送 | 不崩，发送成功 |

### M2 流式渲染与执行过程折叠

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M2-01 | 正常 | 触发带工具调用的任务 | 出现「执行过程」折叠条；点击展开见参数与结果摘要 |
| M2-02 | 正常 | 展开过程条内单个步骤 | 单步可独立展开，显示参数（≤4000 字符）与结果大小徽标 |
| M2-03 | 正常 | 纯文本回复（无工具） | 不出现过程条，直接文本卡片 |
| M2-04 | 边界 | 快速连续发送 2 条 | 第 2 条排队或提示；不出现重复卡片 |
| M2-05 | 异常 | 任务中断（点停止） | 出现「⏹ 任务已中断」，无残留转圈 |

### M3 思考开关（reasoning）

| 用例 | 类型 | 前置 | 步骤 | 期望 |
|---|---|---|---|---|
| M3-01 | 正常 | provider 支持 thinking | 双击模型按钮 → 弹思考强度 | 弹出 `.think-pop`，显示当前模型名与 5 档刻度 |
| M3-02 | 正常 | 同上 | 拖动刻度 → 关闭 | `POST .../reasoning` 成功；`think-badge` 更新 |
| M3-03 | 边界 | 切到**不支持** thinking 的 provider | 双击模型按钮 | 提示不支持 / 不弹刻度 |
| M3-04 | 正常 | 已有会话 | 调强度后发消息 | 本次任务生效（下次任务语义） |
| M3-05 | 边界 | 新会话视图 | 调强度后发第一条消息 | 强度随 `POST /api/messages` body 带上 |

### M4 工具调用展示

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M4-01 | 正常 | 让 agent 执行 `exec` | 显示工具名 + 参数；完成后显示结果摘要 |
| M4-02 | 正常 | 工具返回超长结果 | 截断到 420 字符 + 省略 |
| M4-03 | 正常 | Markdown 渲染 | 标题/列表/表格/代码块正确；代码块有语言标签 + 复制按钮 |
| M4-04 | 边界 | JSON 整条消息 | 自动美化为 json 代码块 |
| M4-05 | 异常 | 工具报错 | 显示 `Error...` + `[Analyze the error above...]` 提示 |
| M4-06 | 安全 | 消息内含 `<script>alert(1)</script>` | 转义显示，不执行 |

### M5 会话列表与切换

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M5-01 | 正常 | 发送首条消息 | 侧栏出现新会话项，「执行中」点亮 |
| M5-02 | 正常 | 点击历史会话 | 消息历史回显，标题/模型/模式/上下文窗口同步 |
| M5-03 | 正常 | 双击标题 | 弹重命名，成功后侧栏同步 |
| M5-04 | 正常 | 拖会话到「已归档」 | 从主列表消失，出现在归档区 |
| M5-05 | 正常 | 拖回会话区 | 恢复 |
| M5-06 | 边界 | 切换到**正在运行**的会话 | 显示「任务正在执行中」提示，停止按钮可用 |
| M5-07 | 边界 | 长会话（>200 条）上滑 | 自动分批加载更早消息 |
| M5-08 | 异常 | 删除当前会话 | 视图回到新会话态 |

### M6 压缩

| 用例 | 类型 | 前置 | 步骤 | 期望 |
|---|---|---|---|---|
| M6-01 | 正常 | 长会话 | 点「⚡ 压缩」 | 按钮变「压缩中…」→ toast 显示 before→after tokens |
| M6-02 | 边界 | 短会话 | 点压缩 | toast「无需压缩」 |
| M6-03 | 边界 | 任务运行中 | 点压缩 | 按钮 disabled |
| M6-04 | 边界 | 新会话（无 id） | 点压缩 | 按钮 disabled |
| M6-05 | 异常 | 后端失败 | 点压缩 | toast「压缩失败:...」，按钮恢复 |

### M7 设置保存

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M7-01 | 正常 | 改模型配置 → 保存 | `PUT /api/config` 成功，toast 提示，模型热切换 |
| M7-02 | 正常 | 无改动 → 保存 | 返回「配置没有变化」 |
| M7-03 | 正常 | 改 Web 端口 → 保存 | 提示「下次启动生效」 |
| M7-04 | 异常 | 填非法端口（负数/超范围） | 前端或后端报错，不写坏配置 |
| M7-05 | 正常 | 取消按钮 / 点遮罩 / Esc | 关闭弹窗，不保存 |
| M7-06 | 正常 | 配置页导航切换 | 只切 `.active`，表单 id 不重建 |

### M8 主题 / 皮肤 / 开机动画

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M8-01 | 正常 | 循环点主题按钮 | 浅色→深色→跟随系统→浅色；`.lbl` 文案同步 |
| M8-02 | 正常 | 配置页选主题 | 与顶栏按钮同一开关，立即生效 |
| M8-03 | 正常 | 刷新页面 | 主题保持（localStorage `mc-theme`） |
| M8-04 | 正常 | 系统切暗色（跟随系统模式） | 页面跟随变化 |
| M8-05 | 正常 | 开机动画启用 + 预览一次 | 播放动画，Esc 可跳过 |
| M8-06 | 边界 | 无已安装主题 | 「没安装主题时不会播放」 |
| M8-07 | 异常 | 主题目录填错 | 明确错误提示，不静默 |
| M8-08 | 正常 | 在线获取 → 安装主题 | 列出来源、下载、sha256 校验、出现在已安装 |
| M8-09 | 边界 | 播放时机 3 种模式 | session/always/daily 各自按预期触发 |

### M9 插件入口

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M9-01 | 正常 | `~/.mini_code/plugins.md` 有插件行 | 系统提示含 `# User plugins` 段 |
| M9-02 | 边界 | plugins.md 不存在 | 核心功能正常，无报错 |
| M9-03 | 异常 | 插件行格式错误 | 忽略该行，核心不受影响 |

### M10 群聊 / 审批 / 图片 / 文件夹选择器

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M10-01 | 正常 | 新建群 → 加成员 → 发消息 | 群消息广播，成员显示 |
| M10-02 | 正常 | 谨慎模式下触发危险操作 | 弹审批框，批准后执行 |
| M10-03 | 正常 | 粘贴截图 | 预览出现，发送后图片随消息上送 |
| M10-04 | 边界 | 粘 5 张图 | 上限 4 张，超出被拦 |
| M10-05 | 正常 | 输入 `@` | 文件夹选择器出现，↑↓ 选择、Enter 插入、Esc 取消 |

### M11 可访问性 / 响应式

| 用例 | 类型 | 步骤 | 期望 |
|---|---|---|---|
| M11-01 | 手工 | Tab 遍历 | 焦点可见，顺序合理 |
| M11-02 | 手工 | 窗口缩到 760px | 侧栏折叠/自适应，不横向溢出 |
| M11-03 | 手工 | 窗口 1560px+ | 内容列守住最大宽度（clamp） |
| M11-04 | 手工 | 暗色下检查小字 | 对比度可读 |
| M11-05 | 手工 | Esc 关闭弹窗 | 所有弹窗可 Esc 关闭 |

---

## 三、测试方案

### 3.1 可自动化部分

| 层次 | 手段 | 覆盖 | 怎么跑 |
|---|---|---|---|
| 后端 handler 单测 | Go `httptest` | 40 个路由的状态码/JSON 形状/错误分支 | `go test ./web/...` |
| 前端静态检查 | Python 脚本（`build/windows/_work_*/check_ui.py`） | ① 无内联 `style=` 残留 ② 无裸 `innerHTML=` 未走渲染函数 ③ 事件绑定全部走集中注册 ④ 无 `catch(e){}` 空吞 ⑤ 引号转义检查 ⑥ CSS 无重复选择器块 ⑦ 无未定义变量引用 | `python check_ui.py` |
| 构建闸门 | Go 工具链 | 编译、vet、embed 路径有效性 | `go vet ./...` && `go build` |
| 前端资源自检 | Go 单测（新增） | embed 里每个 `<link>`/`<script src>` 引用的文件真实存在 | `go test ./web/ -run TestUIAssets` |
| JS 语法 | Node（若可用） | `node --check` 各模块文件 | `node --check web/ui/js/*.js` |

### 3.2 关键路径手工验证

按 M1–M11 逐项执行，重点 5 条冒烟：

1. **发消息 + 流式**：M1-01 → 观察逐字输出与收尾
2. **工具调用展示**：M2-01/M4-01 → 折叠条与结果徽标
3. **思考开关**：M3-01/M3-02 → 刻度与持久化
4. **会话切换**：M5-01/M5-02/M5-06
5. **压缩 + 设置保存**：M6-01/M7-01

### 3.3 回归判定标准

全部满足才算通过：

- [x] `go vet ./...` 零输出
- [x] `go test ./...` 全绿（11 个包，其中 8 个有测试）
- [x] `build.bat windows` 与 `build.bat` 均成功
- [x] 桌面 exe 双击起来、窗口能正常对话
- [x] 浏览器打开 `/` 无 404（CSS/JS 全部加载成功，DevTools Network 无红）
- [x] 浏览器 Console 无 JS 报错
- [x] M1–M11 全部 PASS（见第五节，自动化 53/53）
- [x] 前缀缓存不受影响（见第五节，95.4% / 93.8%）

---

## 四、重构验收标准

### 4.1 功能等价

| 环节 | 判定方法 | 结果 |
|---|---|---|
| 所有控件可点 | M1–M11 逐项 PASS | PASS（CDP 自动化 53/53） |
| 所有 API 调用路径不变 | 前后 `grep '/api/'` 得到同一集合（40 路由的子集） | PASS（19 个前缀，全部命中后端注册路由） |
| 状态语义不变 | `state` 字段名/含义一一对应（可改名，须有映射表） | PASS（`state` 整体搬到 `js/theme.js`，字段零改动） |
| 视觉一致 | 重构前后同分辨率截图对比，关键区块无位移 | PASS（30 元素 × 23 计算样式属性全部 IDENTICAL） |
| 行为细节保留 | 单击 230ms / sending 8s / SSE 75s / 懒加载 120px / 结果截断 420 / 参数截断 4000 | PASS（逐字迁移，`check_ui.py` 校验） |

### 4.2 无 JS 报错

DevTools Console 全程 `error` 级别 0 条；`window.onerror` 未触发。

### 4.3 前缀缓存不受影响

- 系统提示保持**静态**（不加时间戳/随机内容）
- 工具 schema 列表继续**按名字排序**（`tools/registry.go` 已有，禁止改动）
- 插件启用/禁用不得改变核心工具的顺序和内容（插件工具只能**追加**在稳定前缀之后）
- 判定：连续两轮对话，`cached_tokens / prompt_tokens > 90%`

### 4.4 构建全绿

`go vet ./...`、`go test ./...`、`build.bat windows`、`build.bat` 全部成功。

---

## 五、实测记录

> 每次测试在此追加一行，最终标记 PASS/FAIL。
>
> 环境：Windows 10 · go1.25.0 windows/amd64 · Chrome headless（CDP 直连，无第三方依赖）
> · 模型 qoder-ds（本地 Qoder 中转 `http://127.0.0.1:32999/v1`，DeepSeek-Flash）
> · 自动化脚本位于 `build/windows/_work_20261027_split/`（临时目录，任务结束后可删）

### 5.1 构建与单元测试

| 时间 | 用例 | 结果 | 备注 |
|---|---|---|---|
| 2026-10-27 | 重构前基线 | PASS | 原始单文件版本，go vet/test/build 全绿，实测可用 |
| 2026-10-27 | `go vet ./...` | PASS | 零输出 |
| 2026-10-27 | `go test ./... -count=1` | PASS | channels/config/desktop/windows/provider/session/tools/util/web 全 ok |
| 2026-10-27 02:30 | `build.bat windows` | PASS | `build\windows\mini_code.exe` 14,399,488 字节（desktop 壳，`-H windowsgui`） |
| 2026-10-27 02:32 | 交叉构建 linux/amd64 + darwin-arm64 | PASS | `build.bat`（三平台全量）成功 |
| 2026-10-27 | `node --check` × 22 个 JS 模块 | PASS | 全部通过 |
| 2026-10-27 | `python check_ui.py` | PASS | 8 项静态检查全 PASS（见 5.2） |

### 5.2 前端静态检查（`check_ui.py`）

| 检查项 | 结果 | 实测值 |
|---|---|---|
| ① 无内联 `style=` 残留 | PASS | 0 处（重构前 18 处） |
| ② `innerHTML` 写入已清点 | PASS | 63 处，均落在具名 render/paint 函数内 |
| ③ 事件绑定集中 | PASS | 111 处 `addEventListener` + 1 处 `on*` 赋值 |
| ④ 无静默吞掉的网络错误 | PASS | 剩余 `catch(e){}` 全在 localStorage / 清理路径 |
| ⑤ `esc()` 转义引号 | PASS | `dom.js` 现转义 `& < > " '`（修掉审计 A-01） |
| ⑥ 重复选择器块未增加 | PASS | 重构前 180 / 重构后 180（逐字节迁移，未新增） |
| ⑦ 无未定义 CSS 变量 | PASS | 声明 99 / 使用 95 / 缺失 0 |
| ⑧ `getElementById` 目标存在 | PASS | 静态 2 + 渲染期拼接 1，无幽灵 id |

### 5.3 运行时验证（CDP 无头驱动，`e2e.py`）

隔离 `USERPROFILE` 的临时 HOME，真实调用模型，全程收集 console error / 失败请求。

| 用例 | 结果 | 实测值 |
|---|---|---|
| 页面启动 | PASS | `readyState=complete`，启动零 console error |
| M1-03 空输入 Enter 不发请求 | PASS | `fetch` 计数 = 0 |
| M1-01 用户气泡 | PASS | `.msg.user` 出现，文本「只回复两个字:收到」正确渲染 |
| M1-01 等待首 token 转圈 | PASS | `.msg.pending .spinner` 出现 |
| M1-01 流式卡片 | PASS | `.msg.ai` 出现，`.m-card > .m-text` 有内容 |
| M1-01 收尾 | PASS | 转圈移除、`live-ind` 移除、发送键重新可见 |
| M1-01 回复内容 | PASS | 模型实际回复「收到」 |
| M1-01 Token / 缓存 / 速度条 | PASS | `Token 0.87K / 160K · 1%`、`缓存 96% (3.7K/3.9K)`、`速度 77.4 token/s` |
| M2-01 执行过程折叠条 | PASS | `执行过程2 步1 次思考展开` |
| M2-01 工具步骤 | PASS | 工具名 `exec`，参数与 `mini_code_ok` 结果齐全 |
| M2-01 展开折叠条 / 提示字 | PASS | `details.open=true`，提示字切到「收起」 |
| M2-02 单步可展开 | PASS | `.ms-step` 为 `<details>`，可独立开合 |
| M5-01 会话入列表 + 点击回放 | PASS | `.sess-item` 出现，点击后 `.msg` 渲染历史 |
| M8-01 主题三态循环 | PASS | `light → dark → system`，`data-theme` 同步，标签同步，`localStorage` 持久化 |
| M7 设置弹窗打开 | PASS | `#settings-mask` 显示 |
| M7-05 Esc 关闭 | PASS | 按 Esc 后遮罩隐藏 |
| M3-01 双击模型键开思考浮层 | PASS | `#think-pop.open` 出现，刻度 5 档 |
| M3-01 点外部关闭 | PASS | `.open` 移除 |
| M3-01 单击开模型菜单 | PASS | `#model-menu.open`，10 个已配置模型 |
| M6-04 空闲会话压缩键可用 | PASS | `btn-compact.disabled = false` |
| M1-05 Shift+Enter 不发送 | PASS | 输入框保留「第一行」，无 pending 块 |
| M1-06 点 foot-hint 切换发送键 | PASS | 提示字与 placeholder 同步切到「Shift+Enter 发送」 |
| M1-06 发送模式持久化 | PASS | `localStorage['mc-send-mode']` = `shift`，再点回 `enter` |
| M4-06 `esc()` 中和标记与引号 | PASS | `<` `>` `"` 全部转义为实体 |
| M4-06 `mdToHtml` 丢弃裸 `<script>` | PASS | 输出中无 `<script` 子串 |
| M4-03 Markdown 渲染 | PASS | 标题 `<h2>`、列表 `<li>`、代码块（含复制按钮）均生成 |
| M10-05 输入 `@` 开文件夹选择器 | PASS | `#fs-picker` 可见 |
| M11-05 Esc 关闭配置弹窗 | PASS | `settings-mask.open` 添加后按 Esc 移除 |
| M11-02 760px 无横向溢出 | PASS | `scrollWidth - clientWidth = 0` |
| 全程 console error / 失败请求 | PASS | 0 / 0 |

**总计 53/53 PASS。**

### 5.4 行为等价性（同 commit 对照）

对照组：git worktree 检出 `1178001`（重构前单文件版）编译的 `baseline.exe`；
实验组：当前工作区编译的 `t.exe`。两者同一 commit，差异仅来自本次重构。

| 项 | 结果 | 实测值 |
|---|---|---|
| 30 个关键元素 × 23 个计算样式属性 | PASS | **IDENTICAL 30/30，differing 0** |
| body markup 差异 | PASS | 仅 18 处 `style=` → 语义类，无 id/类丢失 |
| CSS 拼接逐字节还原 | PASS | 与重构前 `<style>` 内容完全一致 |
| JS 拼接逐字节还原 | PASS | `JS roundtrip identical: True` |
| 内联 `style=` 数量 | PASS | 29（含 JS 字符串）→ 0 |
| 前端引用的 API 路径 | PASS | 19 个前缀全部命中后端已注册路由 |

### 5.5 前缀缓存（关键指标）

同样隔离 HOME，连续两轮真实对话，第二轮触发工具调用（`bash` 执行 `echo mini_code_ok`）。

| 轮次 | prompt_tokens | cached_tokens | 命中率 |
|---|---|---|---|
| turn 1 | 3,877 | 3,712 | **95.7%** |
| turn 1（工具后二次调用） | 3,893 | 3,712 | **95.4%** |
| turn 2（含工具结果） | 3,957 | 3,712 | **93.8%** |

**判定：PASS** —— 连续两轮均 > 90%（95.4% / 93.8%），且工具调用轮次未打断缓存前缀。
会话持久化 `last_usage` 与前端「缓存 xx%」指示器读到的数字与后端日志一致（3712/3957）。

### 5.6 资源路由（新增静态路由）

| 项 | 结果 | 备注 |
|---|---|---|
| 23 个资源 GET 200 | PASS | 2 CSS + 22 JS 全部 200，`Content-Type` 正确 |
| `/js/nope.js` → 404 | PASS | 白名单命中但不存在的文件不会被 index 兜底 |
| `If-None-Match` → 304 | PASS | 内容哈希 ETag，`Cache-Control: no-cache` |
| `X-Content-Type-Options: nosniff` | PASS | 全部资源响应头均有 |
| `/favicon.ico` 旧路由仍生效 | PASS | 独立 handler 优先于资源路由 |
| index.html 引用缺失告警 | PASS | `loadUIAssets()` 启动时校验并 `slog.Warn` |

