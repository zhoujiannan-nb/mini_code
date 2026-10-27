# 检查点 AA — 前端拆分与插件化基线

> 记录时间：2026-10-27
> 基线 commit：`1178001` (`chore: drop docker/ (stale Python-era scaffold)`)
> 工作区状态：**改动未提交、未打 tag**（按要求，由用户自行检查后再提交）
> 构建目标：`build\windows\mini_code.exe`（desktop 壳）· `mini_code web -p <port>`（纯 Web）


---

## 一、这个检查点代表什么

前端从「单文件 5,962 行 / 290,874 字节」拆成「HTML 外壳 + 2 个 CSS + 23 个 JS 模块」，
并新增了后端静态资源路由。**行为、样式、API 契约全部未变**——已用同 commit 的双构建
计算样式对比（30 元素 × 23 属性全 IDENTICAL）与 CDP 端到端驱动（53/53 PASS）证明。

在此之上叠加了插件化改造（第 7 步，见第十节），且**没有破坏**本检查点记录的前缀缓存指标。

---

## 二、改动清单

### 2.1 新增文件

| 路径 | 作用 |
|---|---|
| `web/ui/css/base.css` | 原 `<style>` 前段：token（99 个变量）、布局、组件基础样式 |
| `web/ui/css/layers.css` | 原 `<style>` 后段：v3 设计润色 → v10 覆盖层，按原顺序迁移 |
| `web/ui/js/*.js`（23 个） | 原 `<script>` 按功能域切分，见下表 |
| `plugin/plugin.go` | 插件清单加载/校验（id、工具名、路径包含） |
| `plugin/tool.go` | 插件工具适配器（stdin 传参、超时、UTF-8 清洗） |
| `plugin/hidewin_windows.go` / `hidewin_other.go` | 子进程不弹控制台窗口 |
| `web/plugins.go` | `/api/plugins`、`/plugins/<id>/<file>` 资源路由、资产注入 |
| `session/plugins_tools.go` | 插件工具追加注册 + 插件记忆段落 |
| `examples/plugins/web-search/` | 示例：工具 + 记忆 |
| `examples/plugins/skin-night/` | 示例：纯皮肤（只覆盖 CSS 变量） |
| `examples/plugins/wallpaper-demo/` | 示例：纯壁纸（SVG 渐变） |
| `web/ui_assets_test.go` | 资源路由单测：embed 完整性、200/304/404、Content-Type、nosniff |
| `web/ui_source_test.go` | 聚合 index.html + css + js 的测试辅助（拆文件后旧断言失效的修复） |
| `docs/frontend-test-plan.md` | 第 2 步交付：审计结论 + 56 条用例 + 实测记录 |
| `docs/plugins.md` | 第 7 步交付：插件契约、缓存约束、踩过的坑 |
| `docs/checkpoint-aa.md` | 本文件 |

### 2.2 修改文件

| 路径 | 改动 |
|---|---|
| `web/ui/index.html` | 5,962 → 584 行；`<style>` 换 2 条 `@import`，`<script>` 换 23 个 `src`；18 处内联 `style=` 改语义类；新增插件资产注入点与「插件」设置页 |
| `web/ui.go` | 18 → 183 行；新增 `loadUIAssets` / `tryServeUIAsset` / `referencedAssets` |
| `web/web.go` | `handleIndex` 前置 `tryServeUIAsset`；页面注入插件资产；注册 `/plugins/` 与 `/api/plugins` |
| `tools/registry.go` | 新增 `extra` 段（`RegisterAppended` / `IsAppended` / `IsCore`），核心段排序与内容不变 |
| `session/prompt.go` | 追加 `registerPluginTools()` / `pluginMemorySection()`；`GetFilteredDefinitions` 不再误杀插件工具 |
| `session/gate.go` | 插件工具在谨慎模式下必审批；显式 deny 仍生效 |
| `web/web_ui_models_test.go` | 断言源从 `embeddedPage()` 改为 `uiSource(t)`（拆文件后行为不变） |
| `web/web_ui_thinking_test.go` | 同上，额外拼入 JS 模块正文 |
| `web/ui/js/dom.js` | `esc()` 改真转义（修掉审计 A-01 的属性上下文注入） |

### 2.3 JS 模块划分

