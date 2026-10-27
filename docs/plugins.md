# 插件系统（三类插件 · 插件中心）

> 一句话：把插件目录丢进 `~/.mini_code/plugins/`，写一个 `plugin.json`，
> **设置 → 插件中心**立刻就能看到它。不改页面、不重编译、不重启。

设置页一共**三项**：`外观与启动` / `模型` / `插件中心`。
插件中心里**两个**页签：`已安装` / `插件市场` —— 左边一列紧凑列表，右边是选中项的详情
（照 VS Code 的扩展页做的）。**没有**「开机动画」页签：开机动画就是外观插件多写一个
`boot` 字段（见第三节），装、选、开关全在同一个地方。
（为什么只剩三项、布局为什么改成左右分栏，见第八节。）

插件按用途分成**三类**，同一套目录契约、同一个 manifest 文件，靠声明的字段自动分类
（也可以用 `"kind"` 显式写死）：

| 类 | `kind` | 它是什么 | 典型字段 | 示例 |
|---|---|---|---|---|
| 一 | `process` | mini_code **启动并保活**的一个 exe / 命令（python 服务、本地 mcp 桥……），可以被披露给模型、模型能查到它 | `process` | `process-demo` |
| 二 | `tool` | 像网络搜索 / skills / mcp 那样，**往工具列表追加**一个工具 | `tools`、`memory` | `web-search` |
| 三 | `appearance` | **DIY 前端**：皮肤、壁纸、界面插槽、动效，以及**开机动画** | `styles`、`wallpaper`、`ui`、`boot` | `skin-aurora` / `mc-access` |

分类是**推断**的：显式 `kind` 优先，否则按 `process > tools > 外观字段` 的顺序判定
（`Plugin.Category()`）。
分类只影响 UI 分组和「造插件」给哪份提示词，**不影响加载**：一个插件可以同时有三类字段。

---

## 一、边界：什么是可插件的，什么不是

| 部分 | 可插件化 | 原因 |
|---|---|---|
| 工具（`tools`） | ✅ 只能**追加** | 插件工具挂在核心工具之后，核心列表的字节序列不变 |
| 程序（`process`） | ✅ 只能**受管** | 只由 mini_code 拉起/停止，插件拿不到宿主的句柄 |
| 皮肤（`styles`） | ✅ | 在内置样式之后注入，纯 CSS |
| 壁纸（`wallpaper`） | ✅ | 一条 `background-image` |
| 插槽（`ui.slots`） | ✅ | 只认 6 个白名单位置，**sandbox iframe** 渲染 |
| 动效（`ui.motion`） | ✅ | 只认 5 个事件 × 一个 class，纯 CSS |
| 记忆（`memory`） | ✅ | 追加到系统提示的静态文本 |
| 开机动画（`boot`） | ✅ | 外观插件的一个字段：静态 HTML 包，走 `/bootanim/` + 沙箱 iframe |
| **agent 循环** | ❌ | 任务执行、重试、放弃逻辑是核心语义 |
| **权限 / 闸门** | ❌ | 插件只会让闸门**更严**（谨慎模式必须审批），不能放宽 |
| **上下文压缩** | ❌ | 决定会话能否活过窗口，不能交给第三方 |
| **基础工具** | ❌ | `read_file`/`write_file`/`exec`/`search_files` 等的语义是契约 |

`plugin` 包**不 import** `agent` / `session` / `approval`，
`check_plugins.py` 第 1 项会静态断言这一点——插件层拿不到核心的任何句柄。

> **关于「桌面宠物」**：这次**没有做**。网页画不到窗口外面，只能在窗口内装成宠物，
> 那不是宠物。等有独立进程（第一类）的那套基建之后，宠物应该作为**独立进程**再做，
> 而不是塞进 WebView。相关代码已从 `plugin/ui.go`、`pluginruntime.js`、`plugins.css` 删除。

---

## 二、目录结构

```
~/.mini_code/plugins/
└── web-search/                ← 目录名建议和 id 一致（资产路由与安装器都依赖这一点）
    ├── plugin.json            ← 唯一必需的文件
    ├── memory.md              ← 可选：追加到系统提示
    ├── skin.css               ← 可选：皮肤
    ├── bar.html               ← 可选：插槽片段（必须在 ui.slots 里声明）
    ├── bg.webp                ← 可选：壁纸
    └── server.py              ← 可选：第一类插件要拉起的程序
```

开关状态**不在**这里，见 `~/.mini_code/plugins.state.json`（第六节）。

参考实现见仓库里的 `examples/plugins/`，**一类一个**：

- `process-demo/` —— 第一类：`python server.py`，带 `/healthz` 健康检查、`restart: on-failure`、
  `expose.tool`，模型可以用 `plugin_proc__process_demo` 查状态 / PID / 日志尾巴
- `web-search/` —— 第二类：两个工具 + 一段 `memory.md`（包装 `D:\project_ai\web_search`）
- `skin-aurora/` —— 第三类：CSS 皮肤 + 输入框下方的插槽 + 强调色 + 两条动效，**零 JavaScript**
- `skin-night/`、`wallpaper-demo/` —— 更小的外观示例（只皮肤 / 只壁纸）

---

## 三、plugin.json

三类字段可以任意组合，下面是**全部**字段：

```json
{
  "id": "process-demo",
  "name": "本地小服务",
  "version": "1.0.0",
  "author": "",
  "description": "…",
  "enabled": false,
  "kind": "process",

  "process": {
    "command": "python",
    "args": ["server.py"],
    "env": ["PORT=8731"],
    "autostart": true,
    "restart": "on-failure",
    "ready_timeout_seconds": 20,
    "description": "演示用 HTTP 服务",
    "health_check": { "type": "http", "url": "http://127.0.0.1:8731/healthz" },
    "expose": { "tool": true, "endpoint": "http://127.0.0.1:8731" }
  },

  "tools": [ { "name": "web_search", "command": "python", "args": ["…"], "timeout_seconds": 150 } ],

  "styles": ["skin.css"],
  "wallpaper": "bg.webp",
  "ui": {
    "accent": "#6be3b0",
    "slots":  [ { "slot": "composer-bar", "html": "bar.html", "height": 40 } ],
    "motion": [ { "event": "send", "class": "flash" } ]
  },

  "memory": "memory.md"
}
```

