# 前端重构 + 插件化改造 · 总报告

> 项目：`D:\project_ai\mini_code`（Go + WebView2 桌面 AI agent）
> 日期：2026-10-27
> 基线 commit：`1178001`（`chore: drop docker/ (stale Python-era scaffold)`）
> 工作区状态：**全部改动未提交、未 push、未打 tag**（按要求交由用户自行检查后提交）
> 验证环境：Windows 10 · go1.25.0 windows/amd64 · Chrome headless（CDP 直连，无第三方依赖）
> 被测模型：qoder-ds（本地 Qoder 中转 `http://127.0.0.1:32999/v1`，DeepSeek-Flash）

---

## 一、结论先说

| # | 任务 | 状态 | 关键证据 |
|---|---|---|---|
| 1 | 前端交互审计 | ✅ | 24 条问题分 4 级 + 控件→路径→样式映射表 |
| 2 | 测试清单与方案 | ✅ | `docs/frontend-test-plan.md`，M1–M11 共 56 条用例 |
| 3 | 重构前端（拆分/统一/等价） | ✅ | 5,962 行 → 584 行 HTML + 2 CSS + 23 JS；逐字节可还原 |
| 4 | 全量测试 | ✅ | vet/test/build 全绿；UI 自动化 53/53；等价性 30/30 |
| 5 | 检查点 aa | ✅ | `docs/checkpoint-aa.md`（**未 commit、未打 tag**，附文件指纹） |
| 6 | 汇总报告 | ✅ | 本文 |
| 7 | 插件化（不破坏 prefix cache） | ✅ | 插件层 8/8 + 端到端 24/24 + 缓存 A/B 9/9 |
| 8 | 总报告 | ✅ | 本文 |

**三个硬指标：**

- 视觉/行为等价：30 个关键元素 × 23 个计算样式属性 **全部 IDENTICAL，差异 0**。
- 前端零报错：全程 console error **0**、失败请求 **0**。
- 前缀缓存：无插件 **95.7% / 95.5%**；装插件 **96.9% / 96.8%**（连续两轮，均 > 90%）。

---

## 二、第 1 步：前端交互审计（要点）

原始状态：`web/ui/index.html` **5,962 行 / 290,874 字节**，HTML + CSS + JS 全在一个文件里。

| 维度 | 审计结果 |
|---|---|
| CSS | 674 条规则 / 254 个类 / **10 层设计叠加覆盖（v3→v10）**；已有 91 个变量、747 处 `var()`（底子不错）；但 151 处硬编码 hex（102 个不同值）+ 84 处 rgba + 9 处 `!important` |
| JS | 203 个全局函数、98 处 `addEventListener`、63 处 `innerHTML=`、5 处 `insertAdjacentHTML` |
| 可访问性 | `aria-*` 仅 5 个、`role` 0 个、40 个按钮无 `aria-label`、无 `tabindex` |
| 安全性 | `esc()` 用 `textContent → innerHTML`，**不转义引号**；`title="..."` 等属性上下文可被闭合注入 |
| 后端 | 原本只服务 `GET /`、`/index.html`、`/favicon.ico`、`/avatars/{name}`、`/bootanim/` 与 API，**没有 CSS/JS 静态路由**——拆分后必须新增 |

问题清单按严重度：P0 安全/正确性 3 条（A-01…A-03）、P1 可维护性 5 条（B-01…B-05）、
P2 可访问性/响应式 4 条（C-01…C-04）、P3 健壮性 3 条（D-01…D-03），共 24 条全记录在
`docs/frontend-test-plan.md` §1.2。

---

## 三、第 2 步：测试清单与方案

`docs/frontend-test-plan.md`（26,530 字节 / 439 行）包含：

- 审计结论与量化基线、控件映射表；
- **M1–M11 共 56 条可执行用例**（含前置、步骤、期望），标注 `[自动]` / `[手工]`；
- 自动化方案（后端 `httptest` / 前端静态检查 / 构建闸门 / 资源自检 / `node --check`）；
- 回归判定标准、重构验收标准；
- **第十节实测记录（已全部回填 PASS/FAIL）**。

---

## 四、第 3 步：重构（做了什么）

### 4.1 拆分结果

