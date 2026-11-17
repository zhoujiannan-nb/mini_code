package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type AgentConfig struct {
	Name        string
	Description string
	Prompt      string
	Mode        string // "primary" | "subagent"
	Permissions PermissionConfig
	// DenyPluginTools flips the plugin-tool exemption (session/gate.go): a role
	// with this set is denied every tool absent from its compiled table that
	// belongs to an installed plugin, instead of the default "absent plugin
	// tool is not a deny". The Router persona must not reach sessions_read /
	// web_search: its surface is exactly the super_* trio, so plugins get
	// closed by construction rather than by omission.
	DenyPluginTools bool
}

// basePermissions is the role's compiled-in allow list. GetFilteredDefinitions
// drops any registered tool whose name is neither listed here nor flagged as an
// appended (plugin) tool, so every name the session registers must appear —
// this is the dsh surface (core + root extras) plus the historical mc names,
// which stay allowed so nothing that is still registered somewhere disappears.
var basePermissions = map[string]interface{}{
	// dsh core surface
	"read":              "allow",
	"write":             "allow",
	"edit":              "allow",
	"glob":              "allow",
	"grep":              "allow",
	"pwsh":              "allow",
	"read_image":        "allow",
	"skill":             "allow",
	"job_output":        "allow",
	"job_list":          "allow",
	"job_kill":          "allow",
	"todo_write":        "allow",
	"working_directory": "allow",
	"web_fetch":         "allow",
	// dsh root extras
	"subagent":      "allow",
	"subagent_fork": "allow",
	"send_message":  "allow",
	"list_agents":   "allow",
	"wait_agent":    "allow",
	"create_goal":   "allow",
	"get_goal":      "allow",
	"update_goal":   "allow",
	// workflow stays allowed in the role table (dsh's preset has it) even
	// though MiniCode does not register it — the surface decision lives in
	// session/prompt.go EnableAgentTools; one-line re-registration must not
	// need a permission change here.
	"workflow": "allow",
	// historical mc names (not registered by default anymore, allowed if wired)
	"read_file":       "allow",
	"write_file":      "allow",
	"edit_file":       "allow",
	"delete_file":     "allow",
	"list_dir":        "allow",
	"search_files":    "allow",
	"exec":            "allow",
	"skills":          "allow",
	"readimg":         "allow",
	"agent_output":    "allow",
	"cancel_agent":    "allow",
	"context_usage":   "allow",
	"compact_context": "allow",
	"workers":         "allow", // 工作大类: discover/create/join + the shared 公约 (only registered for root sessions)
}

func mergePermissions(overrides ...map[string]interface{}) PermissionConfig {
	merged := make(map[string]interface{}, len(basePermissions))
	for k, v := range basePermissions {
		merged[k] = v
	}
	for _, ov := range overrides {
		for k, v := range ov {
			merged[k] = v
		}
	}
	return NewPermissionConfigFromDict(merged)
}

func BuildAgentConfig() *AgentConfig {
	prompt := LoadPrompt("build.txt")
	return &AgentConfig{
		Name:        "build",
		Description: "The default agent. Executes tools based on configured permissions.",
		Prompt:      prompt,
		Mode:        "primary",
		Permissions: mergePermissions(map[string]interface{}{
			"write": "allow",
			"edit":  "allow",
			// move_file / copy_file dropped from the default toolset (see
			// session.registerDefaultTools) — no permission entry to grant.
			// Agent orchestration (one level deep: only root sessions get
			// these) is granted by basePermissions; dsh's root surface.
		}),
	}
}

// WorkAgentConfig is the full-permission worker sub-agent. It inherits the
// parent agent's toolset (everything except the agent tools, so sub-agents can
// never dispatch further sub-agents) and, at dispatch time, the parent
// session's permission mode (full / cautious — see session package).
func WorkAgentConfig() *AgentConfig {
	prompt := LoadPrompt("worker.txt")
	return &AgentConfig{
		Name:        "work",
		Description: "Full-permission worker sub-agent. Inherits the parent agent's toolset (minus task) and permission mode; use for delegating substantial work, in the foreground or background.",
		Prompt:      prompt,
		Mode:        "subagent",
		Permissions: mergePermissions(map[string]interface{}{
			"write": "allow",
			"edit":  "allow",
			// move_file / copy_file dropped from the default toolset (see
			// session.registerDefaultTools).
		}),
	}
}

