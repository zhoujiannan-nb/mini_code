package super

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/mini_code/tools"
)

// The super_* orchestration surface. These tools are registered ONLY for
// agent_role=="super" sessions (see session.preparePromptTools) and listed
// in the super role's permission table, so the runtime gate carries the
// same answer twice.

// --- super_workers ---

type workersTool struct{ s *Service }

func newWorkersTool(s *Service) tools.Tool { return &workersTool{s} }

func (t *workersTool) Name() string { return "super_workers" }
func (t *workersTool) Description() string {
	return `管理你自己的执行体（super worker）。工人只能由你创建/退役（人类只配置上限等边界）。
action=list：查看现有 worker 与名额；
action=create：新建 worker（name 必填，英文短名；role_note 为注入其所有会话的任务公约，可选；model 可选，默认用配置模型）。名额满会报错——此时用 super_queue 排队；
action=update：更新 role_note / model（role_note 改动版本 +1，下次下发自动带新版公约）；
action=delete：退役 worker（任务完结且长期空闲时应退役回收名额；其会话与历史保留可审计）。`
}
func (t *workersTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":    map[string]interface{}{"type": "string", "enum": []string{"list", "create", "update", "delete"}},
			"name":      map[string]interface{}{"type": "string", "description": "create 必填：worker 英文短名"},
			"role_note": map[string]interface{}{"type": "string", "description": "任务公约：注入该 worker 每个会话的约定/边界"},
			"model":     map[string]interface{}{"type": "string", "description": "worker 模型覆盖，空=配置默认"},
			"id":        map[string]interface{}{"type": "string", "description": "update/delete 必填：sw-xxxx"},
		},
		"required": []string{"action"},
	}
}
func (t *workersTool) IsHidden() bool { return false }

func (t *workersTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	action := strings.TrimSpace(getStr(params, "action"))
	cfg := t.s.Config()
	switch action {
	case "list": {
		ws, err := t.s.store.ListWorkers()
		if err != nil {
			return nil, err
		}
		live, _ := t.s.store.CountLiveWorkers()
		var sb strings.Builder
		fmt.Fprintf(&sb, "worker 名额 %d/%d（活跃）\n", live, cfg.WorkerCap)
		for _, w := range ws {
			ss, _ := t.s.store.ListBoundSessions(w.ID)
			nRun := 0
			for _, x := range ss {
				if t.s.hub.ActiveTask(x.ID) {
					nRun++
				}
			}
			fmt.Fprintf(&sb, "- %s %s [%s] 会话%d(运行%d) note_ver=%d dir=%s\n", w.ID, w.Name, w.Status, len(ss), nRun, w.NoteVer, w.WorkDir)
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	case "create": {
		w, err := t.s.CreateWorker(getStr(params, "name"), getStr(params, "role_note"), getStr(params, "model"))
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("已创建 worker %s「%s」，工作区 %s。下一步用 super_sessions spawn 下发任务。", w.ID, w.Name, w.WorkDir)), nil
	}
	case "update": {
		w, err := t.s.UpdateWorkerConfig(getStr(params, "id"), getStr(params, "role_note"), getStr(params, "model"), "super")
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("worker %s 已更新（note_ver=%d, model=%s）。", w.ID, w.NoteVer, w.Model)), nil
	}
	case "delete": {
		if err := t.s.DeleteWorker(getStr(params, "id"), "super"); err != nil {
			return nil, err
		}
		return tools.NewTextResult("worker 已退役，会话归档保留；如有排队任务且名额已空出会自动捞起。"), nil
	}
	default:
		return nil, fmt.Errorf("未知 action: %s（list|create|update|delete）", action)
	}
}

// --- super_sessions ---

type sessionsTool struct{ s *Service }

func newSessionsTool(s *Service) tools.Tool { return &sessionsTool{s} }

func (t *sessionsTool) Name() string { return "super_sessions" }
func (t *sessionsTool) Description() string {
	return `在 worker 名下下发与跟踪任务会话。
action=spawn：把任务下发给 worker（sw_id + task 必填，title 可选）——任务跑完后结果会自动走【super通道·立即】回来，不要原地等待；
action=send：向已绑定的会话追加指令（session_id + message 必填；会话忙则下轮注入）；
action=list：列出全部绑定会话（可选 sw_id 过滤）；
action=close：收尾一个会话（取消在跑任务并归档绑定）。`
}
func (t *sessionsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "enum": []string{"spawn", "send", "list", "close"}},
			"sw_id":      map[string]interface{}{"type": "string", "description": "spawn/list：worker id"},
			"task":       map[string]interface{}{"type": "string", "description": "spawn：任务完整描述"},
			"title":      map[string]interface{}{"type": "string", "description": "可选会话标题"},
			"session_id": map[string]interface{}{"type": "string", "description": "send/close：绑定会话 id"},
			"message":    map[string]interface{}{"type": "string", "description": "send：追加指令"},
		},
		"required": []string{"action"},
	}
}
func (t *sessionsTool) IsHidden() bool { return false }

