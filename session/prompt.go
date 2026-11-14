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

const workConstraints = `<work_requirements>
- The "# Working Directory" block below names this session's folder — it is the same directory stored in the session's database row, and for a worker session it is the worker's shared workspace. Do all reading and writing inside it; database and prompt always mean the same folder.
- When a scratch script is the shortest path, put it in a '_work_{timestamp}_{theme}' directory under the working directory (theme in the user's language) and delete it before you finish; final deliverables stay exactly where the task asked for them.
</work_requirements>`

type PromptBuilder struct {
	agentConfig *agent.AgentConfig
	Tools       *tools.ToolRegistry
	workDir     string
	// goalStore backs create_goal / get_goal / update_goal for this session
	// (dsh's goal tools). Created on first EnableAgentTools call.
	goalStore *tools.GoalStore
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

// registerDefaultTools builds the core tool surface. The names, schemas and
// descriptions are dsh's verbatim core set (see tools/dsh_tools.json):
// read / write / edit / glob / grep / pwsh / read_image / skill / todo_write /
// working_directory / web_fetch. The job tools (job_output / job_list /
// job_kill) attach in EnableJobTools, the root-only extras in EnableAgentTools.
//
// mc's older names (read_file, write_file, edit_file, search_files, exec,
// list_dir, delete_file, readimg, skills, ...) are NOT registered: the
// implementations accept both the old and the new argument keys, but only the
// dsh names are exposed to the model.
func (pb *PromptBuilder) registerDefaultTools() {
	ws := pb.workDir
	pb.Tools.Register(tools.NewReadFileTool(ws))
	pb.Tools.Register(tools.NewWriteFileTool(ws))
	pb.Tools.Register(tools.NewEditFileTool(ws))
	pb.Tools.Register(tools.NewGlobTool(ws))
	pb.Tools.Register(tools.NewSearchTool(ws))
	execTool := tools.NewExecTool(120, ws)
	pb.Tools.Register(execTool)
	pb.Tools.Register(tools.NewReadImgTool(ws))
	pb.Tools.Register(tools.NewSkillsTool(ws))
	pb.Tools.Register(tools.NewTodoTool())
	// working_directory moves the pwsh tool's default directory, exactly like
	// dsh's cd tool moves its shell host.
	cdTool := tools.NewWorkingDirectoryTool(ws)
	cdTool.SetExecHost(execTool)
	pb.Tools.Register(cdTool)
	pb.Tools.Register(tools.NewWebFetchTool())
	// list_dir / delete_file / move_file / copy_file are intentionally NOT
	// registered: dsh's surface has no such tools (the shell covers them), and
	// earlier measurements recorded zero uses. Implementations stay in
	// tools/fileops.go for one-line re-registration.
}

// EnableJobTools attaches the tools that read and stop this session's background
// commands, and installs the hook that turns a job's lifecycle into ledger rows
// plus (when nobody was waiting on it) a settlement notice in the conversation.
//
// The registry is not created here: pwsh owns it, because pwsh is what hands work
// to it. Taking it from the tool instead of injecting it keeps the wiring honest
// — the job tools can only ever see the commands this session's pwsh actually
// started. Called once per session, like EnableAgentTools.
func (pb *PromptBuilder) EnableJobTools(mgr *SessionManager, sessionID string) {
	host, ok := pb.Tools.Get("pwsh").(tools.JobHost)
	if !ok || host.Jobs() == nil {
		slog.Warn("pwsh tool exposes no job registry; background commands unavailable", "session", sessionID)
		return
	}
	reg := host.Jobs()
	reg.SetListener(jobListener(mgr, sessionID))
	for _, jt := range tools.NewJobTools(reg) {
		pb.Tools.Register(jt)
	}
	slog.Debug("job tools enabled", "session", sessionID)
}

// environmentSection tells the model which OS it runs on and which shell
// dialect the pwsh tool speaks. The pwsh tool runs one fresh process per call
// (dsh semantics), so the section says so and keeps the dialect cheat-sheet
// short: an explicit translation aid cuts failed shell turns without bloat.
func environmentSection() string {
	switch runtime.GOOS {
	case "windows":
		return `# Environment

Operating system: Windows. Commands run through PowerShell (pwsh when installed, otherwise Windows PowerShell), one fresh process per pwsh tool call — pass workdir instead of relying on cd carrying over.
- For code, prefer the dedicated tools over shell equivalents: read over type/Get-Content, grep over Select-String, glob over Get-ChildItem -Recurse. Reaching for the shell to inspect files turns one call into several round trips.
- If a Unix-style command fails, translate it to its PowerShell equivalent immediately instead of retrying the same command.`
	case "darwin":
		return "# Environment\n\nOperating system: macOS. Commands run through pwsh (PowerShell), one fresh process per pwsh tool call."
	default:
		return "# Environment\n\nOperating system: " + runtime.GOOS + ". Commands run through pwsh (PowerShell), one fresh process per pwsh tool call."
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
	parts = append(parts, workConstraints)
	if workDir != "" {
		parts = append(parts, "\n# Working Directory\n\nThe current working directory is: "+workDir+"\nAll file operations must be confined to this directory.\n")
	}
	// Worker content is not part of the system prompt (it would be frozen with
	// it): see Session.WorkerNotice, which the loop appends as a user message.
	// Project instructions (AGENTS.md ancestor chain + user globals, dsh's
	// agent-instructions) load here — statically, so the prefix cache survives.
	if ins := agentsInstructionsSection(workDir); ins != "" {
		parts = append(parts, ins)
	}
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

// EnableAgentTools registers the root-only orchestration tools — dsh's root
// surface: subagent / subagent_fork / send_message / list_agents / wait_agent /
// create_goal / get_goal / update_goal — for a primary, root
// session. Sub-agent sessions never get them, which is what keeps the tree one
// level deep: a child cannot start children of its own. (workflow is the one
// root tool NOT registered here; see the note at the end of this function.)
//
// One constant tool list, registered whether or not the session has agents yet:
// the schema array is the head of the request prefix the provider's cache
// matches, so it must not appear or disappear mid-conversation.
func (pb *PromptBuilder) EnableAgentTools(mgr *SessionManager, sessionID string) {
	dispatcher := mgr.Subagents()
	agentTool := tools.NewAgentTool()
	agentTool.SetSessionContext(sessionID, dispatcher)
	pb.Tools.Register(agentTool)
	pb.Tools.Register(tools.NewSubagentForkTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewSendMessageTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewListAgentsTool(sessionID, dispatcher))
	// wait_agent's session-side half: the session's injection queue is where
	// every input it may wait for lands (user steering, sub-agent messages,
	// settlement notices), so the tool gets the loaded session as its waiter.
	waitTool := tools.NewWaitAgentTool(sessionID, dispatcher)
	if s := mgr.Peek(sessionID); s != nil {
		waitTool.SetInputWaiter(s)
	}
	pb.Tools.Register(waitTool)
	if pb.goalStore == nil {
		pb.goalStore = tools.NewGoalStore()
	}
	pb.Tools.Register(tools.NewCreateGoalTool(pb.goalStore))
	pb.Tools.Register(tools.NewGetGoalTool(pb.goalStore))
	pb.Tools.Register(tools.NewUpdateGoalTool(pb.goalStore))
	// workflow is NOT registered (recorded deviation): its dsh description is
	// a full JavaScript orchestration engine (agent/parallel/pipeline hooks),
	// MiniCode has no JS engine and vendoring goja is out of scope, and a
	// tool whose description promises an engine while Execute answers
	// "unavailable" is a lying surface. The honest placeholder implementation
	// stays in tools/dsh_newtools.go for one-line re-registration, and
	// build.txt no longer mentions it.
	slog.Debug("agent tools enabled", "session", sessionID)
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