| 模块 | 行 | 职责 |
|---|---:|---|
| `core.js` | 7 | 常量开关（`GROUP_CHAT_ENABLED` 等） |
| `theme.js` | 98 | 主题三态 + `state` 全局单例 |
| `dom.js` | 24 | `api()` / `$()` / `esc()` |
| `markdown.js` | 343 | `mdToHtml` 渲染器 + 代码块复制 + `toast` |
| `composer-image.js` | 75 | 截图粘贴 / 预览 / 上限 |
| `sessions.js` | 269 | 会话列表、渲染、重命名、归档拖放 |
| `messages.js` | 76 | 消息容器、智能滚动、历史懒加载 |
| `steps.js` | 229 | 执行过程折叠条、工具/思考/错误卡片 |
| `streaming.js` | 93 | 转圈块与流式块生命周期 |
| `send.js` | 88 | 发送 / 停止 / 按钮状态 |
| `sendmode.js` | 25 | Enter / Shift+Enter 发送模式 |
| `events.js` | 159 | SSE 连接与事件分发 |
| `controls.js` | 204 | 模型菜单、思考浮层、模式切换 |
| `approval.js` | 84 | 审批弹窗 |
| `tasks.js` | 65 | 子任务面板 |
| `status.js` | 22 | 状态胶囊 |
| `settings.js` | 415 | 配置弹窗、模型网格/编辑器、探针 |
| `plugins.js` | 62 | 插件列表页（读 `/api/plugins`、重新扫描） |
| `bootanim.js` | 194 | 开机动画主题 |
| `usage.js` | 231 | token/缓存/速度三条指标 + 会话切换 |
| `fspicker.js` | 137 | `@` 文件夹选择器 |
| `groups.js` | 559 | 群聊（消息、成员、头像、设置） |
| `app.js` | 229 | 启动初始化与事件集中注册 |

---

## 三、验证方法（照抄即可复现）

```bat
cd /d D:\project_ai\mini_code

REM 1. 构建闸门
go vet ./...
go test ./... -count=1
build.bat windows
build.bat

REM 2. 静态检查（无内联 style、事件集中、CSS 变量、esc 转义…）
python build\windows\_work_20261027_split\check_ui.py

REM 3. 语法检查
for %f in (web\ui\js\*.js) do @node --check "%f"

REM 4. 端到端（隔离 USERPROFILE，真实调用模型，53 项断言）
python build\windows\_work_20261027_split\e2e.py

REM 5. 前缀缓存两轮实测（要求 > 90%）
python build\windows\_work_20261027_split\prefix_cache.py

REM 6. 与 1178001 的样式等价性（需要 worktree 基准）
python build\windows\_work_20261027_split\equiv2.py

REM 7. 插件层（静态边界 + 端到端 + 装/不装缓存 A/B）
python build\windows\_work_20261027_split\check_plugins.py
python build\windows\_work_20261027_split\e2e_plugins.py
python build\windows\_work_20261027_split\cache_compare.py
```

### 3.1 检查点应当给出的输出

| 命令 | 期望 |
|---|---|
| `go vet ./...` | 无输出 |
| `go test ./...` | 11 个包，其中 9 个 `ok`，2 个 `no test files` |
| `build.bat windows` | `build\windows\mini_code.exe` |
| `build.bat` | 三平台全成功 |
| `check_ui.py` | `RESULT: all checks PASSED`（8 项） |
| `check_plugins.py` | `RESULT: all plugin checks PASSED`（8 项） |
| `node --check` | 23 次 `OK` |
| `e2e.py` | `=== summary: 53/53 PASS ===` |
| `e2e_plugins.py` | `=== summary: 24/24 PASS ===` |
| `prefix_cache.py` | `PREFIX CACHE: PASS (95.4%, 93.8%)` |
| `cache_compare.py` | `=== summary: 9/9 PASS ===` |
| `equiv2.py` | `elements differing: 0 / 30` |

---

## 四、行为契约（拆分后仍然成立）

这些是实现细节里最容易在后续重构中被误伤的部分，逐条登记：

| # | 契约 | 位置 |
|---|---|---|
| 1 | 模型按钮**单击** 230ms 后开菜单，**双击**（230ms 内两次 click）开思考浮层 | `app.js` / `controls.js` |
| 2 | `sending` 保护期 8s 兜底超时 | `send.js` |
| 3 | SSE 断线重连 75s | `events.js` |
| 4 | 历史懒加载触发阈值 120px，每批 `HIST_PAGE=10` 条 | `messages.js` |
| 5 | 工具结果展示截断 420 字符，参数截断 4000 字符 | `steps.js` |
| 6 | 图片上限 4 张 / 单张 10MB（后端 `maxImagesPerMessage` / `maxImageBytes`） | `composer-image.js` / `web/web.go` |
| 7 | 三态主题 `light → dark → system` 循环，key `mc-theme` | `theme.js` |
| 8 | 发送模式 key `mc-send-mode`，`enter` / `shift` | `sendmode.js` |
| 9 | 缓存命中按 `cached/prompt` 计算，`reported=false` 显示 `n/a` 而非 0% | `usage.js` |
| 10 | 所有 HTML 拼接必须过 `esc()`（现在同时转义 `& < > " '`） | `dom.js` |