func (t *sessionsTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "spawn": {
		sid, err := t.s.SpawnSession(getStr(params, "sw_id"), getStr(params, "task"), getStr(params, "title"))
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("已下发：会话 %s。任务完成后结果会自动经【super通道·立即】汇报回来。", sid)), nil
	}
	case "send": {
		if err := t.s.SendToSession(getStr(params, "session_id"), getStr(params, "message")); err != nil {
			return nil, err
		}
		return tools.NewTextResult("已投递（若会话正在执行，会在下一轮注入）。"), nil
	}
	case "list": {
		filter := getStr(params, "sw_id")
		ws, err := t.s.store.ListWorkers()
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		for _, w := range ws {
			if filter != "" && w.ID != filter {
				continue
			}
			ss, _ := t.s.store.ListBoundSessions(w.ID)
			for _, x := range ss {
				run := "空闲"
				if t.s.hub.ActiveTask(x.ID) {
					run = "运行中"
				}
				fmt.Fprintf(&sb, "- %s @%s(%s) [%s·%s] %s\n", x.ID, w.ID, w.Name, x.Status, run, x.Title)
			}
		}
		if sb.Len() == 0 {
			return tools.NewTextResult("暂无绑定会话。"), nil
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	case "close": {
		if err := t.s.CloseSession(getStr(params, "session_id")); err != nil {
			return nil, err
		}
		return tools.NewTextResult("会话已关闭归档。"), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（spawn|send|list|close）")
	}
}

// --- super_queue ---

type queueTool struct{ s *Service }

func newQueueTool(s *Service) tools.Tool { return &queueTool{s} }

func (t *queueTool) Name() string { return "super_queue" }
func (t *queueTool) Description() string {
	return `worker 名额满时的任务排队区（任务永不丢弃）。
action=add：把新任务入队（task 必填；worker_name 可选，捞起时按此名建 worker）；
action=list：查看排队中的任务。名额空出（你退役 worker、或人类调大 worker_cap）会自动按序捞起并下发。`
}
func (t *queueTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":      map[string]interface{}{"type": "string", "enum": []string{"add", "list"}},
			"task":        map[string]interface{}{"type": "string"},
			"worker_name": map[string]interface{}{"type": "string"},
		},
		"required": []string{"action"},
	}
}
func (t *queueTool) IsHidden() bool { return false }

func (t *queueTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "add": {
		q, err := t.s.QueueTask(getStr(params, "task"), getStr(params, "worker_name"))
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("任务已入队 %s；名额空出会自动捞起。", q.ID)), nil
	}
	case "list": {
		qs, err := t.s.store.QueuePending()
		if err != nil {
			return nil, err
		}
		if len(qs) == 0 {
			return tools.NewTextResult("排队区是空的。"), nil
		}
		var sb strings.Builder
		for _, q := range qs {
			fmt.Fprintf(&sb, "- %s [%s] %s\n", q.ID, q.CreatedAt, q.Task)
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（add|list）")
	}
}

// --- super_schedule ---

type scheduleTool struct{ s *Service }

func newScheduleTool(s *Service) tools.Tool { return &scheduleTool{s} }

func (t *scheduleTool) Name() string { return "super_schedule" }
func (t *scheduleTool) Description() string {
	return `定时任务——你自己不写脚本、不写代码：任务就是一句话，到点由系统投递回你的消息通道，你收到后再分派给 worker 干活。
action=create：登记一个周期任务（name 英文短名、task 到点发给未来的你的一句话、every 周期如 "3h"/"90m"/"2h30m"/"1d"、mode 可选 imm|buf，默认 buf 走缓冲批次——除非人类明说"立即"）。例：人类说"每 3 小时拉取 xx 检查更新并修复发布"，你就 create 一个 every=3h、task 原样转述这件工作的任务；到点消息进通道，你再 spawn 给合适 worker；
action=list：查看现有定时任务；
action=toggle：启停一个计划（file）；
action=run：立即触发一次（file，任务型=立刻投一条消息）；
action=delete：删除定时任务（file）。`
}
func (t *scheduleTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{"type": "string", "enum": []string{"create", "list", "toggle", "run", "delete"}},
			"name":   map[string]interface{}{"type": "string", "description": "create：英文短名，如 repo-pull-check"},
			"task":   map[string]interface{}{"type": "string", "description": "create：到点投递的一句话任务描述"},
			"every":  map[string]interface{}{"type": "string", "description": `create：周期，如 "3h"、"90m"、"2h30m"、"1d"（默认 1h，最短 60s）`},
			"mode":   map[string]interface{}{"type": "string", "enum": []string{"buf", "imm"}, "description": "create：默认 buf（缓冲批次），人类明确要求才 imm"},
			"file":   map[string]interface{}{"type": "string", "description": "toggle/run/delete：计划 file 标识"},
		},
		"required": []string{"action"},
	}
}
func (t *scheduleTool) IsHidden() bool { return false }

