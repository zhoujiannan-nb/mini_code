# 插件系统（目录式插件）

> 一句话：把插件目录丢进 `~/.mini_code/plugins/`，写一个 `plugin.json`，
> 点「配置 → 插件 → 重新扫描」就生效。不改页面、不重编译、不重启。

---

## 一、边界：什么是可插件的，什么不是

| 部分 | 可插件化 | 原因 |
|---|---|---|
| 工具（`tools`） | ✅ 只能**追加** | 插件工具挂在核心工具之后，核心列表的字节序列不变 |
| 皮肤（`styles`） | ✅ | 在内置样式之后注入，纯 CSS |
| 壁纸（`wallpaper`） | ✅ | 一条 `background-image` |
| 记忆（`memory`） | ✅ | 追加到系统提示的静态文本 |
| 开机动画（`bootanim`） | ✅ | 已有独立机制：`config.BootAnim` + `web/bootanim.go` |
| **agent 循环** | ❌ | 任务执行、重试、放弃逻辑是核心语义 |
| **权限 / 闸门** | ❌ | 插件只会让闸门**更严**（谨慎模式必须审批），不能放宽 |
| **上下文压缩** | ❌ | 决定会话能否活过窗口，不能交给第三方 |
| **基础工具** | ❌ | `read_file`/`write_file`/`exec`/`search_files` 等的语义是契约 |

`plugin` 包**不 import** `agent` / `session` / `approval`，
`check_plugins.py` 第 1 项会静态断言这一点——插件层拿不到核心的任何句柄。

---

## 二、目录结构

```
~/.mini_code/plugins/
└── web-search/                ← 目录名可以随便，但建议和 id 一致
    ├── plugin.json            ← 唯一必需的文件
    ├── memory.md              ← 可选：追加到系统提示
    ├── skin.css               ← 可选：皮肤
    └── bg.webp                ← 可选：壁纸
```

参考实现见仓库里的 `examples/plugins/`：

- `web-search/` —— 工具 + 记忆（包装 `D:\project_ai\web_search` 的浏览器驱动）
- `skin-night/` —— 纯皮肤（只覆盖 4 个 CSS 变量）
- `wallpaper-demo/` —— 纯壁纸（一张 1200×800 的 SVG 渐变）

---

## 三、plugin.json

```json
{
  "id": "web-search",
  "name": "网页搜索 / 浏览器",
  "version": "1.0.0",
  "author": "",
  "description": "通过本地浏览器驱动搜索网页并读取正文。",
  "enabled": true,

  "tools": [
    {
      "name": "web_search",
      "description": "Search the web and return JSON results.",
      "parameters": {
        "type": "object",
        "properties": {
          "query": { "type": "string", "description": "what to search" },
          "engine": { "type": "string", "enum": ["bing", "baidu", "google"] }
        },
        "required": ["query"]
      },
      "command": "python",
      "args": ["-c", "...", "..."],
      "timeout_seconds": 150
    }
  ],

  "styles": ["skin.css"],
  "wallpaper": "bg.webp",
  "memory": "memory.md"
}
```

### 字段规则

| 字段 | 规则 |
|---|---|
| `id` | 必填，缺省取目录名。`^[a-z0-9][a-z0-9-]{0,63}$`（要能当 URL 路径段） |
| `enabled` | 缺省 = `true`。`false` 时整个插件不加载 |
| `tools[].name` | `^[a-z][a-z0-9_]{1,63}$`，不能与核心工具重名（重名会被忽略并记警告） |
| `tools[].command` | 必填。直接 exec，**不经过 shell**——参数是自己的 `args` 数组，不会二次解析 |
| `tools[].timeout_seconds` | 缺省 60，上限 600。超时返回工具结果，不是崩溃 |
| `styles` / `wallpaper` / `memory` | 必须是插件目录内的**相对路径**，绝对路径和 `..` 会被拒绝 |

### 工具如何拿到参数

模型的参数对象以 **JSON 写到子进程的 stdin**，工作目录是插件目录。
所以命令本身在 manifest 里是**静态**的（不进命令行 → 没有引号/注入问题），
脚本这样读：

```python
import json, sys
params = json.load(sys.stdin)
print(main(params["query"]))
```

### 子进程环境

`plugin/tool.go` 会给子进程加：

```
PYTHONIOENCODING=utf-8
PYTHONUTF8=1
LANG=en_US.UTF-8
LC_ALL=en_US.UTF-8
```

原因：Windows 上子进程默认继承控制台代码页（中文系统是 GBK），
Python 往 stdout 打中文会直接 `UnicodeEncodeError` 崩掉。这是实测踩到的真 bug
（见「七、踩过的坑」）。另外输出还会过一遍 UTF-8 清洗，非 UTF-8 字节被替换成 `�`，
保证工具结果一定能 JSON 序列化进下一次请求。

---

## 四、前端如何注入

`web/plugins.go` 在 `handleIndex` 里把 `<!--mc-plugin-assets-->` 替换成：

```html
<link rel="stylesheet" href="/plugins/skin-night/skin.css">
<style id="mc-plugin-wallpaper">#messages{background-image:url("/plugins/skin-night/bg.webp");
  background-size:cover;background-position:center;background-repeat:no-repeat;
  background-attachment:scroll}</style>
```

注入点在内置 `base.css` / `layers.css` **之后**，所以皮肤能覆盖内置主题。
没有装插件时替换成空串，页面与不装插件时逐字节一致。

壁纸的四个属性都是**显式**写的，不是靠默认值：

