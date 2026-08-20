package session

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/user/mini_code/agent"
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

const toolConstraints = `<tool_requirements>
# Tool Usage Instructions
- Tool calls must be made exclusively through structured tool_calls with valid JSON object parameters.
- By default, all independent tool calls within a single response are executed in a batch — parallel execution improves efficiency.
- It is prohibited to use text simulating the ` + "<tool_call></tool_call>" + ` format within the body of the text to make unauthorized calls.
- The format of the JSON parameters must be valid.
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

// currentTimeSection tells the model the actual current date and time. The
// LLM has no clock of its own and silently guesses dates from its training
// data ("today" is always a stale 2024/2025 date); date-related tasks
// (naming files by date, "how many days until X", weekday questions) then
// fail. Injecting the real timestamp costs a few tokens and makes every
// date-dependent task correct by construction.
func currentTimeSection() string {
	now := time.Now()
	_, offset := now.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("# Current Time\n\nThe current date and time is: %s (%s), UTC%s%02d:%02d.\nUse this as the reference for anything date- or time-related (\"today\", \"this week\", file names containing dates, relative-date math). Never guess the current date from memory; if you need more precision or another timezone, verify with exec (e.g. `date`).",
		now.Format("2006-01-02 15:04:05"), now.Format("Monday"), sign, offset/3600, (offset%3600)/60)
}

func (pb *PromptBuilder) BuildSystemPrompt(workDir, extraContext string) string {
	var parts []string
	if pb.agentConfig.Prompt != "" {
		parts = append(parts, pb.agentConfig.Prompt)
	}
	parts = append(parts, toolConstraints)
	parts = append(parts, taskProgressAnchor)
	parts = append(parts, extraConstraints)
	if workDir != "" {
		parts = append(parts, "\n# Working Directory\n\nThe current working directory is: "+workDir+"\nAll file operations must be confined to this directory.\n")
	}
	parts = append(parts, "\n"+currentTimeSection()+"\n")
	parts = append(parts, "\n"+environmentSection()+"\n")
	if extraContext != "" {
		parts = append(parts, "\n# Additional Context\n\n"+extraContext+"\n")
	}
	return strings.Join(parts, "\n")
}

func (pb *PromptBuilder) EnableTaskTool(executor tools.SessionCreator, sessionID, workDir string) {
	pb.taskTool = tools.NewTaskTool()
	pb.taskTool.SetSessionContext(executor, sessionID, workDir)
	pb.Tools.Register(pb.taskTool)
	slog.Debug("TaskTool enabled", "session", sessionID)
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