func (t *scheduleTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "create": {
		sc, err := t.s.CreateTaskSchedule(getStr(params, "name"), getStr(params, "task"), getStr(params, "every"), getStr(params, "mode"))
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("定时任务已登记：%s（每 %ds · %s 通道）。到点后任务文本会出现在你的消息通道，届时按纪律分派 worker。", sc.File, sc.EverySec, sc.Mode)), nil
	}
	case "list": {
		scheds, err := t.s.store.ListSchedules()
		if err != nil {
			return nil, err
		}
		if len(scheds) == 0 {
			return tools.NewTextResult("还没有任何定时任务/脚本计划。"), nil
		}
		var sb strings.Builder
		for _, sc := range scheds {
			kind := "脚本"
			body := ""
			if sc.Kind == "task" {
				kind = "任务"
				body = " ｜ " + truncate(sc.Note, 80)
			}
			on := "停"
			if sc.Enabled {
				on = "启"
			}
			fmt.Fprintf(&sb, "- %s [%s·%s] 每%ds %s通道 last=%s exit=%d%s\n", sc.File, kind, on, sc.EverySec, sc.Mode, sc.LastRun, sc.LastExit, body)
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	case "toggle": {
		on, err := t.s.ToggleSchedule(getStr(params, "file"), "super")
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(fmt.Sprintf("已%s。", map[bool]string{true: "启用", false: "停用"}[on])), nil
	}
	case "run": {
		t.s.RunScheduleNow(getStr(params, "file"), "super")
		return tools.NewTextResult("已触发一次。"), nil
	}
	case "delete": {
		if err := t.s.DeleteTaskSchedule(getStr(params, "file")); err != nil {
			return nil, err
		}
		return tools.NewTextResult("任务已删除。"), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（create|list|toggle|run|delete）")
	}
}

// --- super_audit ---

type auditTool struct{ s *Service }

func newAuditTool(s *Service) tools.Tool { return &auditTool{s} }

func (t *auditTool) Name() string { return "super_audit" }
func (t *auditTool) Description() string {
	return `审计轨迹（全量自动记录之外的人工补充与追溯）。
action=note：给关键决策补一条备注（target + detail，refs 可填关联 id）；
action=trace：按引用 id 链式追溯（refs 填如 sw-xxxx / 会话 id / b-xxxx）；
action=search：关键词检索审计行。`
}
func (t *auditTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{"type": "string", "enum": []string{"note", "trace", "search"}},
			"target": map[string]interface{}{"type": "string"},
			"detail": map[string]interface{}{"type": "string"},
			"refs":   map[string]interface{}{"type": "string"},
			"query":  map[string]interface{}{"type": "string"},
		},
		"required": []string{"action"},
	}
}
func (t *auditTool) IsHidden() bool { return false }

func (t *auditTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "note": {
		target := getStr(params, "target")
		detail := getStr(params, "detail")
		if target == "" || detail == "" {
			return nil, fmt.Errorf("note 需要 target 和 detail")
		}
		if err := t.s.store.AuditInsert("super", "super.note", target, truncate(detail, 500), getStr(params, "refs")); err != nil {
			return nil, err
		}
		return tools.NewTextResult("备注已入审计。"), nil
	}
	case "trace": {
		refs := getStr(params, "refs")
		if refs == "" {
			return nil, fmt.Errorf("trace 需要 refs")
		}
		rows, err := t.s.store.AuditRecent(30, refs, "")
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(formatAuditRows(rows)), nil
	}
	case "search": {
		q := getStr(params, "query")
		if q == "" {
			return nil, fmt.Errorf("search 需要 query")
		}
		rows, err := t.s.store.AuditSearch(q, 30)
		if err != nil {
			return nil, err
		}
		return tools.NewTextResult(formatAuditRows(rows)), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（note|trace|search）")
	}
}

func formatAuditRows(rows []*AuditRow) string {
	if len(rows) == 0 {
		return "没有匹配的审计记录。"
	}
	var sb strings.Builder
	for _, a := range rows {
		fmt.Fprintf(&sb, "%s [%s] %s %s — %s", a.Ts, a.Actor, a.Action, a.Target, a.Detail)
		if a.Refs != "" {
			fmt.Fprintf(&sb, " (%s)", a.Refs)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func getStr(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
