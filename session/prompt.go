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

const toolConstraints = `<tool_requirements>
# Tool Usage Instructions
- Tool calls must be made exclusively through structured tool_calls with valid JSON object parameters.
- By default, all independent tool calls within a single response are executed in a batch — parallel execution improves efficiency.
</tool_requirements>
`
const taskProgressAnchor = `<progress_anchor>
# Execution Requirements
- At the beginning of a response or before/after tool calls, output a structured progress anchor **when appropriate**.
- Must output the progress anchor when: a sub-step is completed, phase changes, context becomes long, or risk of drifting off-task appears.
- Progress anchor format:
【Current Progress】 Goal: xxx | Completed: 1.xxx | Next: 1.xxx | Status: Executing...

- Keep all responses concise. Refresh full progress at least every 3-4 turns.
- Self-reminder: If reasoning becomes lengthy, immediately stop and refocus on the main task.
</progress_anchor>`

const workConstraints = `<work_requirements>
# Work Requirements
- Place all scripts, dependencies, and all files needed or generated during task execution into '_work_{timestamp}_{theme}' under the current directory. The theme must use the user's language.
- If the work involves SSH or remote login to other terminals, still create working files in the remote terminal using the same format, and place all project-related files there.
- For temporary files only; no final outputs. Auto-cleaned after task completion.
</work_requirements>`

const extraConstraints = `<extra_requirements>
# Extra Requirements
- You must tell the user of the result after each tool is used.
- It is important to regularly report the execution progress to users.
</extra_requirements>`

type PromptBuilder struct {
	agentConfig *agent.AgentConfig
	Tools       *tools.ToolRegistry
	taskTool    *tools.TaskTool
	workDir     string
	// Worker content deliberately does NOT live here: the system prompt is frozen
	// into the stored history after the first turn, so the join reminder and the
	// 公约 travel as a user message instead (Session.WorkerNotice).
}

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
		return `# Environment

Operating system: Windows. The exec tool runs commands through cmd.exe, NOT a Unix shell. Use Windows syntax:
- dir (not ls), type (not cat), findstr (not grep), copy (not cp), move (not mv), del/erase (not rm), mkdir (no -p flag), ren (not mv)
- Chain commands with && (the ; separator does not work in cmd)
- Quote paths that contain spaces; both / and \ work as path separators in most tools
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
		parts = append(parts, pb.agentConfig.Prompt)
	}
	parts = append(parts, toolConstraints)
	parts = append(parts, taskProgressAnchor)
	parts = append(parts, workConstraints)
	parts = append(parts, extraConstraints)
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
