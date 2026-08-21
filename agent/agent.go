package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type AgentConfig struct {
	Name        string
	Description string
	Prompt      string
	Mode        string // "primary" | "subagent"
	Permissions PermissionConfig
}

var basePermissions = map[string]interface{}{
	"read_file":    "allow",
	"list_dir":     "allow",
	"search_files": "allow",
	"exec":         "allow",
	"skills":       "allow",
	"readimg":      "allow",
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
			"write_file":  "allow",
			"edit_file":   "allow",
			"delete_file": "allow",
			"move_file":   "allow",
			"copy_file":   "allow",
			"task":        "allow",
		}),
	}
}

func ExploreAgentConfig() *AgentConfig {
	prompt := LoadPrompt("explore.txt")
	return &AgentConfig{
		Name:        "explore",
		Description: "Fast agent specialized for exploring codebases.",
		Prompt:      prompt,
		Mode:        "subagent",
		Permissions: mergePermissions(map[string]interface{}{
			"write_file": "deny",
			"edit_file":  "deny",
			"exec":       map[string]interface{}{"rm": "deny", "del": "deny"},
			"skills":     "allow",
		}),
	}
}

func GenerateAgentConfig() *AgentConfig {
	prompt := LoadPrompt("generate.txt")
	return &AgentConfig{
		Name:        "generate",
		Description: "API documentation generator sub-agent.",
		Prompt:      prompt,
		Mode:        "subagent",
		Permissions: mergePermissions(map[string]interface{}{
			"write_file": "allow",
			"edit_file":  "allow",
			"exec":       map[string]interface{}{"rm": "deny", "del": "deny"},
			"skills":     "allow",
			"git_clone":  "deny",
			"git_pull":   "deny",
			"git_diff":   "allow",
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
					"write_file": "deny",
					"edit_file":  "deny",
					"exec":       map[string]interface{}{"rm": "deny", "del": "deny"},
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
	"build":    BuildAgentConfig,
	"explore":  ExploreAgentConfig,
	"generate": GenerateAgentConfig,
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