| 属性 | 为什么必须写明 |
|---|---|
| `background-size: cover` | 缩放填满消息栏，不重复、不变形 |
| `background-repeat: no-repeat` | CSS 默认是 `repeat`；平铺的照片从来不是壁纸的本意 |
| `background-position: center` | 居中裁切 |
| `background-attachment: scroll` | 固定在消息框内，不随对话滚走 |

多个插件都声明壁纸时**第一个（按 id 排序）胜出**，避免属性打架、顺序不定。

皮肤只写变量就够了，因为 `base.css` 里所有颜色都走 `var(--*)`：

```css
:root { --accent: #2f8fb5; --accent-t: #1f6f92; }
html[data-theme="dark"] { --accent: #5fc4e8; --accent-t: #8fd8f2; }
```

### 资源路由的安全约束

| 约束 | 说明 |
|---|---|
| 扩展名白名单 | 只有 `.css .svg .png .jpg .jpeg .webp .gif .woff2`。**没有 `.js`、没有 `.html`** —— 插件不能往应用同源里塞脚本 |
| 路径包含 | 相对路径 + 二次校验（`filepath.EvalSymlinks` 之后再比一次），`..` 与符号链接都出不去 |
| 响应头 | `nosniff` + `Cache-Control: no-store` + `Content-Security-Policy: default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self'` |

`GET /api/plugins` 返回已装列表（id / name / version / kinds / tools / dir，按 id 排序），
设置页的「插件」一栏就是渲染这个。

---

## 五、前缀缓存为什么不会退化

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
3. 插件工具若与核心工具重名，直接跳过（`check_plugins.py` 第 4 项）——
   否则那个已缓存名字的语义会被换掉。
4. 插件记忆是磁盘上的静态文本，不含时间戳/随机数（`check_plugins.py` 第 7 项）。
5. 插件按 id 排序加载，顺序确定。

### 实测（`cache_compare.py`，同一段两轮对话，装 / 不装插件各跑一次）

| | prompt | cached | 命中率 |
|---|---:|---:|---:|
| 无插件 · turn 1 | 3,877 | 3,712 | 95.7% |
| 无插件 · turn 2 | 3,888 | 3,712 | 95.5% |
| 有插件 · turn 1 | 4,353 | 4,219 | 96.9% |
| 有插件 · turn 2 | 4,364 | 4,224 | 96.8% |

- 装插件只多了 **476** 个 prompt token（就是 2 个插件工具的 schema），
- 命中率差别 **1.2 个百分点**（有插件反而略高，因为固定的 schema 段更长、可复用比例更大），
- 每一次 LLM 调用的命中率曲线逐条对齐，最大偏差 1.3 个百分点。

### 关于「工具循环里后面的调用命中率会掉」

一轮带工具调用的任务里，第 2、3… 次 LLM 调用的 prompt 会追加**本次新产生的工具结果**，
这部分天然无法命中缓存，所以命中率必然下滑。这与插件无关：
**不装插件同样下滑**。判定口径因此取「每个用户轮次的第一次调用」，
以及「同一对话装/不装插件的逐调用对齐」——两者都在 90% 以上。

---

## 六、权限：插件只会让闸门更严

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

---

## 七、踩过的坑（实测记录）

| 现象 | 原因 | 修法 |
|---|---|---|
| 模型说「我没有 web_search 工具」 | 角色权限表过滤掉了插件工具 | `GetFilteredDefinitions` / `gateFor` 对「已启用插件工具」放行（显式 deny 仍生效） |
| 插件工具返回 `UnicodeEncodeError: 'gbk' codec can't encode` | Windows 子进程继承 GBK 代码页 | 子进程注入 `PYTHONUTF8=1` / `PYTHONIOENCODING=utf-8` |
| 工具输出含非 UTF-8 字节时发消息失败 | JSON 序列化不接受非法 UTF-8 | `sanitizeUTF8()` 把非法字节替换为 `�` |
| 插件目录里的 `evil.js` 被同源加载 | —— | 白名单只放静态样式/图片，`.js`/`.html` 一律 403 |
| 壁纸「看起来没生效」 | **不是功能 bug，是我自己的测试图坏了**：1-bit 调色板 PNG 带白色调色板项，`cover` 放大后大片是白，截图采样正好落在白色上 | 换成 8-bit truecolour 纯色图；判定改成像素统计 + 「移除规则后画面必须变化」的差分 |

最后一条值得记下来，因为它改变了这条链路的测试方式：

**只看 `getComputedStyle().backgroundImage` 是没用的**——它可能完全正确而画面上什么都没有；
反过来也可能正确且画上去了却被别的东西盖住。壁纸/皮肤这类"看起来应该对"的东西，
断言必须落在**像素**上：

```
1. 装一个纯色（#FF0000）32×32 truecolour PNG 当壁纸
2. 截整页图，取 #messages 左内边距那一条竖线（内容卡片够不到的地方）逐点采样
3. 断言 >95% 的采样点是红色
4. 再删掉 mc-plugin-wallpaper 规则重截，断言画面有 >50% 的采样点发生变化
```

第 4 步是关键：它不依赖壁纸的颜色、也不依赖布局，只问「这条规则到底有没有改变画面」。

---

## 八、调试

```bat
REM 列出已装插件（JSON）
curl http://127.0.0.1:7500/api/plugins

REM 单独跑一次插件命令（参数走 stdin）
echo {"query":"sglang mtp"} | python -c "..."

REM 启动时若某个 plugin.json 坏了，日志里会有
REM   level=WARN msg="plugins: skipped" dir=... err=...
type %USERPROFILE%\.mini_code\agent.log | findstr /i plugins
```

规则：**一个插件坏掉不会拖垮启动**——`plugin.Load()` 跳过并记 WARN，核心照常运行。
