package session

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	pb.Tools.Register(tools.NewListDirTool(ws))
	pb.Tools.Register(tools.NewExecTool(120, ws))
	pb.Tools.Register(tools.NewSkillsTool(ws))
	pb.Tools.Register(tools.NewReadImgTool(ws))
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

func loadPromptFile(name string) string {
	path := filepath.Join("agent", "prompts", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