### 通用字段规则

| 字段 | 规则 |
|---|---|
| `id` | 必填，缺省取目录名。`^[a-z0-9][a-z0-9-]{0,63}$`（要能当 URL 路径段） |
| `kind` | 可选，`process` / `tool` / `appearance`。不写就推断 |
| `enabled` | 缺省 = `true`。`false` 时整个插件不加载 |
| `styles` / `wallpaper` / `memory` | 必须是插件目录内的**相对路径**，绝对路径和 `..` 会被拒绝 |

### 第一类：`process`（程序插件）

| 字段 | 规则 |
|---|---|
| `command` | 必填。直接 exec，**不经过 shell** |
| `args` / `env` | `env` 是 `["K=V"]` 数组；子进程还会自动拿到 `MINICODE_PLUGIN_ID` / `MINICODE_PLUGIN_DIR`，以及 UTF-8 三件套 |
| `autostart` | `true` 时 mini_code 启动后自动拉起（`StartAutostart()`，不阻塞启动流程） |
| `restart` | `never` / `on-failure`（缺省）/ `always`。退避 1s → 30s |
| `ready_timeout_seconds` | 等待就绪的时间上限，缺省 20，会被夹到合理区间 |
| `health_check` | `{"type":"http","url":…}` / `{"type":"tcp","url":"host:port"}` / `{"type":"process"}`。`http` 没写 `url` 会在加载期就报错 |
| `expose.tool` | `true` 才给模型一个控制工具 `plugin_proc__<id>`（`-` 换成 `_`）；不写则模型看不见，但插件照常在插件中心跑 |
| `expose.endpoint` | 控制工具回报给模型的地址（可选） |

进程由一个**全局单例**监督器管（`plugin.DefaultProcs()`）：插件程序是「安装」的属性，
不是「会话」的属性，两个会话不能各拉一份 python 服务。插件中心（HTTP 层）和
模型的控制工具（会话层）共用这一个实例。日志保留最后 200 行，供插件中心查看。

### 第二类：`tools`（工具插件）

| 字段 | 规则 |
|---|---|
| `tools[].name` | `^[a-z][a-z0-9_]{1,63}$`，**不能与核心工具重名**（重名会被忽略并记警告） |
| `tools[].command` | 必填，直接 exec，不经过 shell |
| `tools[].timeout_seconds` | 缺省 60，上限 600。超时返回工具结果，不是崩溃 |

模型的参数对象以 **JSON 写到子进程的 stdin**，工作目录是插件目录。
所以命令本身在 manifest 里是**静态**的（不进命令行 → 没有引号/注入问题）：

```python
import json, sys
params = json.load(sys.stdin)
print(main(params["query"]))
```

### 第三类：`appearance`（外观插件）

| 字段 | 规则 |
|---|---|
| `styles` | 一组 CSS 文件，在内置 `base.css` / `layers.css` **之后**注入，能覆盖内置主题 |
| `wallpaper` | 一张图，注入成 `#messages` 的 `background-image` |
| `ui.accent` | 一个 `#rrggbb` 颜色。**插件只给一个颜色**，宿主据此推导出主题实际读的五个变量（见下），浅色/深色各一套 |
| `ui.slots[]` | 只能放进白名单 6 个位置（见下）；`html` 必须是本目录内、且**在这里声明过**的文件；`height` 是像素上限 |
| `ui.motion[]` | `event` 只能取 5 个白名单事件；`class` 加在 `<html>` 上，动效全在 CSS 里 |
| `boot` | **开机动画**。声明了它，这个外观插件同时就是一段启动画面（见下）。这是唯一允许带 `.js` 的插件类型 |

#### `boot`：开机动画就是外观插件的一个字段

开机动画曾经是一个**独立子系统**：自己的目录（`~/.mini_code/boot-anim`）、自己的清单
（`theme.json`）、自己的安装器、设置页里自己的页签。那是一层多余的概念——一段开机动画
就是「一堆静态文件 + 一个 HTML 入口」，这恰好是外观插件本来就在做的事。并进来之后
**只有一个东西要装、只有一个开关、只有一个地方找**：

```json
{
  "id": "mc-access",
  "kind": "appearance",
  "boot": { "entry": "index.html", "durationMs": 17800, "maxMs": 30000,
            "interactive": false, "minViewport": 480, "preview": "preview.svg" }
}
```

（插件仓库 `mini_code-plugins` 就是这么写的：`boot-anim/<id>/plugin.json` 由
`tools/make_plugins.py` 从同目录的 `theme.json` 派生，一次跑完顺带重建 `index.json`。）

| 字段 | 规则 |
|---|---|
| `entry` | 插件目录内的 HTML 文档，缺省 `index.html`；**必须存在**，否则插件加载失败并给出文件名 |
| `preview` | 可选静态图（svg/png/webp/gif），用于卡片预览；声明了就必须存在 |
| `durationMs` / `maxMs` | 动画自己的时长 / 播放器强制收场的硬上限（≤30s）。播放器无论如何不会让动画把界面锁死 |
| `interactive` | 片尾是否还能响应指针（还要主题自己声明支持） |
| `minViewport` | 窗口小于这个宽度就不播（小鸡肋动画在窄窗口里只显得糟） |

**同时保留 `theme.json` 兼容**：插件目录里如果有 `theme.json`，其中 `boot` 块没有写的
数字（时长、`interactive`、`minViewport`）和 `options`（品牌文案、语音语言）从它那里读。
所以插件仓库里已有的主题加上一个 `plugin.json` 就能用，不用重抄一遍。
旧目录里装好的主题也照旧能播，只是会被标成 `source: "legacy"`。

