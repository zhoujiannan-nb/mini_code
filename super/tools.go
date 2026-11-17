package super

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/mini_code/tools"
)

// The v9 Router surface — exactly three tools, matching the gate table in
// agent.SuperAgentConfig (which also flips DenyPluginTools, so no plugin tool
// slips through the "absent = plugin" exemption any more):
//
//	super_workers  查看 worker 与 thread（只读）
//	super_send     转发原话到已有 thread 会话
//	super_ticket   创建工单 / 查看工单 / 记录完结
//
// Everything else the old Router had (queue, schedule, audit query, session
// spawn) is gone: dispatch is the decision table's job, history is the
// panel's job, and the Router session is pure audit.

// --- super_workers ---

type workersTool struct{ s *Service }

func newWorkersTool(s *Service) tools.Tool { return &workersTool{s} }

func (t *workersTool) Name() string { return "super_workers" }
func (t *workersTool) Description() string {
	return `查看你的执行体花名册（只读）。工人由系统按采集源的落地方式自动建立（首绑建 src-<源名> 工人 + thread 会话），人类在面板配边界、退役工人——你不建、不删、不改工人。
action=list：列出 worker、其会话（含运行态）与工单并发占用（running/3）。`
}
func (t *workersTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{"type": "string", "enum": []string{"list"}},
		},
		"required": []string{"action"},
	}
}
func (t *workersTool) IsHidden() bool { return false }

func (t *workersTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	if strings.TrimSpace(getStr(params, "action")) != "list" {
		return nil, fmt.Errorf("super_workers 只有 list（建/删工人已收归系统与人类面板）")
	}
	ws, err := t.s.store.ListWorkers()
	if err != nil {
		return nil, err
	}
	running, _ := t.s.store.TicketCountState("running")
	queued, _ := t.s.store.TicketCountState("queued")
	var sb strings.Builder
	fmt.Fprintf(&sb, "工单并发 %d/%d（排队 %d）· worker 名额 %d/%d\n",
		running, maxRunningTickets, queued, func() int { n, _ := t.s.store.CountLiveWorkers(); return n }(), t.s.Config().WorkerCap)
	for _, w := range ws {
		ss, _ := t.s.store.ListBoundSessions(w.ID)
		nRun := 0
		for _, x := range ss {
			if t.s.hub.ActiveTask(x.ID) {
				nRun++
			}
		}
		fmt.Fprintf(&sb, "- %s %s [%s] 会话%d(运行%d) dir=%s\n", w.ID, w.Name, w.Status, len(ss), nRun, w.WorkDir)
	}
	return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// --- super_send ---

type sendTool struct{ s *Service }

func newSendTool(s *Service) tools.Tool { return &sendTool{s} }

func (t *sendTool) Name() string { return "super_send" }
func (t *sendTool) Description() string {
	return `转发人类的【原话】到一个已有 thread 会话（worker 会话）。这是你唯一的话语出口：
先照抄原话放 message 里，需要补充就在末尾另起一行加「（转述·≤30字）」——除此之外一个字都不要多说。
会话忙会在下一轮注入，不阻塞你。想知道会话内容别调任何读取工具（你没有），thread 的回复会经审计回到你能看见的地方。`
}
func (t *sendTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"session_id": map[string]interface{}{"type": "string", "description": "目标 thread 会话 id"},
			"message":    map[string]interface{}{"type": "string", "description": "原话（一字不改）+ 可选 ≤30字转述"},
		},
		"required": []string{"session_id", "message"},
	}
}
func (t *sendTool) IsHidden() bool { return false }

func (t *sendTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	sid := getStr(params, "session_id")
	msg := strings.TrimSpace(getStr(params, "message"))
	if msg == "" {
		return nil, fmt.Errorf("message 不能为空")
	}
	if err := t.s.SendToSession(sid, "【Router转发】\n"+msg); err != nil {
		return nil, err
	}
	return tools.NewTextResult("已转发到 " + sid + "（忙则下轮注入）。"), nil
}

// --- super_ticket ---

type ticketTool struct{ s *Service }

func newTicketTool(s *Service) tools.Tool { return &ticketTool{s} }

func (t *ticketTool) Name() string { return "super_ticket" }
func (t *ticketTool) Description() string {
	return `工单中心（你只开单、看单、记完结）。
action=create：人类交办一件事时开人工工单——text 填人类原话（一字不改），note 填你的转述（≤30字），title 可省（自动取原话）；session 填目标 thread 会话（人类指明了才填），src 填采集源 id（人类的话是对某个源说的才填）。落地点都没有也能开，会排队等人类在面板绑定。并发满自动排队，不要你等。
action=list：查看工单（可按 state=queued|running|ready|closed 过滤）。
action=close：关闭工单记完结（id），关单是你的正当权限：人类说验收/关掉，或 ready 工单的事项已在 thread 里办结出结论，你都可以直接关；拿不准就不关，留给人工。`
}
func (t *ticketTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "enum": []string{"create", "list", "close"}},
			"text":       map[string]interface{}{"type": "string", "description": "create：人类原话，一字不改"},
			"note":       map[string]interface{}{"type": "string", "description": "create：≤30字转述"},
			"title":      map[string]interface{}{"type": "string", "description": "create：可选标题"},
			"session":    map[string]interface{}{"type": "string", "description": "create：目标 thread 会话 id（可选）"},
			"src":        map[string]interface{}{"type": "string", "description": "create：关联采集源 id（可选）"},
			"state":      map[string]interface{}{"type": "string", "description": "list：状态过滤（可选）"},
			"id":         map[string]interface{}{"type": "string", "description": "close：工单 id"},
		},
		"required": []string{"action"},
	}
}
func (t *ticketTool) IsHidden() bool { return false }

func (t *ticketTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	switch strings.TrimSpace(getStr(params, "action")) {
	case "create": {
		tk, err := t.s.CreateTicket(getStr(params, "src"), getStr(params, "text"), getStr(params, "note"),
			getStr(params, "title"), getStr(params, "session"), "super")
		if err != nil {
			return nil, err
		}
		if tk.State == "queued" {
			return tools.NewTextResult(fmt.Sprintf("工单 %s 已开，并发满(%d/%d)在排队。", tk.ID, maxRunningTickets, maxRunningTickets)), nil
		}
		return tools.NewTextResult(fmt.Sprintf("工单 %s 已开并派发（原话+备注已进会话）。", tk.ID)), nil
	}
	case "list": {
		ts, err := t.s.store.ListTickets(50)
		if err != nil {
			return nil, err
		}
		filter := getStr(params, "state")
		var sb strings.Builder
		for _, tk := range ts {
			if filter != "" && tk.State != filter {
				continue
			}
			srcTag := ""
			if tk.SrcID != "" {
				srcTag = " @" + tk.SrcID
			}
			fmt.Fprintf(&sb, "- %s [%s]%s %s → %s\n", tk.ID, tk.State, srcTag, tk.Title, orNone2(tk.Session, "待落地"))
		}
		if sb.Len() == 0 {
			return tools.NewTextResult("没有匹配的工单。"), nil
		}
		return tools.NewTextResult(strings.TrimRight(sb.String(), "\n")), nil
	}
	case "close": {
		if err := t.s.CloseTicket(getStr(params, "id"), "super"); err != nil {
			return nil, err
		}
		return tools.NewTextResult("工单已记完结（closed）。"), nil
	}
	default:
		return nil, fmt.Errorf("未知 action（create|list|close）")
	}
}

func getStr(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