---

## 五、静态资源路由（新增能力）

`web/ui.go` 在 `handleIndex` 之前拦下带白名单扩展名的请求：

- 白名单：`.css .js .svg .png .webp .ico .woff2`
- 启动时一次性 `fs.WalkDir` 读入内存，并对 `index.html` 的引用做存在性校验（缺失即 `slog.Warn`）
- 响应头：`Content-Type`（按扩展名显式指定，避免 `mime` 包在 Windows 注册表上返回 `text/plain`）、
  `ETag`（内容 sha256 前 8 字节）、`Cache-Control: no-cache`、`X-Content-Type-Options: nosniff`
- `If-None-Match` 命中返回 304
- 非白名单扩展名**不下沉拦截**，仍交给原 index 路由，因此 `/favicon.ico`、
  `/avatars/{name}`、`/bootanim/` 的既有 handler 优先级不变

---

## 六、前缀缓存指标（插件化的红线）

### 6.1 插件化之前的基线（无插件）

连续两轮真实对话（第二轮带工具调用），本地 Qoder 中转 / DeepSeek-Flash：

| 轮次 | prompt | cached | 命中率 |
|---|---:|---:|---:|
| turn 1 | 3,877 | 3,712 | 95.7% |
| turn 1（工具后） | 3,893 | 3,712 | 95.4% |
| turn 2 | 3,957 | 3,712 | 93.8% |

### 6.2 插件化之后的 A/B（同一段对话，装 / 不装插件）

| | prompt | cached | 命中率 |
|---|---:|---:|---:|
| 无插件 · turn 1 | 3,877 | 3,712 | 95.7% |
| 无插件 · turn 2 | 3,888 | 3,712 | 95.5% |
| **有插件 · turn 1** | 4,353 | 4,219 | **96.9%** |
| **有插件 · turn 2** | 4,364 | 4,224 | **96.8%** |

- 装插件只多 **476** 个 prompt token（2 个插件工具的 schema），命中率差 **1.2 个百分点**；
- 逐调用对齐，最大偏差 1.3 个百分点；
- **结论：连续两轮 > 90% 通过，且与不装插件时基本一致。**

> 口径说明：一轮带工具调用的任务里，第 2、3… 次 LLM 调用的 prompt 会追加本次新产生的
> 工具结果，这部分天然无法命中缓存，命中率必然下滑——**不装插件同样下滑**（实测 80.0%）。
> 因此判定口径取「每个用户轮次的第一次调用」+「装/不装逐调用对齐」，而不用「任务末尾那次调用」的绝对值。

### 6.3 硬约束（实现必须满足）

1. 系统提示必须保持静态：不得插入时间戳、随机数、"当前插件列表"之类会变的字符串；
2. 工具 schema 列表继续按名字排序（`tools/registry.go`），核心段先输出、插件段后追加；
3. 插件工具只能**追加**在稳定前缀之后，禁用/启用插件不得改变核心工具的顺序与内容；
4. 插件工具不得与核心工具重名（重名直接忽略）；
5. 插件加载按 id 排序，顺序确定。

---

## 七、回滚方式

改动全部未提交，所以回滚 = 丢弃工作区：

```bat
cd /d D:\project_ai\mini_code
git checkout -- session/gate.go session/prompt.go tools/registry.go ^
                web/ui.go web/web.go web/ui/index.html ^
                web/web_ui_models_test.go web/web_ui_thinking_test.go
del /s /q web\ui\css web\ui\js
del web\ui_assets_test.go web\ui_source_test.go web\plugins.go web\plugins_test.go
rd /s /q plugin examples docs
```

或（更彻底，保留 docs）：

```bat
git stash push --include-untracked
```

拆出来的 CSS/JS 是可以**逐字节还原**回单文件的（`split.py` / `emit.py` 的往返校验已通过），
所以如果只想回到单文件形态又保留其他改动，拼接回去即可。

---

## 八、关于 git tag

第 5 步原计划打本地 tag 作为检查点。**按要求本次未执行任何 `git commit` / `git push`**，
而 `git tag` 默认指向某个 commit——在当前工作区有未提交改动的情况下打 tag，
标记的会是 `1178001`（重构**前**的状态），与检查点语义相反，会误导后续回溯。

因此本检查点用**文件 + 内容哈希**固定，而不是 git 对象：

- 本文档第二节列出的改动清单
- 各文件的 sha256 前 16 位（见下）