**安全边界没有放松**：动画文件走 `/bootanim/<id>/…`，带主题 CSP
（`connect-src 'none'`、`frame-src 'none'`、`form-action 'none'`），跑在
`sandbox="allow-scripts"`（**没有** `allow-same-origin`）的 iframe 里。所以它能动画、
能出声，但读不到本应用的存储、也调不到本应用的接口。这也正是「唯一允许 `.js` 的插件类型」
说得通的地方：它的代码跑在那个沙箱里，不在应用自己的源里。普通资产路由 `/plugins/…`
对 `.js` 仍然是拒绝的。

**选哪个播**：配置里可以钉一个 id（`boot_anim.theme`），没钉就按 id 排序播第一个启用的。
开机画面是全局的东西，没有「同时播两个」的合理语义。
**开关就是插件开关**：在插件中心把那个插件关掉，它既不出现也不播——一个概念，一个开关。


**插槽白名单**（`plugin.SlotNames()`）：
`app-top` / `sidebar-top` / `sidebar-bottom` / `composer-bar` / `header-right` / `overlay`。

**动效事件白名单**（`plugin/ui.go`）：`send`（用户发出）、`reply`（回复结束）、
`error`（出错）、`tool`（工具调用）、`session`（会话切换）。
前端 `plugerevents.js` 的 `mcEmit()` 在这 5 个点触发，`pluginruntime.js` 据此挂/摘 class。

皮肤只写变量就够了，因为 `base.css` 里所有颜色都走 `var(--*)`：

```css
:root { --accent: #2fb0b5; --accent-t: #1f8f92; }
html[data-theme="dark"] { --accent: #5fc4e8; --accent-t: #8fd8f2; }
```

> 可用变量名以 `web/ui/css/base.css` 为准：`--border --border2 --text --text2 --text3
> --hover --red --surface --surface2 --accent`。写不存在的变量（比如 `--muted`）不会报错，
> 只是**什么都不会发生**——这是实测踩过的坑。

#### `ui.accent`：一个颜色，推导出五个变量

`base.css` 的强调色是**五件套**，宿主的界面到处都在用：

| 变量 | 用途 | 什么时候会坏 |
|---|---|---|
| `--accent` | 边框、焦点环、图标 | 和背景太近就看不见 |
| `--accent-btn` | 实心按钮底色，上面是**白字** | 颜色浅了 → 白字消失在白底上 |
| `--accent-t` | 用强调色写的**文字**（在 `--surface` 上） | 颜色浅了 → 正文读不了 |
| `--accent-l` | 极浅的底色（用户气泡、选中项） | —— |
| `--accent-b` | 浅色边框 | —— |

所以插件**只给一个** `ui.accent`（它的意图色），剩下的由宿主按 `base.css` 自己的规律推导：
浅色主题往下压暗（`-t` 压 45%、`-btn` 压 60%），深色主题往上提亮（`-t` 提 45%、`-btn` 压 35%），
浅底色用 `rgba(…,.13)`。这样 `#ffd400` 这种「当边框很好、当文字读不了、当白字按钮等于隐形」的颜色
也不会把界面搞瞎。

浅色/深色**两套都写**，并且选择器带上 `html[data-theme="dark"]`——只写一条 `:root`
会被 `base.css` 里更靠后的深色主题规则盖掉，表现为「深色模式下改强调色毫无反应」。

> ⚠️ 这里曾经有一个真 bug：早期注入的是 `--mc-plugin-accent`，一个**没有任何样式表读**的变量。
> 插件看起来被接受了，实际什么都没变（浏览器测试当时之所以「通过」，是因为示例皮肤自己也设了
> `--accent`，把问题遮住了）。现在测试直接断言五个真实变量都被赋值、且 `--accent-t` 在页面背景上
> 的对比度 ≥3:1，`--mc-plugin-accent` 不许再出现。

---

## 四、前端如何注入

`web/plugins.go` 在 `handleIndex` 里把 `<!--mc-plugin-assets-->` 替换成插件资产，
**注入点在内置样式之后**，所以皮肤能覆盖内置主题。没有装插件时替换成空串，
页面与不装插件时逐字节一致。

壁纸的四个属性都是**显式**写的，不是靠默认值：

| 属性 | 为什么必须写明 |
|---|---|
| `background-size: cover` | 缩放填满消息栏，不重复、不变形 |
| `background-repeat: no-repeat` | CSS 默认是 `repeat`；平铺的照片从来不是壁纸的本意 |
| `background-position: center` | 居中裁切 |
| `background-attachment: scroll` | 固定在消息框内，不随对话滚走 |

多个插件都声明壁纸时**第一个（按 id 排序）胜出**，避免属性打架、顺序不定。

### 插槽：结构隔离，而不是「洗一遍 HTML」

`ui.slots[].html` 指向的片段**不会**直接插进页面。`injectPluginAssets` 只生成一个容器 +
一个 `<iframe src="/plugins/<id>/<file>">`，并且 `sandbox` 属性**由宿主写在 iframe 上**，
插件的 HTML 完全参与不了这条决定：

```html
<div class="mc-slot" data-slot="composer-bar" hidden>
  <iframe class="mc-slot-frame" data-plugin="skin-aurora"
          src="/plugins/skin-aurora/bar.html" height="40"
          sandbox referrerpolicy="no-referrer" scrolling="no" loading="lazy"></iframe>
</div>
```

裸 `sandbox`（既没有 `allow-scripts`，也没有 `allow-same-origin`）意味着片段跑在
**独一无二的源**里：它渲染得出来，但执行不了脚本、碰不到应用的 DOM/Cookie，
也不能带着凭据去调 `/api/*`。所以即使片段里写了 `<script>` 或 `<img onerror=…>`，
也只是**死代码**——隔离靠的是浏览器，不是「我们有没有把危险标签洗干净」。

代价是片段拿不到宿主的样式变量（跨源），所以皮肤要动的颜色请写在自己的
`styles` CSS 里（那份 CSS 注入在**宿主**文档里，能覆盖内置主题）；片段只负责结构。

高度由 `height` 夹住（缺省 56），片段撑不破布局。
前端 `pluginruntime.js` 负责把 `.mc-slot` 搬到页面对应的锚点上（`[data-mc-slot="…"]`，
没有就用内置映射），搬不动就保持 `hidden`，不报错。