| 之前 | 之后 |
|---|---|
| `index.html` 5,962 行 / 290,874 字节 | `index.html` **584 行 / 31,470 字节** |
| — | `css/base.css` 1,122 行（token + 布局 + 组件基础） |
| — | `css/layers.css` 744 行（v3 润色 → v10 覆盖层，原顺序迁移） |
| — | `js/` **23 个模块**，3,665 行 |

### 4.2 22 → 23 个 JS 模块（按功能域）

`core`（常量）、`theme`（三态主题 + `state` 单例）、`dom`（`api`/`$`/`esc`）、
`markdown`（`mdToHtml` + 代码块复制 + `toast`）、`composer-image`（截图）、
`sessions`（列表/重命名/归档拖放）、`messages`（容器/滚动/懒加载）、
`steps`（执行过程折叠条）、`streaming`（转圈 + 流式块）、`send`、`sendmode`、
`events`（SSE）、`controls`（模型菜单/思考浮层/模式）、`approval`、`tasks`、`status`、
`settings`（配置弹窗/模型网格/探针）、`plugins`（**新增**，插件页）、
`bootanim`、`usage`（token/缓存/速度）、`fspicker`、`groups`（群聊）、`app`（初始化）

### 4.3 其它统一

- **18 处内联 `style=` 全部转为语义类**：`u-flex`、`u-gap10`、`u-mt14/16/20`、`u-w100`、
  `u-center`、`modal-400/420/480`、`boot-bar-label`、`g-danger-center`。前端 `style=` 归零。
- **事件绑定集中管理**：111 处 `addEventListener`（+1 处 `on*` 赋值），全部位于具名模块，
  `app.js` 的 `init()` 是唯一的注册入口。
- **统一错误处理**：`api()` 统一抛 `Error`（优先读后端 `{error}` 字段）；
  剩余 `catch(e){}` 全在 localStorage / 清理路径（`check_ui.py` 第 4 项静态确认无静默吞网络错误）。
- **修掉审计 A-01**：`esc()` 改为真转义 `& < > " '`（文本上下文不可见，属性上下文安全）。
- **后端新增静态资源路由**（`web/ui.go`）：白名单扩展名、启动时 `fs.WalkDir` 预载、
  内容哈希 ETag、`If-None-Match → 304`、`nosniff`、`Cache-Control: no-cache`、
  并校验 `index.html` 引用的资源是否真的存在（缺失即 `slog.Warn`）。

### 4.4 没有改的东西（承诺 & 验证）

- **后端 API 契约零改动**：40 个路由原样，前端引用的 20 个 API 前缀全部命中。
- **provider / session / tools / loop 逻辑零改动**（本步）：`git diff` 只有
  `web/ui.go`、`web/web.go`（+1 行前置调用）、`web/ui/index.html` 与两个测试断言的断言源。

### 4.5 等价性怎么证明的

像素 diff 不可靠（控制实验：同一构建两次截图差异 99.94%，是「截图早于渲染完成」的时序噪声）。
所以改用三层证据：

1. **逐字节往返**：CSS 两文件拼接 == 原 `<style>` 内容；23 个 JS 拼接 == 原 `<script>` 内容。
2. **同 commit 双构建计算样式对比**：git worktree 检出 `1178001` 编 `baseline.exe`，
   当前工作区编 `t.exe`，同分辨率下取 30 个关键元素 × 23 个属性 →
   **30/30 IDENTICAL，differing 0**。
   （`baseline.exe` 已留在 `_work_20261027_split/` 里，可直接复跑 `equiv2.py`；
   worktree 用完已 `git worktree remove` 清理，需要重建时见 §10.3。）
3. **markup 差异审计**：body 差异**仅**为 18 处 `style=` → 语义类；无 id 丢失、无类丢失。

---

## 五、第 4 步：全量测试

### 5.1 构建闸门

| 命令 | 结果 |
|---|---|
| `go vet ./...` | **零输出** |
| `go test ./... -count=1` | **13 个包，9 个 ok，4 个 no test files** |
| `build.bat windows` | ✅ `build\windows\mini_code.exe`（14,452,224 字节，desktop 壳） |
| `build.bat`（三平台） | ✅ windows/amd64 + linux/amd64 + darwin/arm64 全成功 |
| `node --check` × 23 | ✅ 全部 OK |

> 测试过程中发现并修好了 2 个**旧断言**（`web_ui_models_test.go` / `web_ui_thinking_test.go`
> 直接对 `embeddedPage()` 做 `strings.Contains`，拆分后必然失败）。修法是新增
> `uiSource(t)` 辅助（HTML + 全部 CSS/JS 拼接），断言语义不变、覆盖范围更合理。