// SuperAgentConfig is the Router persona: it never does the work itself. The
// tool surface is deliberately tiny — read-only inspection (read / grep / glob)
// plus the super_* orchestration tools registered by the super service; the
// doers (write / edit / pwsh / subagents / goals) are explicitly denied so the
// runtime gate blocks them even if a schema ever leaks through registration.
func SuperAgentConfig() *AgentConfig {
	prompt := LoadPrompt("super.txt")
	return &AgentConfig{
		Name:            "super",
		Description:     "Resident router agent: dispatches tasks to super workers it manages, never executes work itself.",
		Prompt:          prompt,
		Mode:            "primary",
		DenyPluginTools: true,
		Permissions: mergePermissions(map[string]interface{}{
			// orchestration surface (registered per-task by the super service)
			"super_workers": "allow",
			"super_send":    "allow",
			"super_ticket":  "allow",
			// everything else — inspection included — is denied
			"read":              "deny",
			"grep":              "deny",
			"glob":              "deny",
			"todo_write":        "deny",
			"write":             "deny",
			"edit":              "deny",
			"pwsh":              "deny",
			"read_image":        "deny",
			"skill":             "deny",
			"web_fetch":         "deny",
			"working_directory": "deny",
			"job_output":        "deny",
			"job_list":          "deny",
			"job_kill":          "deny",
			"subagent":          "deny",
			"subagent_fork":     "deny",
			"send_message":      "deny",
			"list_agents":       "deny",
			"wait_agent":        "deny",
			"create_goal":       "deny",
			"get_goal":          "deny",
			"update_goal":       "deny",
			"workflow":          "deny",
		}),
	}
}

var (
	customAgents     map[string]*AgentConfig
	customAgentsOnce sync.Once
	customAgentsDirs = []string{".apiguide/agents"}
)

func loadCustomAgents() map[string]*AgentConfig {
	agents := make(map[string]*AgentConfig)
	for _, dir := range customAgentsDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			agents[name] = &AgentConfig{
				Name:        name,
				Description: "Custom agent: " + name,
				Prompt:      strings.TrimSpace(string(data)),
				Mode:        "subagent",
				Permissions: mergePermissions(map[string]interface{}{
					"write":      "deny",
					"edit":       "deny",
					"write_file": "deny",
					"edit_file":  "deny",
					"pwsh":       map[string]interface{}{"rm": "deny", "del": "deny"},
					"exec":       map[string]interface{}{"rm": "deny", "del": "deny"},
					"skill":      "allow",
					"skills":     "allow",
				}),
			}
		}
	}
	return agents
}

func getCustomAgents() map[string]*AgentConfig {
	customAgentsOnce.Do(func() {
		customAgents = loadCustomAgents()
	})
	return customAgents
}

var agentFactories = map[string]func() *AgentConfig{
	"build": BuildAgentConfig,
	"work":  WorkAgentConfig,
	"super": SuperAgentConfig,
}

func GetAgentConfig(role string) (*AgentConfig, error) {
	custom := getCustomAgents()
	if cfg, ok := custom[role]; ok {
		return cfg, nil
	}
	factory, ok := agentFactories[role]
	if !ok {
		available := make([]string, 0, len(agentFactories)+len(custom))
		for k := range agentFactories {
			available = append(available, k)
		}
		for k := range custom {
			available = append(available, k)
		}
		sort.Strings(available) // stable order: map iteration is randomized
		return nil, fmt.Errorf("unknown agent role: %s, available: %v", role, available)
	}
	return factory(), nil
}

func ListAllAgents() map[string]string {
	result := make(map[string]string)
	for _, factory := range agentFactories {
		cfg := factory()
		result[cfg.Name] = cfg.Description
	}
	for name, cfg := range getCustomAgents() {
		result[name] = cfg.Description
	}
	return result
}