### 资源路由的安全约束

| 约束 | 说明 |
|---|---|
| 扩展名白名单 | 静态资产只有 `.css .svg .png .jpg .jpeg .webp .gif .woff2`。**没有 `.js`** —— 插件不能往应用同源里塞脚本 |
| `.html/.htm` | **只**在 manifest 的 `ui.slots` 里显式声明过才放行（`Plugin.DeclaresSlotHTML`），其它 html 一律 403 |
| 路径包含 | 相对路径 + 二次校验（`filepath.EvalSymlinks` 之后再比一次），`..` 与符号链接都出不去 |
| 响应头 | `nosniff` + `Cache-Control: no-store` + `Content-Security-Policy: default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self'` |

---

## 五、插件中心：HTTP 接口

全部挂在 `web/plugins_centre.go` / `web/plugins_catalogue.go` / `web/plugins_prompt.go`。

| 方法 / 路径 | 作用 |
|---|---|
| `GET /api/plugins` | 已装列表（含**已禁用**和**加载失败**的条目，失败项带 `error` 字段）。按 id 排序 |
| `POST /api/plugins/{id}/toggle` | `{"enabled":true|false}`，写 `plugins.state.json` |
| `DELETE /api/plugins/{id}` | 卸载（删目录；只允许删已装插件目录内的目录） |
| `GET /api/plugins/processes` | 所有进程插件的状态：`state`/`pid`/`uptime`/`restarts`/`last_error` |
| `POST /api/plugins/{id}/process` | `{"action":"start"|"stop"|"restart"}` |
| `GET /api/plugins/{id}/logs` | 该进程最后 200 行输出 |
| `GET /api/plugins/catalog` | 插件市场（`zhoujiannan-nb/mini_code-plugins` 的 `index.json`），按类分组，带 `installed` / `up_to_date` |
| `POST /api/plugins/install` | `{"id":…,"kind":…}` 原子安装 |
| `GET /api/plugins/prompt?kind=process\|tool\|appearance` | 「造插件」用的标准提示词 |
| `GET /api/bootanim` | 已装的**开机动画**（即带 `boot` 的已启用插件 + 旧目录里的主题），带 `source`/`error`；另有 `play` 段（`enabled/theme/mode/mute`）供详情面板渲染 |
| `PUT /api/bootanim` | 只改播放设置。四个可选字段 `enabled/theme/mode/mute`，**省略即保持原值**；`theme` 必须是本机已装且可播的 id，否则 400。**不校验模型列表**（这个接口没碰它） |
| `GET /bootanim/{id}/{file…}` | 动画自己的文件，带主题 CSP |

> 开机动画**没有**自己的安装/卸载接口：它就是插件，走 `/api/plugins/*`。
> 早先的 `/api/bootanim/remote`、`/api/bootanim/install`、`DELETE /api/bootanim/{id}`
> 已经删掉——留着就会有两套安装器、两套卸载语义，正是这次要消掉的东西。
>
> `PUT /api/bootanim` 是**后加**的，理由见第八节：播放开关是"勾一下"，而
> `PUT /api/config` 要求整份配置（含模型与 API key）并通过 `AppConfig.Validate()`，
> 用它等于为了一个复选框重校验所有模型。


列表项的形状（节选）：

```json
{ "id":"process-demo", "category":"process", "kinds":["process"],
  "enabled":true, "tools":["plugin_proc__process_demo"], "dir":"…",
  "process":{"state":"running","pid":1234,"uptime_seconds":42},
  "has_memory":false, "has_styles":false, "source":"catalogue" }
```

`tools` 里包含**宿主合成**的控制工具（`plugin_proc__<id>`）：它不在 manifest 里，
是由 `process.expose.tool` 推导出来的，但必须列出来——否则插件中心会对着一个
「确实给了模型工具」的插件说「它没给模型任何东西」。

### 安装是「先验后用」，不是「下载完再检查」

`POST /api/plugins/install` 的顺序是：

1. 读 `index.json`，取出该条目的**文件清单 + 每个文件的 sha256 + 体积**；
2. 逐个下载到 `.install-<随机>` **暂存目录**（这个前缀不会被 `Load()` 当成插件）；
3. 在暂存目录里逐文件校验 sha256 与体积，超限（体积 / 文件数）直接中止；
4. `os.Rename` 把暂存目录换成正式目录；
5. 用**真实目录名**再 `LoadDir()` 校验一次（能抓到「manifest 的 id 和文件夹名不一致」）；
6. 任何一步失败 → 回滚，正式目录保持原样（已有插件不受影响）。

装好的目录里会多一个 `.catalogue` 标记文件，插件中心据此把来源显示成「市场」，
而不是「本地」。

### 开关状态：为什么不能改 plugin.json

启用/禁用写在 `~/.mini_code/plugins.state.json`：

```json
{ "disabled": ["skin-night"] }
```

`plugin.json` 是**被市场校验 sha256** 的文件之一。如果点击开关就去改它，
下一次升级时每个已装插件都会显示「被篡改」。所以状态外置：manifest 里的
`"enabled": false`（市场条目默认关闭）和状态文件里的 `disabled` **两者都尊重**，
点开关永远不会写插件自己的文件。

### 离线 / 内网镜像：`MINICODE_GITHUB_RAW`

市场默认从 `raw.githubusercontent.com` 拉。离线安装或内网镜像时：

```bat
set MINICODE_GITHUB_RAW=https://mirror.example.com/gh
```

值会过一条**严格**正则（`^https?://host[/path]$`，无 query 无 fragment），
不合规直接忽略并记 WARN——这个变量是用来拼下载 URL 的，松一点就等于把安装器
指向任意端点。它只从本进程的环境读，能设它的人本来就能以这个用户跑代码。
（测试也是用它把市场指向本地 HTTP 服务器的。）

---

## 六、前缀缓存为什么不会退化

这是本次改动的**硬约束**：连续两轮对话 `cached_tokens / prompt_tokens > 90%`。

