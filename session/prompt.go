package session

import (
	"log/slog"
	"runtime"
	"strings"

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
- Place all scripts, dependencies, and all files needed or generated during task execution into _work_{timestamp} under current directory.
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
}

func NewPromptBuilder(config *agent.AgentConfig, workDir string) *PromptBuilder {
	pb := &PromptBuilder{
		agentConfig: config,
		Tools:       tools.NewToolRegistry(),
		workDir:     workDir,
	}
	pb.registerDefaultTools()
	return pb
}

func (pb *PromptBuilder) registerDefaultTools() {
	ws := pb.workDir
	pb.Tools.Register(tools.NewReadFileTool(ws))
	pb.Tools.Register(tools.NewWriteFileTool(ws))
	pb.Tools.Register(tools.NewEditFileTool(ws))
	pb.Tools.Register(tools.NewDeleteFileTool(ws))
	pb.Tools.Register(tools.NewMoveFileTool(ws))
	pb.Tools.Register(tools.NewCopyFileTool(ws))
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
	// User plugins from ~/.mini_code/plugins.md (one per line, optional
	// "desc:" suffix). Re-read on every task, so edits to the file take
	// effect from the next task without a restart.
	if ps := pluginsSection(); ps != "" {
		parts = append(parts, ps)
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

func (pb *PromptBuilder) GetFilteredDefinitions(cfg *agent.AgentConfig) []provider.ToolSchema {
	defs := pb.Tools.GetDefinitions(false)
	if cfg == nil || len(cfg.Permissions.Tools) == 0 {
		return defs
	}
	var filtered []provider.ToolSchema
	for _, d := range defs {
		if cfg.Permissions.IsToolAllowed(d.Function.Name, "") {
			filtered = append(filtered, d)
		}
	}
	return filtered
}