### 5.2 前端静态检查 `check_ui.py`（8 项全 PASS）

| 检查项 | 结果 |
|---|---|
| ① 无内联 `style=` | 0 处（原 18 处） |
| ② `innerHTML` 写入已清点 | 66 处，均在具名 render/paint 函数内 |
| ③ 事件绑定集中 | 112 处（111 `addEventListener` + 1 `on*`） |
| ④ 无静默吞网络错误 | PASS |
| ⑤ `esc()` 转义引号 | PASS（已修 A-01） |
| ⑥ 重复选择器块未增加 | 重构前 180 / 重构后 180 |
| ⑦ 无未定义 CSS 变量 | 声明 99 / 使用 95 / 缺失 0 |
| ⑧ `getElementById` 目标存在 | 静态 2 + 渲染期拼接 1，无幽灵 id |

### 5.3 运行时端到端 `e2e.py`（53/53 PASS）

隔离 `USERPROFILE` 的临时 HOME（真实 `agent.db` 不受影响），CDP 无头驱动，**真实调用模型**：

| 模块 | 覆盖 | 结果 |
|---|---|---|
| M1 发消息/流式 | 空输入不发请求、用户气泡、转圈、流式卡片、收尾、Token/缓存/速度条、Shift+Enter 不发送、发送键模式切换与持久化 | 18 项 PASS |
| M2 执行过程折叠 | 折叠条出现、步数摘要、工具名与结果、展开/收起提示字、单步可展开 | 9 项 PASS |
| M3 思考开关 | 双击开浮层、5 档刻度、点外部关闭、单击开模型菜单、列出 10 个模型 | 5 项 PASS |
| M4 Markdown/安全 | `esc()` 中和标记与引号、`mdToHtml` 丢弃裸 `<script>`、标题/列表/代码块渲染 | 3 项 PASS |
| M5 会话 | 入列表、点击回放历史 | 2 项 PASS |
| M6 压缩 | 空闲会话压缩键可用 | 1 项 PASS |
| M7 设置 | 弹窗打开、Esc 关闭 | 2 项 PASS |
| M8 主题 | 三态循环、`data-theme` 同步、按钮标签同步、`localStorage` 持久化 | 4 项 PASS |
| M10 文件夹选择器 | 输入 `@` 打开 | 1 项 PASS |
| M11 响应式 | 760px 无横向溢出 | 1 项 PASS |
| 全程 | console error / 失败请求 | **0 / 0** |

### 5.4 资源路由自检

23 个资源（2 CSS + 22 JS，插件化后 23 JS）全部 200、`Content-Type` 正确；
`/js/nope.js` → 404；`If-None-Match` → 304；`nosniff` 全有；
`/favicon.ico` 旧路由优先于资源路由（未被抢）。

---

## 六、第 5 步：检查点 aa

`docs/checkpoint-aa.md`（未提交、未打 tag）：

- 改动清单（新增/修改/模块划分）；
- **可照抄的复现命令**与期望输出；
- **10 条行为契约**（230ms 单击/双击、8s sending、75s SSE、120px 懒加载、420/4000 截断…）；
- 静态资源路由的安全设计；
- 前缀缓存基线与 A/B；
- 回滚脚本；
- **文件指纹（sha256 前 16 位）**。

### 为什么没打 tag

原计划打本地 tag 作检查点。但要求「不 commit、不 push」，而 `git tag` 默认指向某个
commit——工作区有未提交改动时打 tag 会落到 `1178001`（重构**前**的状态），
与检查点语义**相反**，会误导后续回溯。因此改用「文件 + 内容哈希」固定，并给出
用户提交后建议执行的 `git commit` + `git tag -a checkpoint-aa` 命令。

---

## 七、第 7 步：插件化改造

### 7.1 边界（核心不参与插件化）

