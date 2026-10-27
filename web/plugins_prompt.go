// The "make me a plugin" prompts.
//
// The plugin centre has a 造插件 button. It does not open a form: it starts a
// chat and hands the model a prompt that contains everything the plugin contract
// requires, so the agent can write a working plugin without the user reading any
// documentation. The prompt is built here (not in the UI) so it can quote the
// real slot list, the real file paths and the real validation rules — a prompt
// that drifts from the loader is worse than no prompt at all.
package web

import (
	"net/http"
	"strings"

	"github.com/user/mini_code/plugin"
)

// handlePluginMakePrompt: GET /api/plugins/prompt?kind=process|tool|appearance
//
// Returns the prompt text for a new session. The UI puts it in the composer and
// the user presses send, which keeps the user in control of what the model is
// asked to do (and lets them add "and make it pink" before sending).
func (s *server) handlePluginMakePrompt(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == "" {
		kind = plugin.CategoryAppearance
	}
	switch kind {
	case plugin.CategoryProcess, plugin.CategoryTool, plugin.CategoryAppearance:
	default:
		writeErr(w, http.StatusBadRequest, "kind 只能是 process / tool / appearance")
		return
	}
	dir, _ := plugin.DirPath()
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":   kind,
		"prompt": pluginMakePrompt(kind, dir),
		"dir":    dir,
	})
}

// pluginMakePrompt builds the instruction text for one category.
func pluginMakePrompt(kind, dir string) string {
	var b strings.Builder
	b.WriteString("帮我做一个 mini_code 插件。\n\n")
	switch kind {
	case plugin.CategoryProcess:
		b.WriteString("类型:**程序类**(第一类)—— 一个 mini_code 负责启动、保活、并在需要时给你看的独立进程。\n\n")
		b.WriteString("要求:\n")
		b.WriteString("1. 在 `" + dir + "\\<插件id>\\` 下建好目录,写一个 `plugin.json`;\n")
		b.WriteString("2. `plugin.json` 的 `kind` 为 `\"process\"`,并带 `process` 字段:`command`、`args`、`autostart`、`restart`(always/on-failure/never)、`health_check`、`ready_timeout_seconds`;\n")
		b.WriteString("3. 如果要让我能查到它,加 `expose: {\"tool\": true, \"endpoint\": \"http://127.0.0.1:端口\"}` —— 这会给模型一个 `plugin_proc__<id>` 工具来查状态和日志;\n")
		b.WriteString("4. 程序本体可以是 python / node / 任何一个 exe。它必须能被非交互地启动,日志打到 stdout;\n")
		b.WriteString("5. 插件 id 只能用小写字母、数字、减号,且必须和目录名一致。\n\n")
	case plugin.CategoryTool:
		b.WriteString("类型:**工具类**(第二类)—— 给模型追加一个工具,像网页搜索那样。\n\n")
		b.WriteString("要求:\n")
		b.WriteString("1. 在 `" + dir + "\\<插件id>\\` 下建好目录,写一个 `plugin.json`;\n")
		b.WriteString("2. `kind` 为 `\"tool\"`,`tools` 数组里每一项要有 `name`(^[a-z][a-z0-9_]{1,63}$)、`description`、`parameters`(JSON Schema)、`command`、`args`、`timeout_seconds`;\n")
		b.WriteString("3. **工具参数是从 stdin 传进来的 JSON**,不是命令行参数。脚本开头应该是 `data = json.load(sys.stdin)`;\n")
		b.WriteString("4. 输出直接打印到 stdout;超过 32KB 会被截断。失败时非零退出并往 stderr 写原因;\n")
		b.WriteString("5. 不要和内置工具重名(read_file / write_file / exec / search / task…),重名会被忽略;\n")
		b.WriteString("6. 插件 id 只能用小写字母、数字、减号,且必须和目录名一致。\n\n")
	default:
		b.WriteString("类型:**外观类**(第三类)—— 皮肤、壁纸、槽位、动画。**不需要写 JavaScript**,也不需要编译。\n\n")
		b.WriteString("要求:\n")
		b.WriteString("1. 在 `" + dir + "\\<插件id>\\` 下建好目录,写一个 `plugin.json`;\n")
		b.WriteString("2. `kind` 为 `\"appearance\"`,可用字段:\n")
		b.WriteString("   - `styles: [\"skin.css\"]` —— CSS,注入在内置样式之后,可覆盖一切;\n")
		b.WriteString("   - `wallpaper: \"bg.webp\"` —— 铺在消息区的背景图;\n")
		b.WriteString("   - `ui.accent: \"#ff7500\"` —— 只换强调色,不用写 CSS;\n")
		b.WriteString("   - `ui.slots` —— 往页面指定位置塞一段 HTML,可用 slot:" + strings.Join(plugin.SlotNames(), " / ") + ";\n")
		b.WriteString("   - `ui.motion` —— 把事件(send/reply/error/tool/session)绑定到皮肤里定义的 CSS 动画名;\n")
		b.WriteString("   - `boot: {\"entry\": \"index.html\", \"durationMs\": 8000}` —— 开机动画。" +
			"声明了它,这个插件同时就是一段启动画面:入口是插件目录里的 HTML,可以带自己的 css/js/音频。\n")
		b.WriteString("3. **一般资产不允放 .js**,资产路由会拒绝;唯一的例外是 `boot` 声明的开机动画 —— " +
			"它的文件走 `/bootanim/<id>/…`,跑在沙箱 iframe 里(sandbox 只有 allow-scripts,没有同源),\n")
		b.WriteString("   所以它能动画、能出声,但读不到本应用的存储、也调不到本应用的接口(CSP connect-src 'none');\n")
		b.WriteString("   主题和宿主说话只能用 `parent.postMessage({mc:'boot-anim', type:'ready|step|handoff|done'}, '*')`,必须自己兜住超时;\n")
		b.WriteString("4. 颜色请用 CSS 变量和 `prefers-color-scheme`,同时兼容浅色和深色;\n")
		b.WriteString("5. 插件 id 只能用小写字母、数字、减号,且必须和目录名一致。\n\n")
	}
	b.WriteString("做完之后:\n")
	b.WriteString("- 把目录放到 `" + dir + "` 下(已经在了就不用动);\n")
	b.WriteString("- 告诉我怎么看效果(打开 设置 → 插件中心 → 重新扫描 → 启用);\n")
	b.WriteString("- 如果有外部依赖(装 python 包、下载字体之类),明确写出来,别默默假设它已经装好了。\n")
	b.WriteString("\n先问我想要什么风格/功能,再动手。")
	return b.String()
}