请求前缀的顺序是：

```
[系统提示][核心工具 schema × N][插件工具 schema × M][历史消息]
                ↑ 这一段必须与装不装插件无关
```

实现上：

1. `tools.ToolRegistry` 拆成 `tools`（核心，按名字排序）和 `extra`（插件），
   `GetDefinitions` 先输出排序后的核心段，**再**追加排序后的插件段。
   装插件时核心段的字节序列完全不变。
2. `session.NewPromptBuilder` 里 `registerDefaultTools()` 先跑，
   `registerPluginTools()` 后跑（`check_plugins.py` 第 3 项静态断言顺序）。
   `plugin.ProcTools()` 也走同一个 `RegisterAppended`。
3. 插件工具若与核心工具重名，直接跳过（`check_plugins.py` 第 4 项）——
   否则那个已缓存名字的语义会被换掉。
4. 插件记忆是磁盘上的静态文本，不含时间戳/随机数（`check_plugins.py` 第 7 项）。
5. 插件按 id 排序加载，顺序确定；进程状态**不进**提示词（模型查得到，提示词不因此变）。

### 三类插件同时装：实测（`_work_*/prefix_cache3.py`）

装齐 `skin-aurora` + `process-demo` + `web-search`，与「零插件」逐字节对比，**10/10 通过**：

| 断言 | 结果 |
|---|---|
| 核心工具列表（名字、顺序）一致 | 12 个：`copy_file delete_file edit_file exec glob list_dir move_file read_file readimg search_files skills write_file` |
| 核心工具描述 sha256 一致 | ✅ |
| 核心工具参数 schema sha256 一致 | ✅ |
| 插件工具不插进核心段 | core=12，appended=3 |
| 追加段按名字排序（稳定） | `plugin_proc__process_demo web_read web_search` |
| 每一类贡献的正是它该贡献的 | ✅ 外观类贡献 0 个工具 |
| 系统提示只增长「记忆块」的大小 | 3884 → 4493 字节（块 608 + 1 个分隔符） |
| 把记忆块摘掉后，提示词与零插件**逐字节相同** | ✅ |
| 零插件时没有任何「追加」工具 | 12 个，0 个 appended |

最后两条是关键：它证明插件对提示词的**唯一**影响就是结尾那段 `[memory: …]`
（`web-search` 自带 `memory.md`）。摘掉它，剩下的字节和不装插件完全一样——
所以哪怕提示词变长了，**前缀照样命中**。

### 早先的端到端命中率（`cache_compare.py`，同一段两轮对话）

| | prompt | cached | 命中率 |
|---|---:|---:|---:|
| 无插件 · turn 1 | 3,877 | 3,712 | 95.7% |
| 无插件 · turn 2 | 3,888 | 3,712 | 95.5% |
| 有插件 · turn 1 | 4,353 | 4,219 | 96.9% |
| 有插件 · turn 2 | 4,364 | 4,224 | 96.8% |

装插件只多了 **476** 个 prompt token（就是 2 个插件工具的 schema），命中率差别
**1.2 个百分点**（有插件反而略高：固定 schema 段更长、可复用比例更大）。

### 关于「工具循环里后面的调用命中率会掉」

一轮带工具调用的任务里，第 2、3… 次 LLM 调用的 prompt 会追加**本次新产生的工具结果**，
这部分天然无法命中缓存，所以命中率必然下滑。这与插件无关：
**不装插件同样下滑**。判定口径因此取「每个用户轮次的第一次调用」，
以及「同一对话装/不装插件的逐调用对齐」——两者都在 90% 以上。

---

## 七、权限：插件只会让闸门更严

`session/gate.go` 的两条规则：

1. **角色白名单不再误杀插件工具。**
   编译进二进制的 `agent/basePermissions` 只列核心工具，插件工具天然不在里面。
   原逻辑「不在表里 = 拒绝」会把插件工具从 schema 和运行期都过滤掉
   （实测表现：模型回答「本会话里我并没有 `web_search` 这个工具」）。
   现在：不在表里 + 确实是已启用的插件工具 → 放行；
   但如果角色**显式**写了 `"web_search": "deny"`，仍然拒绝。核心工具的过滤逻辑一点没松。
2. **谨慎模式下插件工具一律要审批。**
   插件跑的是宿主不认识的外部程序，所以和「破坏性 shell 命令」同等对待：
   先弹审批，批准才跑。这是插件层唯一碰到闸门的地方，且只会让审批变多、不会变少。

第一类插件的 `plugin_proc__<id>` 控制工具走的是同一套判定：它只能 `start`/`stop`/
`restart`/`logs`/`status` **自己那个**插件，拿不到别的插件的 id（id 在工具构造时就绑死了）。

---

## 八、设置页结构：为什么只有三项

插件中心第一次上线后，设置页有 6 个一级入口：外观、个人信息、模型、Web 服务、插件、开机动画。
用户的原话是「插件里面布局还是有点乱,我一进去看的很懵逼」。逐项测量后（`_work_*/measure_layout.py`）
定位到两个真问题，不是审美问题：

| 症状（实测） | 原因 | 改法 |
|---|---|---|
| 插件页 scrollHeight **1325px**，视口只有 **523px**（2.5 屏） | 3 个分类各只有 1 个插件，而每个分类标题占**整行**（`grid-column: 1/-1`），卡片被挤成一行一张 | 先按分类分栏 → 再换左列表 + 右详情 → 最后是**卡片网格 + 详情**（见下） |
| 一进页面先看到 179px 的「造一个」 | 常驻展开 | 改成 `<details>` 默认收起（39px），要造时点开 |
| 卡片描述最多 4 行（72px） | 中文描述整段铺开 | 卡片上只留**一行**描述（超出省略），完整文字在详情与 `title` 里 |
| 端口/头像/主题散在三个一级入口 | 都属「窗口长什么样、怎么起来」 | 合并成 **外观与启动** 一页 |
| 大卡片一屏只有三四张 | 状态/按钮/徽标/详情全塞在每一张卡里 | 卡片只留"名字 + tag + 一行描述"，其余进详情 |
| 插件市场在左列表里局促（列表只有 260px） | 左右分栏把一半宽度让给了列表 | 换成**卡片网格**，一段动画一张卡；点进去才是详情 |