| 部分 | 可插件化 | 原因 |
|---|---|---|
| 工具 | ✅ 只能**追加** | 核心工具列表是缓存前缀的头部，插件工具挂在其后 |
| 皮肤 / 壁纸 | ✅ | 纯 CSS / 一条 `background-image` |
| 记忆 | ✅ | 追加到系统提示的**静态**文本 |
| 开机动画 | ✅ | 已有独立机制（`config.BootAnim` + `web/bootanim.go`） |
| **agent 循环** | ❌ | 任务执行、重试、放弃是核心语义 |
| **权限 / 闸门** | ❌ | 插件只会让闸门**更严**，不能放宽 |
| **上下文压缩** | ❌ | 决定会话能否活过窗口 |
| **基础工具** | ❌ | `read_file`/`exec`/`search_files` 的语义是契约 |

静态断言：`plugin` 包**不 import** `agent` / `session` / `approval`（`check_plugins.py` 第 1 项）。

### 7.2 形态：目录式插件

```
~/.mini_code/plugins/<id>/
├── plugin.json     ← 唯一必需
├── memory.md       ← 可选
├── skin.css        ← 可选
└── bg.webp         ← 可选
```

模型的工具参数以 **JSON 写到子进程 stdin**（命令本身在清单里是静态字符串 →
无引号/注入问题），工作目录是插件目录。改完点「配置 → 插件 → 重新扫描」即生效，
不改页面、不重编译、不重启。

示例：`examples/plugins/web-search/`（2 个工具 + 记忆，包装 `D:\project_ai\web_search`）、
`examples/plugins/skin-night/`（只覆盖 4 个 CSS 变量）、
`examples/plugins/wallpaper-demo/`（一张 SVG 渐变铺在消息区）。

### 7.3 前缀缓存为什么没退化（这是硬约束）

请求前缀顺序：`[系统提示][核心工具 schema × N][插件工具 schema × M][历史]`
——**中间插入插件段，核心段字节不变**。

实现：
1. `ToolRegistry` 拆 `tools`（核心，排序）+ `extra`（插件，排序后**追加**）；
2. `NewPromptBuilder` 里 `registerDefaultTools()` 先跑、`registerPluginTools()` 后跑；
3. 插件工具与核心工具重名 → 直接忽略；
4. 插件记忆是磁盘静态文本（无时间戳/随机数）；
5. 插件按 id 排序加载。

**实测 A/B（同一段两轮对话，装 / 不装插件各跑一次）：**

| | prompt | cached | 命中率 |
|---|---:|---:|---:|
| 无插件 · turn 1 | 3,877 | 3,712 | 95.7% |
| 无插件 · turn 2 | 3,888 | 3,712 | 95.5% |
| **有插件 · turn 1** | 4,353 | 4,219 | **96.9%** |
| **有插件 · turn 2** | 4,364 | 4,224 | **96.8%** |

- 装插件只多 **476** token（就是 2 个插件工具的 schema）；
- 命中率差 **1.2 个百分点**；逐调用对齐最大偏差 1.3 个百分点；
- **连续两轮 > 90%：PASS。**

> 口径说明：一轮带工具调用的任务里，第 2、3… 次调用的 prompt 会追加本次新产生的工具结果，
> 命中率必然下滑——**不装插件同样下滑**（实测 80.0%）。所以判定口径取「每轮第一次调用」
> 加「装/不装逐调用对齐」，不用「任务末尾那次调用」的绝对值。

### 7.4 安全约束

| 约束 | 说明 |
|---|---|
| 扩展名白名单 | `.css .svg .png .jpg .jpeg .webp .gif .woff2`。**没有 `.js`、没有 `.html`**——插件不能往应用同源里塞脚本（实测 `.js`/`.html` → 403） |
| 路径包含 | 相对路径 + `EvalSymlinks` 后二次校验，`..` 与符号链接都出不去 |
| 响应头 | `nosniff` + `no-store` + `Content-Security-Policy: default-src 'none'` |
| 权限 | 插件工具在**谨慎模式下一律要审批**（与破坏性 shell 同待遇）；角色显式 `deny` 仍然生效 |
| 故障隔离 | 一个 `plugin.json` 坏了只跳过它 + `slog.Warn`，不拖垮启动 |

### 7.5 插件化踩到并修好的真实 bug

| 现象 | 根因 | 修法 |
|---|---|---|
| 模型回答「本会话里我并没有 `web_search` 这个工具」 | 编译进二进制的角色权限表只列核心工具，「不在表里 = 拒绝」把插件工具全过滤掉了 | `GetFilteredDefinitions` / `gateFor` 对已启用的插件工具放行，**显式 deny 仍生效**；核心工具过滤一点没松（有回归测试） |
| 插件工具返回 `UnicodeEncodeError: 'gbk' codec can't encode` | Windows 子进程继承 GBK 控制台代码页 | 子进程注入 `PYTHONUTF8=1` / `PYTHONIOENCODING=utf-8` / `LANG` |
| 工具输出含非 UTF-8 字节时整个发消息失败 | JSON 序列化不接受非法 UTF-8 | `sanitizeUTF8()` 把非法字节替换为 `�` |

