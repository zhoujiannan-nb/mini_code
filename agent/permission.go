package agent

type PermissionAction int

const (
	Allow PermissionAction = iota
	Deny
)

type ToolPermission struct {
	Action      PermissionAction
	SubCommands map[string]PermissionAction
}

func NewAllow() ToolPermission {
	return ToolPermission{Action: Allow}
}

func NewDeny() ToolPermission {
	return ToolPermission{Action: Deny}
}

func NewCommands(mapping map[string]string) ToolPermission {
	sub := make(map[string]PermissionAction, len(mapping))
	for k, v := range mapping {
		if v == "deny" {
			sub[k] = Deny
		} else {
			sub[k] = Allow
		}
	}
	return ToolPermission{Action: Deny, SubCommands: sub}
}

func (tp ToolPermission) IsAllowed(subCmd string) bool {
	if subCmd == "" {
		return tp.Action == Allow
	}
	if len(tp.SubCommands) > 0 {
		if a, ok := tp.SubCommands[subCmd]; ok {
			return a == Allow
		}
	}
	return tp.Action == Allow
}

type PermissionConfig struct {
	Tools map[string]ToolPermission
}

func NewPermissionConfigFromDict(raw map[string]interface{}) PermissionConfig {
	tools := make(map[string]ToolPermission, len(raw))
	for name, val := range raw {
		switch v := val.(type) {
		case string:
			if v == "deny" {
				tools[name] = NewDeny()
			} else {
				tools[name] = NewAllow()
			}
		case map[string]interface{}:
			m := make(map[string]string, len(v))
			for k, vv := range v {
				if s, ok := vv.(string); ok {
					m[k] = s
				}
			}
			tools[name] = NewCommands(m)
		}
	}
	return PermissionConfig{Tools: tools}
}

func (pc PermissionConfig) IsToolAllowed(toolName, subCmd string) bool {
	tp, ok := pc.Tools[toolName]
	if !ok {
		return false
	}
	return tp.IsAllowed(subCmd)
}

func (pc PermissionConfig) AllowedTools() []string {
	var names []string
	for name, tp := range pc.Tools {
		if tp.Action == Allow || len(tp.SubCommands) > 0 {
			names = append(names, name)
		}
	}
	return names
}