实测演进：**1325px（2.5 屏）→ 分栏 621px → 左列表 611px（1.2 屏）→ 卡片网格 555px（1.0 屏）**。

现在的一级入口只有三个：

| 入口 | 内容 |
|---|---|
| **外观与启动** | 主题、个人头像、Web 端口（含当前地址行）、开机动画的**一句状态摘要**（+ 跳转按钮） |
| **模型** | 模型列表与默认模型 |
| **插件中心** | **两个**页签：已安装 / 插件市场 |

### 插件中心为什么是「卡片网格 + 点进去看详情」

这块改过两次，两次都是被实测数字推着走的：

1. 一开始是**一屏大卡片**：每张卡自带状态、按钮、徽标、可展开的详情。1025px / 2.5 屏，
   一屏只放得下三四张，而且「我正在看的那一个」和别的一样重。
2. 后来照 VS Code 的扩展页改成**左列表 + 右详情**：611px（1.2 屏），行高 76px → 56px。
   但左列只有 260px，插件市场塞在里面很局促；用户的原话是「插件市场有点局促」。
3. 现在是**卡片网格 + 点进去看详情**（和「模型」页同一套卡片）：

| | 卡片（网格） | 详情（点进去那一屏） |
|---|---|---|
| 回答什么 | **装了什么**：图标 + 名字 + 分类 tag + 一行描述 + 一行状态 | 这个插件**是什么、能干什么、下一步点哪里** |
| 尺寸 | 实测 **269×105px**，一屏 **4 列 × 2 行** | 整块 1094px 宽，独立滚动 |
| 卡片里有什么 | **零按钮**（断言过 `buttons == 0`） | 所有动作：预览 / 设为开机动画 / 播放设置 / 启停 / 卸载 |
| 怎么回来 | —— | 左上角 `‹ 全部插件` |

三条铁律（都有测试盯着）：

- **卡片等高**。状态那行即使没有话也要占位，否则同一排会出现 105px 和 84px 两种高度
  （第一版就踩了，`len(set(heights)) == 1` 现在是一条断言）。
- **卡片里没有按钮**。按钮一多，卡片就回到"每一行都很重"的老路。
- **网格和详情不同时出现**。首屏永远是网格，所以整页永远是一屏
  （实测四个页面全是 `scrollH == clientH`，即 1.0 屏、`overflow=none`）。

### 开机动画为什么不再是一个页签

它一开始是独立子系统，后来并进插件中心当一个页签，现在是**外观插件的一个字段**
（`boot`，见第三节）。每一步的理由都一样：它不是「另一套东西」，它就是
「一堆静态文件 + 一个 HTML 入口」，而这正是外观插件。留一个页签就意味着一套
自己的安装器、自己的开关、自己的卸载路径 —— 两套语义，两处要找。

再往后又收了一步：**播放设置也搬进动画插件的详情里了**，外观页只剩一句摘要。

| 事情 | 在哪做 |
|---|---|
| 装 / 删 / 停用 | 插件中心的卡片与详情（`/api/plugins/*`） |
| 选哪个播 | 详情里的「设为开机动画」（写 `boot_anim.theme`） |
| 播不播 / 何时播 / 静默 | 同一个详情的「开机播放」段（`PUT /api/bootanim`） |
| 现在是什么状态 | 外观页一行摘要（"已启用 · 接入序列 · 每个会话一次 · 已装 2 个"） |

为什么播放设置要单独开一个 `PUT /api/bootanim`，而不是复用 `PUT /api/config`：
后者要求**整份**配置（含模型列表与 API key）并且要过 `AppConfig.Validate()`。
从一个"只有开关"的面板提交它，等于为了勾一个复选框把所有模型重新校验一遍 ——
模型列表只要有一处不完整，勾选就会 400（这是实测到的，见踩坑表）。
所以那个接口只解析 `enabled/theme/mode/mute` 四个指针字段、只校验 boot 块，
省略的字段保持原值。

### 开机动画为什么砍掉了「速度」

原来有一档 `0.35–3` 的倍速。砍掉的理由：

- 一段开机动画是**设计好的时间线**，"调快两倍"通常只是把同一段东西看得更仓促；
- 它让 `boot_anim` 多一个字段、让播放器多一个 query、让每个主题都要考虑 `speed` 的
  排版后果（字号、行长都要跟着变），成本分散在所有地方，收益只在个别场景；
- 真的嫌长，主题自己的 `durationMs` 就是那个"该多长"的答案。

`config.BootAnimConfig.Speed`、`Min/MaxBootAnimSpeed`、`#b-speed`、播放器的
`speed=` query 全部删除。老 `config.json` 里残留的 `"speed": 2` 不会报错
（`encoding/json` 忽略未知字段），但也不会再被写回去。

早先的 `/api/bootanim/remote|install`、`DELETE /api/bootanim/{id}` 因此整个删掉；
`GET /api/bootanim` 保留（多带一个 `play` 段），因为播放器要知道「现在该播哪个、
入口在哪」，而详情面板要知道「现在是开还是关」。

### Web 服务为什么只能改端口、不能关

桌面端的窗口**就是通过 `http://127.0.0.1:<port>` 加载界面的**
（`desktop/desktop.go:268` 构造 `app.url`，`desktop/windows/windows.go:332` 调 `Navigate`，
而且启动时会等到 `/api/healthz` 返回 200 才显示窗口）。
所以「关闭 Web 服务」等于让桌面窗口没有任何内容可加载 —— 那不是可选项。

页面上因此只提供端口一个字段，并直接把「当前地址」写出来，顺带说明桌面窗口也走它。

---

## 九、造插件：点一下就带着提示词跳会话

插件中心的「造插件」不去调什么生成接口，而是：