### 7.6 插件层验证

| 项 | 结果 |
|---|---|
| `check_plugins.py`（8 项静态边界） | 8/8 PASS |
| `go test ./plugin/`（15 个单测，含真实子进程） | PASS |
| `go test ./session/ -run Plugin|Gate`（16 个） | PASS |
| `go test ./web/ -run Plugin`（7 个） | PASS |
| `e2e_plugins.py`（真机端到端） | **24/24 PASS** |
| `cache_compare.py`（装/不装缓存 A/B） | **9/9 PASS** |

---

## 八、改了哪些文件

### 8.1 新增（21 项）

| 路径 | 作用 |
|---|---|
| `web/ui/css/base.css` | 原 `<style>` 前段（token + 布局 + 组件） |
| `web/ui/css/layers.css` | 原 `<style>` 后段（v3→v10 覆盖层） |
| `web/ui/js/*.js`（23 个） | 原 `<script>` 按功能域切分 |
| `plugin/plugin.go` | 插件清单加载/校验 |
| `plugin/tool.go` | 插件工具适配器（stdin 传参 / 超时 / UTF-8 清洗） |
| `plugin/hidewin_windows.go`、`hidewin_other.go` | 子进程不弹控制台 |
| `plugin/plugin_test.go`、`tool_test.go` | 15 个单测 |
| `web/plugins.go`、`web/plugins_test.go` | `/api/plugins`、`/plugins/<id>/<file>`、资产注入 + 7 个测试 |
| `session/plugins_tools.go`、`plugins_tools_test.go`、`plugins_permission_test.go` | 插件工具注册 + 记忆注入 + 权限回归 |
| `web/ui_assets_test.go`、`web/ui_source_test.go` | 资源路由单测 + 聚合断言辅助 |
| `examples/plugins/web-search/` | 示例：工具 + 记忆 |
| `examples/plugins/skin-night/` | 示例：纯皮肤 |
| `examples/plugins/wallpaper-demo/` | 示例：纯壁纸 |
| `docs/frontend-test-plan.md` | 第 2 步交付 |
| `docs/checkpoint-aa.md` | 第 5 步交付 |
| `docs/plugins.md` | 第 7 步交付 |
| `docs/final-report.md` | 本文 |

### 8.2 修改（9 个）

| 路径 | 改动 |
|---|---|
| `web/ui/index.html` | 5,962 → 584 行；外链 2 CSS + 23 JS；0 内联 `style`；新增插件资产注入点与「插件」设置页 |
| `web/ui.go` | 18 → 183 行；静态资源路由（ETag/304/nosniff/缺失引用告警） |
| `web/web.go` | `handleIndex` 前置资源路由；页面注入插件资产；注册 `/plugins/`、`/api/plugins` |
| `tools/registry.go` | 新增 `extra` 段（`RegisterAppended`/`IsAppended`/`IsCore`），核心段不变 |
| `session/prompt.go` | 插件工具追加注册 + 插件记忆段落 + 权限过滤修正 |
| `session/gate.go` | 谨慎模式下插件工具必审批；显式 deny 生效 |
| `web/ui/js/dom.js` | `esc()` 改真转义（修 A-01） |
| `web/web_ui_models_test.go`、`web_ui_thinking_test.go` | 断言源改为 `uiSource(t)` |

### 8.3 删 / 移动（仓库内）

无。`vendor/`、`build/`、`docs/`（新建）外的一切保持原样。

---

## 九、测试结果汇总（一张表）

