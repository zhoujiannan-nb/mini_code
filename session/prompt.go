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
	goalStore *tools.GoalStore
	modelName string
}

func (pb *PromptBuilder) SetModelName(name string) { pb.modelName = name }

func NewPromptBuilder(config *agent.AgentConfig, workDir string) *PromptBuilder {
	pb := &PromptBuilder{
		agentConfig: config,
		Tools:       tools.NewToolRegistry(),
		workDir:     workDir,
	}
	pb.registerDefaultTools()
	pb.registerPluginTools()
	return pb
}

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
	cdTool := tools.NewWorkingDirectoryTool(ws)
	cdTool.SetExecHost(execTool)
	pb.Tools.Register(cdTool)
	pb.Tools.Register(tools.NewWebFetchTool())
}

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

type PromptPart struct {
	Label    string `json:"label"`
	Injected bool   `json:"injected"`
	Text     string `json:"text"`
}

func (pb *PromptBuilder) BuildSystemPromptParts(workDir, extraContext string) []PromptPart {
	var parts []PromptPart
	add := func(label string, injected bool, text string) {
		parts = append(parts, PromptPart{Label: label, Injected: injected, Text: text})
	}
	if pb.agentConfig.Prompt != "" {
		persona := pb.agentConfig.Prompt
		name := pb.modelName
		if name == "" {
			name = "default"
		}
		persona = strings.ReplaceAll(persona, "{{model}}", name)
		add("人设", false, persona)
	}
	add("工作约束", false, workConstraints)
	if workDir != "" {
		add("工作目录", true, "\n# Working Directory\n\nThe current working directory is: "+workDir+"\nAll file operations must be confined to this directory.\n")
	}
	if ins := agentsInstructionsSection(workDir); ins != "" {
		add("项目指令", true, ins)
	}
	if ps := pluginsSection(); ps != "" {
		add("插件清单", true, ps)
	}
	add("环境信息", true, "\n"+environmentSection()+"\n")
	if extraContext != "" {
		add("附加上下文", true, "\n# Additional Context\n\n"+extraContext+"\n")
	}
	return parts
}

func (pb *PromptBuilder) BuildSystemPrompt(workDir, extraContext string) string {
	parts := pb.BuildSystemPromptParts(workDir, extraContext)
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, "\n")
}

func (pb *PromptBuilder) EnableAgentTools(mgr *SessionManager, sessionID string) {
	dispatcher := mgr.Subagents()
	agentTool := tools.NewAgentTool()
	agentTool.SetSessionContext(sessionID, dispatcher)
	pb.Tools.Register(agentTool)
	pb.Tools.Register(tools.NewSubagentForkTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewSendMessageTool(sessionID, dispatcher))
	pb.Tools.Register(tools.NewListAgentsTool(sessionID, dispatcher))
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
	slog.Debug("agent tools enabled", "session", sessionID)
}

func (pb *PromptBuilder) EnableContextTools(state *tools.ContextState) {
	pb.Tools.Register(tools.NewContextUsageTool(state))
	pb.Tools.Register(tools.NewCompactContextTool(state))
}

func (pb *PromptBuilder) EnableWorkerTools(sessionID string, api tools.WorkerDispatcher) {
	if api == nil {
		return
	}
	pb.Tools.Register(tools.NewWorkersTool(api))
	slog.Debug("WorkersTool enabled", "session", sessionID)
}

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
		if pb.Tools.IsAppended(name) && !explicitlyDenied(cfg, name) && !cfg.DenyPluginTools {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

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

func parseDisabledTools(v string) map[string]bool {
	set := map[string]bool{}
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
}

func dropDisabledTools(defs []provider.ToolSchema) []provider.ToolSchema {
	return filterOutTools(defs, disabledTools())
}

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

func explicitlyDenied(cfg *agent.AgentConfig, name string) bool {
	tp, ok := cfg.Permissions.Tools[name]
	if !ok {
		return false
	}
	return !tp.IsAllowed("")
}
