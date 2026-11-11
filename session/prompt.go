package session

import (
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// Tool routing follows the dsh convention: name the dedicated tool AND explicitly forbid
// the shell spelling. "Batch your calls" alone measurably failed — on the SWE-bench round
// the model walked the tree with dir/type/findstr (43 shell calls) and used search_files
// 6 times, costing 1.43x the round trips dsh needed.
const toolConstraints = `<tool_requirements>
# Tool Usage Instructions
- Call tools only through structured tool_calls with valid JSON object parameters.
- Use the dedicated tools, not the shell: glob/list_dir to discover files, search_files to
  search file contents, read_file to read them. Do not walk the tree with exec (dir, type,
  findstr, more) to look at code — one dedicated call replaces several shell round trips.
- exec is for running things (tests, builds, git, package tools), not for inspecting files.
- Batch every independent call into one response: two or more read-only calls that do not
  depend on each other go out together, never one per turn.
</tool_requirements>
`
// Deliberately rate-limited: the old wording ("report the result of each tool used ...
// before/after tool calls") cost a progress line per tool call, i.e. extra output on
// every round trip.
const taskProgressAnchor = `<progress_anchor>
# Progress Reporting
- Report progress at most once every 3-4 turns, and only when something actually changed:
【当前进展】 目标: xxx | 已完成: xxx | 下一步: xxx
- Never spend a turn solely on progress — put the anchor in the same message as the work,
  and do not narrate individual tool calls.
</progress_anchor>`

const workConstraints = `<work_requirements>
- The "# Working Directory" block below names this session's folder — it is the same directory stored in the session's database row, and for a worker session it is the worker's shared workspace. Do all reading and writing inside it; database and prompt always mean the same folder.
- Verify with the project's own tests or commands, not with helper scripts you write. When a scratch script really is the shortest path, put it in a '_work_{timestamp}_{theme}' directory under the working directory (theme in the user's language) and delete it before you finish; final deliverables stay exactly where the task asked for them.
</work_requirements>`

type PromptBuilder struct {
	agentConfig *agent.AgentConfig
	Tools       *tools.ToolRegistry
	taskTool    *tools.TaskTool
	workDir     string
	// modelName feeds the {{model}} placeholder in the persona file, dsh-style
	// ("a coding agent powered by the {{model}} model"). Empty renders as
	// "default" so the prompt stays valid and byte-stable.
	modelName string
	// Worker content deliberately does NOT live here: the system prompt is frozen
	// into the stored history after the first turn, so the join reminder and the
	// 公约 travel as a user message instead (Session.WorkerNotice).
}

// SetModelName names the model for the {{model}} persona placeholder. Call it
// before the first BuildSystemPrompt of the session; the system prompt is
// frozen afterwards anyway.
func (pb *PromptBuilder) SetModelName(name string) { pb.modelName = name }

func NewPromptBuilder(config *agent.AgentConfig, workDir string) *PromptBuilder {
	pb := &PromptBuilder{
		agentConfig: config,
		Tools:       tools.NewToolRegistry(),
		workDir:     workDir,
	}
	pb.registerDefaultTools()
	// Plugin tools are appended after the core list (never interleaved): the
	// core segment of the tool schema array is the head of the prefix the
	// provider's cache matches, so installing a plugin must not move it.
	pb.registerPluginTools()
	return pb
}

func (pb *PromptBuilder) registerDefaultTools() {
	ws := pb.workDir
	pb.Tools.Register(tools.NewReadFileTool(ws))
	pb.Tools.Register(tools.NewWriteFileTool(ws))
	pb.Tools.Register(tools.NewEditFileTool(ws))
	pb.Tools.Register(tools.NewDeleteFileTool(ws))
	// move_file / copy_file are intentionally NOT registered: two independent
	// measurements (an 82-sample ablation and 257 real tool calls on the live
	// instance) recorded zero uses, and the environment section already teaches
	// the cmd/Unix spellings of copy/move/ren. The implementations stay in
	// tools/fileops.go, so a worker that wants a guaranteed-atomic copy/rename
	// can register them back with one line each. Saving is 837 + 577 = 1414 B
	// of tool schema per request (~400 tok of cache-busting prefix).
	pb.Tools.Register(tools.NewListDirTool(ws))
	pb.Tools.Register(tools.NewGlobTool(ws))
	pb.Tools.Register(tools.NewExecTool(120, ws))
	pb.Tools.Register(tools.NewSearchTool(ws))
	pb.Tools.Register(tools.NewSkillsTool(ws))
	pb.Tools.Register(tools.NewReadImgTool(ws))
}

// environmentSection tells the model which OS it runs on and which shell
// dialect the exec tool speaks. LLMs default to Unix syntax (ls, cat, grep,
// ";"); on Windows cmd.exe that fails on the first try, so an explicit,
// short cheat-sheet cuts failed exec turns without bloating the prompt.
func environmentSection() string {
	switch runtime.GOOS {
	case "windows":
		// The spellings are a translation aid for commands the dedicated tools do not
		// cover, not a recommendation: an earlier version read "findstr (not grep)",
		// which pushed the model to grep file contents through exec instead of through
		// search_files (measured: 25 dir/type/findstr calls per 3 tasks).
		return `# Environment

Operating system: Windows. The exec tool runs commands through cmd.exe, NOT a Unix shell.
- For code, prefer the dedicated tools over shell equivalents: read_file over type, search_files over findstr, glob/list_dir over dir /s /b. Reaching for the shell to inspect files turns one call into several round trips.
- When you do use the shell, these are the cmd spellings: dir, type, findstr, copy, move, ren, del/erase, mkdir (no -p flag). Chain with && (';' does not work). Quote paths that contain spaces; both / and \ work as path separators in most tools.
- Python is usually invoked as "python", not "python3"
- If a Unix-style command fails, translate it to its cmd equivalent immediately instead of retrying the same command.`
	case "darwin":
		return "# Environment\n\nOperating system: macOS. The exec tool runs commands through sh (POSIX shell)."
	default:
		return "# Environment\n\nOperating system: " + runtime.GOOS + ". The exec tool runs commands through sh (POSIX shell)."
	}
}

func (pb *PromptBuilder) BuildSystemPrompt(workDir, extraContext string) string {
	var parts []string
	if pb.agentConfig.Prompt != "" {
		// dsh-style persona: the file may name the model via {{model}}. Render it
		// here (on a copy — the shared AgentConfig must stay untouched) so the
		// system prompt is still byte-stable once built.
		persona := pb.agentConfig.Prompt
		name := pb.modelName
		if name == "" {
			name = "default"
		}
		persona = strings.ReplaceAll(persona, "{{model}}", name)
		parts = append(parts, persona)
	}
	parts = append(parts, toolConstraints)
	parts = append(parts, taskProgressAnchor)
	parts = append(parts, workConstraints)
	if workDir != "" {
		parts = append(parts, "\n# Working Directory\n\nThe current working directory is: "+workDir+"\nAll file operations must be confined to this directory.\n")
	}
	// Worker content is not part of the system prompt (it would be frozen with
	// it): see Session.WorkerNotice, which the loop appends as a user message.
	// User plugins from ~/.mini_code/plugins.md (one per line, optional
	// "desc:" suffix). Re-read on every task, so edits to the file take
	// effect from the next task without a restart.
	if ps := pluginsSection(); ps != "" {
		parts = append(parts, ps)
	}
	// Plugin memory: notes contributed by installed directory plugins
	// (~/.mini_code/plugins/<id>/memory.md). Placed after the plugin list and
	// before the environment block so the untrusted-ish user text stays in one
	// region of the prompt. Each file is static until edited, so the prefix
	// stays cacheable.
	if mem := pluginMemorySection(); mem != "" {
		parts = append(parts, mem)
	}
	// The system prompt is static on purpose: it is the head of the request
	// prefix the provider's prefix cache matches, so it must not be
	// re-rendered with fresh content (e.g. a timestamp) on every task.
	parts = append(parts, "\n"+environmentSection()+"\n")
	if extraContext != "" {
		parts = append(parts, "\n# Additional Context\n\n"+extraContext+"\n")
	}
	return strings.Join(parts, "\n")
}

// EnableTaskTool registers the task tool and the sub-agent control tools
// (task_output / task_status / task_cancel / task_list) for a primary,
// root session. Sub-agent sessions never get them (a sub-agent cannot
// dispatch further sub-agents).
func (pb *PromptBuilder) EnableTaskTool(mgr *SessionManager, sessionID, workDir string) {
	dispatcher := mgr.Subagents()
	pb.taskTool = tools.NewTaskTool()
	pb.taskTool.SetSessionContext(sessionID, dispatcher)
	pb.Tools.Register(pb.taskTool)
	pb.Tools.Register(tools.NewTaskOutputTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewTaskStatusTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewTaskCancelTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewTaskListTool(sessionID, dispatcher))
	slog.Debug("TaskTool enabled", "session", sessionID)
}

// EnableContextTools registers the context-management tools (context_usage /
// compact_context), wired to the per-task context state the agent loop updates
// each turn. Every session gets its own ContextState, so each session manages
// its own context.
func (pb *PromptBuilder) EnableContextTools(state *tools.ContextState) {
	pb.Tools.Register(tools.NewContextUsageTool(state))
	pb.Tools.Register(tools.NewCompactContextTool(state))
}

// EnableWorkerTools registers the workers tool (工作大类: discover, create,
// join, and the shared convention file).
//
// It is registered for every ROOT session, whether or not it already belongs to
// a worker. That is deliberate: a session has to be able to decide, and the
// decision changes the session's state mid-life — if the schema appeared only
// for unattached sessions, every join would also change the tool list and thus
// the request prefix. One constant schema costs a few hundred schema bytes and
// saves a break on every join.
func (pb *PromptBuilder) EnableWorkerTools(sessionID string, api tools.WorkerDispatcher) {
	if api == nil {
		return
	}
	pb.Tools.Register(tools.NewWorkersTool(api))
	slog.Debug("WorkersTool enabled", "session", sessionID)
}

// EnableGroupTool registers the group_chat tool for a group-member session.
// api is the per-session backend (group info/log + delivery); it is read
// live on every call, so mid-task membership changes are reflected.
func (pb *PromptBuilder) EnableGroupTool(sessionID string, api tools.GroupChatDispatcher) {
	t := tools.NewGroupChatTool()
	t.SetContext(api)
	pb.Tools.Register(t)
	slog.Debug("GroupChatTool enabled", "session", sessionID)
}

// GetFilteredDefinitions returns the tool schemas the agent role may use.
//
// The role's permission table is compiled in (agent/basePermissions) and only
// lists the core tools, so a plugin tool would otherwise be filtered out and the
// model would never see it. Appended (plugin) tools are therefore allowed by
// default unless the role explicitly denies that exact name — a plugin still
// cannot sneak past an explicit "deny".
func (pb *PromptBuilder) GetFilteredDefinitions(cfg *agent.AgentConfig) []provider.ToolSchema {
	defs := pb.Tools.GetDefinitions(false)
	defs = dropDisabledTools(defs)
	if cfg == nil || len(cfg.Permissions.Tools) == 0 {
		return defs
	}
	var filtered []provider.ToolSchema
	for _, d := range defs {
		name := d.Function.Name
		if cfg.Permissions.IsToolAllowed(name, "") {
			filtered = append(filtered, d)
			continue
		}
		if pb.Tools.IsAppended(name) && !explicitlyDenied(cfg, name) {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// ablationEnv is the tool-ablation hook: MC_DISABLE_TOOLS="copy_file,glob,task"
// (comma separated names) drops those schemas from every request in this process.
// It exists to measure what a smaller tool list costs or saves — the schema array
// is part of the cached prompt prefix, so the set must be fixed per process: it is
// read once and frozen. Empty/unset means production behaviour (nothing dropped).
const ablationEnv = "MC_DISABLE_TOOLS"

var (
	ablationOnce     sync.Once
	ablationDisabled map[string]bool
)

func disabledTools() map[string]bool {
	ablationOnce.Do(func() {
		ablationDisabled = parseDisabledTools(os.Getenv(ablationEnv))
	})
	return ablationDisabled
}

// parseDisabledTools turns the env value into a set ("a, b ,,c" -> {a,b,c}).
func parseDisabledTools(v string) map[string]bool {
	set := map[string]bool{}
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
}

// dropDisabledTools removes the ablated tools. It runs before the role filter so
// plugin tools (web_search, sessions_*) can be ablated too.
func dropDisabledTools(defs []provider.ToolSchema) []provider.ToolSchema {
	return filterOutTools(defs, disabledTools())
}

// filterOutTools is the pure half, so the removal rule stays testable even though
// the env value itself is read once and frozen per process.
func filterOutTools(defs []provider.ToolSchema, dis map[string]bool) []provider.ToolSchema {
	if len(dis) == 0 || len(defs) == 0 {
		return defs
	}
	kept := make([]provider.ToolSchema, 0, len(defs))
	for _, d := range defs {
		if dis[d.Function.Name] {
			continue
		}
		kept = append(kept, d)
	}
	return kept
}

// explicitlyDenied reports whether the role names this tool with a "deny"
// action. An absent entry is not a deny: the compiled tables predate plugins.
func explicitlyDenied(cfg *agent.AgentConfig, name string) bool {
	tp, ok := cfg.Permissions.Tools[name]
	if !ok {
		return false
	}
	return !tp.IsAllowed("")
}