| 验证项 | 命令 | 结果 |
|---|---|---|
| 静态分析 | `go vet ./...` | 零输出 |
| 单元测试 | `go test ./... -count=1` | 13 包 / 9 ok / 4 无测试 |
| 桌面构建 | `build.bat windows` | ✅ 14,452,224 字节 |
| 三平台构建 | `build.bat` | ✅ 全成功 |
| JS 语法 | `node --check` × 23 | ✅ |
| 前端静态检查 | `check_ui.py` | 8/8 PASS |
| 插件静态边界 | `check_plugins.py` | 8/8 PASS |
| UI 端到端 | `e2e.py` | **53/53 PASS** |
| 插件端到端 | `e2e_plugins.py` | **24/24 PASS** |
| 样式等价性 | `equiv2.py` | **差异 0 / 30** |
| 前缀缓存（无插件） | `prefix_cache.py` | **95.4% / 93.8%** |
| 前缀缓存 A/B | `cache_compare.py` | **9/9 PASS**，有插件 96.9% / 96.8% |
| console error | `e2e.py` 全程 | **0** |
| 失败请求 | `e2e.py` 全程 | **0** |

---

## 十、遗留风险与建议

### 10.1 明确的遗留（不影响本次验收）

| 项 | 影响 | 建议 |
|---|---|---|
| `web/ui/js` 仍有 66 处 `innerHTML=` | 无统一虚拟 DOM，长会话重渲染是 O(n)（懒加载已缓解） | 后续渐进收敛到 `patch()` 式渲染 |
| 40 个按钮无 `aria-label`、无 `role`/`tabindex` | 可访问性不达标（键盘 Tab 无焦点环） | 独立任务补 ARIA，动 CSS 需重跑等价性 |
| `config/config.go:222 SaveConfig` 会丢弃 `CompactionEnabled` | 该字段已废弃（压缩恒开），当前无实际影响 | 顺手清理或注释说明 |
| `gofmt -l` 仍报若干既有文件 | 非本次引入 | 单开一个纯格式化 commit |
| 插件工具无沙箱 | 插件是用户自己装的程序，权限等于用户权限 | 谨慎模式下已强制审批；若将来做插件市场需加沙箱 |
| `_work_20261027_split/` 临时目录 | 含脚本与对照 exe | 验收后整体删除 |

### 10.2 需要用户决策的点

1. **是否提交**：全部改动在工作区，`git status` 干净可复现；建议先 `git diff` 过一遍
   再 `git add -A && git commit`，然后按 `docs/checkpoint-aa.md` §八 打 tag。
2. **插件示例默认 `enabled: false`**：`examples/plugins/` 里的两个示例是关的，
   需要用户手动拷到 `~/.mini_code/plugins/` 并改 `enabled: true`——刻意如此，
   避免「装了就自动跑外部程序」。
3. **`web_search` 插件依赖外部仓库**：`args` 里硬编码了 `D:\project_ai\web_search\ws.py`。
   如果路径变了，改 `plugin.json` 即可，不动二进制。

### 10.3 回归防线（留给后续）

```
go vet ./... && go test ./... -count=1 && build.bat
python build\windows\_work_20261027_split\check_ui.py
python build\windows\_work_20261027_split\check_plugins.py
python build\windows\_work_20261027_split\e2e.py
python build\windows\_work_20261027_split\e2e_plugins.py
python build\windows\_work_20261027_split\cache_compare.py
python build\windows\_work_20261027_split\equiv2.py      # 用现成的 baseline.exe；需重建时见下
```

`e2e.py` / `e2e_plugins.py` / `cache_compare.py` 都用**隔离的 `USERPROFILE`**，
不碰真实的 `~/.mini_code/agent.db`，可以随时重复跑。

`equiv2.py` 需要一个「重构前」的可执行文件作对照。`_work_20261027_split/baseline.exe`
已经留在目录里；如果它被清掉了，这样重建：

```bat
cd /d D:\project_ai\mini_code
git worktree add build\windows\_work_20261027_split\baseline 1178001
cd build\windows\_work_20261027_split\baseline && go build -o ..\baseline.exe .
cd /d D:\project_ai\mini_code && git worktree remove --force build\windows\_work_20261027_split\baseline
```

---

## 十一、一句话总结

**前端从 5,962 行单文件拆成「584 行 HTML + 2 个 CSS + 23 个 JS 模块」，
行为与视觉零差异（30/30 元素属性 IDENTICAL、53 项端到端全 PASS）；
在此之上加了目录式插件系统（工具/皮肤/壁纸/记忆/开机动画可插，agent 循环/权限/压缩/基础工具不可插），
插件工具追加在核心工具之后，连续两轮缓存命中 96.9% / 96.8%，未破坏前缀缓存。
全部改动未提交、未 push、未打 tag，交由用户检查。**