1. `GET /api/plugins/prompt?kind=…` 取对应类别的**标准提示词**；
2. 关掉设置、新建会话；
3. 把提示词填进输入框（**不自动发送**，用户可以改名、改端口、改路径再发）。

三份提示词分别覆盖：`process`（怎么声明 command/health_check/expose、怎么本地先跑通）、
`tool`（stdin 收 JSON 参数、stdout 出结果、UTF-8、超时）、
`appearance`（只能 CSS、slot 只能 6 个、html 必须自己声明、动效只能 5 个事件、禁 JS）。
提示词里写死了「把产物放进 `~/.mini_code/plugins/<id>/`」和「不要写 `.js`」这两条红线。

想分享给别人，就按 `zhoujiannan-nb/mini_code-plugins` 的 `index.json` 格式提交：
文件清单 + 每个文件的 sha256 + 体积。插件中心的「插件市场」页签就是读它。

---

## 十、踩过的坑（实测记录）

| 现象 | 原因 | 修法 |
|---|---|---|
| 模型说「我没有 web_search 工具」 | 角色权限表过滤掉了插件工具 | `GetFilteredDefinitions` / `gateFor` 对「已启用插件工具」放行（显式 deny 仍生效） |
| 插件工具返回 `UnicodeEncodeError: 'gbk' codec can't encode` | Windows 子进程继承 GBK 代码页 | 子进程注入 `PYTHONUTF8=1` / `PYTHONIOENCODING=utf-8` |
| 工具输出含非 UTF-8 字节时发消息失败 | JSON 序列化不接受非法 UTF-8 | `sanitizeUTF8()` 把非法字节替换为 `�` |
| 插件目录里的 `evil.js` 被同源加载 | —— | 白名单只放静态样式/图片，`.js` 一律 403，`.html` 只放声明过的插槽片段 |
| 设置页一打开就报 `loadPlugins is not defined` | 前端函数改名成 `loadPluginCentre()` 后漏改调用点 | 实实在在的 bug，已修 |
| 皮肤写了却没生效 | 顺手用了 `--muted` / `--line` 这种**不存在**的变量名 | 以 `base.css` 的真实变量名为准；写错不报错，只是没效果 |
| `ui.accent` 写了却没生效 | 注入的是 `--mc-plugin-accent`，**没有任何样式表读它**；示例皮肤自己也设了 `--accent`，把问题遮住了 | 改成推导五个真实变量（浅/深两套），并加上「不许再出现 `--mc-plugin-accent`」的断言 |
| 插件中心对 `process-demo` 显示「没有工具」 | `tools` 只从 manifest 读，而控制工具是宿主从 `process.expose.tool` 合成的 | 列表里补上 `pluginProcToolName(id)`，并加了断言 |
| 插件页「一进去很懵」（2.5 屏） | 每个分类标题占整行 + 卡片一行一张 + 「造一个」常驻展开 | 改成按分类分栏 + 「造一个」折叠 + 描述 2 行封顶；实测 1325px → 621px |
| 换端口后界面空白的风险 | 桌面窗口本身就是用 `http://127.0.0.1:<port>` 加载的 | 不提供「关闭 Web 服务」，只让改端口，并把当前地址写出来 |
| 壁纸「看起来没生效」 | **不是功能 bug，是测试图坏了**：1-bit 调色板 PNG 带白色调色板项，`cover` 放大后大片是白，截图采样正好落在白色上 | 换成 8-bit truecolour 纯色图；判定改成像素统计 + 「移除规则后画面必须变化」的差分 |
| 执行过程里 `exec` 的工具名和参数**黏在一行**、换行错乱 | `stepsAddTool()` 把**整段 JSON 参数**塞进标题行的单个 span，长命令直接把工具名挤下去 | 按工具名抽出关键字段（`exec` → `command`）压成单行 + `text-overflow: ellipsis`；参数全文留在展开区 |
| `exec` 的命令被截成 `…/js/steps.js` | 判断「是不是路径」时看**值里有没有斜杠** —— 命令行里全是斜杠，于是被当成路径只留尾部两段 | 只按**键名**判断（`path`/`file`/`dir`/`cwd`…），命令就按命令截断 |
| 执行过程折叠后仍占 **21px** 白高 | `0fr → 1fr` 的网格轨道动画：轨道压到 0 了，网格项自己的最小内容高度又把轨道顶回来 | 改成 `max-height: 0 → 46vh` 过渡；实测折叠后 wrap 高度精确为 0 |
| CDP 测试里的截图/导航偶发 `TimeoutError` | `pump()` 把 socket 超时降到 0.4s 做轮询，而 `call()` 没有把它改回来 | `call()` 每次先 `settimeout(30)` 再等响应 |
| 市场页「点进去什么都没有，要先手动刷新」 | 页签切换只渲染了缓存 | 进入市场页签就**自动执行一次**读取（后端有 5 分钟缓存，重复进入不打 GitHub） |
| 市场里同一段开机动画**并排两张卡** | 插件仓库同时给了 `kinds.appearance` 和兼容别名 `kinds.boot-anim`，市场把各桶合并成一张列表却没有去重（点哪张装出来都是同一个目录） | 合并时按 id 去重（桶名有序，`appearance` 先命中）；仓库侧也在 README 里写明别名桶不许再挂新 id |
| 播放设置想从插件详情里改，一勾就 400「at least one model is required」 | 复用了 `PUT /api/config`，它要**整份**配置并跑 `AppConfig.Validate()`；而那个面板只提交一个复选框 | 新开 `PUT /api/bootanim`：四个指针字段、省略即保持原值、**只校验 boot 块**。测试里专门有一条「模型列表为空也能打这个补丁」 |
| `PUT /api/bootanim` 传了非法 mode 却返回 200 | `Normalize()` 会把 `"weekly"` 悄悄改成 `"session"` —— 请求没被照办，却报了成功 | 在 `Normalize` **之前**先校验枚举，非法值直接 400 |
| 卡片网格里同一排出现 **105px 和 84px** 两种高度 | 状态那行在"没有状态可说"时整个不渲染，少一行自然就矮 | 那一行**永远存在**（可以为空），`min-height:15px`；断言改成 `len(set(heights)) == 1` |
| 折叠后的步骤仍占白高、卡片描述被判定为"溢出" | 用 `scrollWidth > clientWidth` 判断"是否单行"是**反的** —— `text-overflow: ellipsis` 生效时它天然就大于 | 改成判定「`white-space: nowrap` 且高度 ≈ 一行」 |
| 配置弹窗 880px 时插件市场很局促 | 窗口默认 1280×800，弹窗只给 880，左右分栏再各让一半 | 弹窗改成 `calc(100vw - 96px)` / `calc(100vh - 72px)`（带 clamp 上下限），实测占视口 95%、四边都露遮罩 |

