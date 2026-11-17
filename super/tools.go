package super

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/mini_code/tools"
)

// The lite super surface — exactly one tool:
//
//	super_schedule  定时任务的增删查改与立即触发
//
// The v9 trio (super_workers / super_send / super_ticket) is gone with the
// subsystems it drove; agent.SuperAgentConfig keeps DenyPluginTools so no
// plugin tool slips into the super session either.

type scheduleTool struct{ s *Service }

func newScheduleTool(s *Service) tools.Tool { return &scheduleTool{s} }

func (t *scheduleTool) Name() string { return "super_schedule" }
func (t *scheduleTool) Description() string {
	return `定时任务：到点把一句写好的话原样投给某个会话。这是你唯一能做的事。
action=create：新建。task=到点发的那句话（原话，必填）；every=间隔（30m / 2h / 1d，最短30s）；name 可选短名；session 可选——目标会话 id，留空就发给你自己（人类没指明时发自己最稳妥，别编造会话 id）。
action=list：列出全部任务（id、内容、间隔、开关、上次触发）。
action=toggle：暂停/恢复（id）。
action=run：立即发一次（id）。
action=delete：删除（id）。`
}
func (t *scheduleTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":  map[string]interface{}{"type": "string", "enum": []string{"create", "list", "toggle", "run", "delete"}},
			"task":    map[string]interface{}{"type": "string", "description": "create：到点发的那句话（原话）"},
			"every":   map[string]interface{}{"type": "string", "description": "create：间隔，如 30m/2h/1d（默认 1h）"},
			"name":    map[string]interface{}{"type": "string", "description": "create：短名（可选）"},
			"session": map[string]interface{}{"type": "string", "description": "create：绑定会话 id（可选，默认发自己）"},
			"id":      map[string]interface{}{"type": "string", "description": "toggle/run/delete：任务 id"},
		},
		"required": []string{"action"},
	}
}
func (t *scheduleTool) IsHidden() bool { return false }

func (t *scheduleTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "create": {
		everySec, err := parseEvery(getStr(params, "every"))
		if err != nil {
			return nil, err
		}
		sc, err := t.s.CreateSchedule(getStr(params, "name"), getStr(params, "task"),
			getStr(params, "session"), everySec, "super")
		if err != nil {
			return nil, err
		}
		target := "本会话（你自己）"
		if sc.Session != t.s.SuperSessionID() {
			target = sc.Session
		}
		return tools.NewTextResult(fmt.Sprintf("定时任务 %s 已建立：每 %s 发一句话 → %s。", sc.ID, everyHuman(sc.EverySec), target)), nil
	}
	case "list": {
		scs, err := t.s.store.ListSchedules()
		if err != nil {
			return nil, err
		}
		if len(scs) == 0 {
			return tools.NewTextResult("还没有定时任务。"), nil
		}
		var sb strings.Builder
		for _, sc := range scs {
			state := "开"
			if !sc.Enabled {
				state = "关"
			}
			last := sc.LastRun
			if last == "" {
				last = "从未"
			}
			fmt.Fprintf(&sb, "- %s [%s] 每%s →%s 上次:%s ｜%s\n",
				sc.ID, state, everyHuman(sc.EverySec), truncate(sc.Session, 12), truncate(last, 19), truncate(sc.Text, 60))
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	case "toggle": {
		on, err := t.s.ToggleSchedule(getStr(params, "id"), "super")
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("已%s。", map[bool]string{true: "恢复", false: "暂停"}[on])), nil
	}
	case "run": {
		if err := t.s.RunScheduleNow(getStr(params, "id"), "super"); err != nil {
			return nil, err
		}
		return tools.NewTextResult("已立即投递一次。"), nil
	}
	case "delete": {
		if err := t.s.DeleteSchedule(getStr(params, "id"), "super"); err != nil {
			return nil, err
		}
		return tools.NewTextResult("定时任务已删除。"), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（create|list|toggle|run|delete）")
	}
}

func getStr(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