用户检查满意后自行提交时，建议：

```bat
git add -A
git commit -m "refactor(web-ui): split single-file UI into html/css/js modules"
git tag -a checkpoint-aa -m "checkpoint AA: front-end split verified equivalent"
```

### 8.1 文件指纹（sha256 前 16 位）

| 文件 | 字节 | 行 | sha256(前 16) |
|---|---:|---:|---|
| `web/ui/index.html` | 31,470 | 584 | `95e28d4b27a81deb` |
| `web/ui/css/base.css` | 58,928 | 1,123 | `efdb832d30e763ba` |
| `web/ui/css/layers.css` | 35,779 | 745 | `a53705538d43ffb3` |
| `web/ui/js/core.js` | 201 | 7 | `b74185b949273c09` |
| `web/ui/js/dom.js` | 853 | 24 | `cb11231a95415206` |
| `web/ui/js/theme.js` | 5,334 | 98 | `cd49d46039449d0c` |
| `web/ui/js/markdown.js` | 18,821 | 343 | `8b52e03aef01c2e8` |
| `web/ui/js/groups.js` | 25,186 | 559 | `3dc0a80284b780e6` |
| `web/ui/js/settings.js` | 19,818 | 415 | `59e62c2a2962c126` |
| `web/ui/js/plugins.js` | 2,455 | 62 | `918fb5ef16508ef2` |
| `web/ui/js/app.js` | 10,159 | 229 | `443ff59292ae875a` |
| `web/ui.go` | 4,921 | 183 | `ec281ea6d7e846aa` |
| `web/web.go` | 36,024 | 1,023 | `88195540b4bd5b29` |
| `web/ui_assets_test.go` | 4,130 | 133 | `94c797257696a803` |
| `web/ui_source_test.go` | 1,963 | 81 | `af3a30878f784ba2` |
| `docs/frontend-test-plan.md` | 26,530 | 439 | `3ec4a9e0cc74589b` |

（完整 31 个文件的指纹由 `build/windows/_work_20261027_split/manifest.py` 生成。）

---

## 九、已知遗留（不属于本检查点范围）

| 项 | 说明 |
|---|---|
| `web/ui/js` 中仍有 66 处 `innerHTML=` | 原样迁移，未做虚拟 DOM；后续可渐进收敛到统一渲染函数 |
| 40 个按钮无 `aria-label`、无 `role`/`tabindex` | 可访问性问题（审计 P2），拆分未改动 |
| `config/config.go:222 SaveConfig` 会丢弃 `CompactionEnabled` | 既有 bug，与前端无关，未修 |
| `gofmt -l` 报 `web/fsindex.go`、`web/web_mode_test.go` 等 | 拆分前就未格式化，非本次引入 |
| `build/windows/_work_*/` 为临时目录 | 含脚本与对照 exe，任务结束后可整体删除 |

---

## 十、这个检查点之后做了什么（第 7 步：插件化）

本次改动是在本检查点的等价性基线**之上**叠加的，没有回退任何已通过项：

| 新增 | 作用 |
|---|---|
| `plugin/` 包 | 目录式插件：清单解析、校验、工具适配器 |
| `tools.ToolRegistry.extra` | 插件工具**追加**在核心工具之后（核心段字节不变） |
| `session/plugins_tools.go` | 插件工具注册 + 插件记忆注入系统提示 |
| `session/prompt.go` / `session/gate.go` | 角色权限表不再误杀插件工具；谨慎模式下插件工具必审批 |
| `web/plugins.go` | `/api/plugins`、`/plugins/<id>/<file>`、页面资产注入 |
| `web/ui/js/plugins.js` + 「插件」设置页 | 列出已装插件 / 重新扫描 |
| `examples/plugins/{web-search,skin-night,wallpaper-demo}` | 三个可直接拷进去用的示例 |

重跑结果（全部通过，无回归）：

| 项 | 结果 |
|---|---|
| `go vet ./...` | 零输出 |
| `go test ./...` | 11 个包，9 个有测试全部 ok（新增 `plugin` 包） |
| `build.bat` 三平台 | 全成功 |
| `check_ui.py` | 8 项全 PASS |
| `check_plugins.py` | 8 项全 PASS |
| `e2e.py`（UI 53 项） | 53/53 PASS |
| `e2e_plugins.py` | 24/24 PASS |
| `equiv2.py` | 差异 0/30 |
| `prefix_cache.py`（无插件） | 95.4% / 93.8% |
| `cache_compare.py`（装/不装 A/B） | 9/9 PASS，有插件 96.9% / 96.8% |

细节见 `docs/plugins.md`。