最后一条值得记下来，因为它改变了这条链路的测试方式：
**只看 `getComputedStyle().backgroundImage` 是没用的**——它可能完全正确而画面上什么都没有。
壁纸/皮肤这类「看起来应该对」的东西，断言必须落在**像素**上：

```
1. 装一个纯色（#FF0000）32×32 truecolour PNG 当壁纸
2. 截整页图，取 #messages 左内边距那一条竖线（内容卡片够不到的地方）逐点采样
3. 断言 >95% 的采样点是红色
4. 再删掉 mc-plugin-wallpaper 规则重截，断言画面有 >50% 的采样点发生变化
```

第 4 步是关键：它不依赖壁纸的颜色、也不依赖布局，只问「这条规则到底有没有改变画面」。

---

## 十一、测试怎么跑

| 脚本（在 `build/windows/_work_*/`） | 覆盖 | 实测 |
|---|---|---|
| `e2e_centre.py` | 7501 端口真机 HTTP：三类分类、工具暴露、注入、开关、进程起停、日志、提示词、卸载 | **63/63** |
| `e2e_install.py` | 本地镜像装市场：真实 sha256、`.catalogue` 标记、`up_to_date`、卸载 | **29/29** |
| `e2e_tamper.py` | 篡改文件后 sha256 不符被拒，不留残留，已装插件不受影响 | **9/9** |
| `e2e_browser.py` | 真 Chrome + CDP：零 console error、皮肤生效、`ui.accent` 真的改了五个变量且 `--accent-t` 可读、插槽 iframe 是 sandbox、动效、市场空态、造插件填词、`prefers-reduced-motion`、**没有宠物层** | **36/36** |
| `check_shot.py` | 截图**像素统计**（1474×824，采样 2000–3200 色，非空白） | 3/3 |
| `e2e_settings_layout.py` | 设置页结构：3 个入口、插件中心 **2 个页签**、**卡片网格 + 点进去看详情**（网格多列、卡片等高、卡片里零按钮、每张卡有分类 tag、点卡进详情、有返回、详情里有播放设置且无速度）、外观页无表单只有摘要、市场同一套卡片且进入即自动读取 | **43/43** |
| `e2e_steps.py` | 执行过程渲染：整段 JSON 不进标题行、工具名与参数分盒、长命令单行省略且可悬停看全文、路径只留尾两段、折叠高度精确为 0、展开有过渡、结果与参数在步骤内部 | **21/21** |
| `e2e_grid.py` | 卡片网格的专项验收：一卡一插件、等高、无按钮、点卡进详情/返回、播放设置三选一且无速度、静默开关真的落库、市场卡片、外观页零控件 | **27/27** |
| `_grid_geo.py` | 卡片几何实测（列数、卡片宽高、详情宽、整页屏数） | 4 列 × 269×105px，1.0 屏 |
| `e2e_layout_metrics.py` | **布局度量**：四个页面 × 越界/裁切/横向溢出、卡片数/行数/最小卡高、整页屏数、900px 窄屏 | 四页均 1.0 屏 |
| `_dup_catalog.py` | 拿**真实**插件仓库当镜像，数市场 entries：两个桶（appearance + 别名 boot-anim）必须只出一张卡 | 2 条 / 无重复 |
| `go test ./web/ -run TestCatalogue` | **Go 侧**：别名桶按 id 去重、不同桶里的不同插件不能被误删、离线目录返回可读 502 | ok |
| `go test ./web/ -run TestBootAnimPatch` | **Go 侧**：`PUT /api/bootanim` 只动 boot 块（模型列表不完整也能过）、省略字段保持原值、未安装的 id 400、非法 mode 400、旧 `speed` 被忽略且不回写 | ok |
| `measure_layout.py` | 逐块高度归因（定位「为什么 2.5 屏」） | 1325px → 611px |
| `prefix_cache3.py` | 三类插件全装 vs 零插件的前缀字节对比 | **10/10** |
| `go test ./...` | **9 个包**全 ok（含 `plugin` 的示例契约测试、`web` 的 boot-clip 测试） | 全 ok |

> 测试一律用 **7501** 端口。7500 是正在跑的那个 instance，别去碰。
>
> ⚠️ `e2e_*.py` 大多会自己拉一个 7501 实例。**跑之前先把占着 7501 的进程停掉**
> （包括自己起的演示实例，`taskkill /PID <pid> /F`），否则脚本会连到别人的服务器上，
> 于是出现「明明只装了 1 个插件却列出 3 个」这类假失败 —— 实测踩过。

---

## 十二、调试

```bat
REM 已装插件（含已禁用的）+ 进程状态
curl http://127.0.0.1:7500/api/plugins

REM 单独跑一次插件命令（参数走 stdin）
echo {"query":"sglang mtp"} | python -c "..."

REM 某个进程插件的日志尾巴
curl http://127.0.0.1:7500/api/plugins/process-demo/logs

REM 启动时若某个 plugin.json 坏了，日志里会有
REM   level=WARN msg="plugins: skipped" dir=... err=...
type %USERPROFILE%\.mini_code\agent.log | findstr /i plugins
```

规则：**一个插件坏掉不会拖垮启动**——`plugin.Load()` 跳过并记 WARN，核心照常运行；
`plugin.Discover()`（插件中心用的那个）会把它**列出来并显示错误**，否则用户
只会看到「插件消失了」而不知道为什么。
