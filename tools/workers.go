package tools

import (
	"context"
	"fmt"
)

type WorkerDispatcher interface {
	WorkerInfo() string
	List() string
	Create(name, summary, copyFrom string) string
	Join(workerID string) string
	Leave() string
	Update(summary string) string
	History(limit int) string
	Messages(sessionID string, chars int) string
	NoteRead() string
}

type WorkersTool struct {
	api WorkerDispatcher
}

func NewWorkersTool(api WorkerDispatcher) *WorkersTool { return &WorkersTool{api: api} }
func (t *WorkersTool) SetContext(api WorkerDispatcher) { t.api = api }

func (t *WorkersTool) Name() string { return "workers" }

func (t *WorkersTool) Description() string {
	return "Worker(工作大类)管理:一个 worker = 共享工作目录 + 带版本的共享公约,同 worker 的会话共用目录并自动收到公约;" +
		"action 与参数的语义见各字段描述,公约只能由用户在界面里改写。"
}

func (t *WorkersTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"list", "create", "join", "leave", "info", "update", "history", "messages", "note_read"},
				"description": "list=列出 worker;create=新建并加入;join=加入;leave=退出;info=本会话所属 worker;" +
					"update=改写摘要;history=兄弟会话列表;messages=读兄弟会话历史;note_read=读公约(公约只能人工改写)",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "仅 create:worker 名称(简短,会用它命名工作目录)",
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "create/update:这个 worker 在做什么(一两句,别的会话靠它决定是否加入)",
			},
			"worker_id": map[string]interface{}{
				"type":        "string",
				"description": "仅 join:目标 worker 的 id(取自 list)",
			},
			"copy_from": map[string]interface{}{
				"type":        "string",
				"description": "仅 create(可选):把已有目录或文件复制进新工作目录作为起点(绝对路径)",
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "仅 messages:要读取的兄弟会话 id(取自 history)",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "history 用:最多列多少个会话,默认 20,最多 100",
			},
			"chars": map[string]interface{}{
				"type":        "integer",
				"description": "messages 用:最多返回多少字符,默认 6000,最多 24000",
			},
		},
		"required": []string{"action"},
	}
}

func (t *WorkersTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	if t.api == nil {
		return NewTextResult("workers 工具当前不可用(该会话没有接入 worker 服务)。"), nil
	}
	action, _ := params["action"].(string)
	switch action {
	case "list":
		return NewTextResult(t.api.List()), nil
	case "create":
		name, _ := params["name"].(string)
		summary, _ := params["summary"].(string)
		copyFrom, _ := params["copy_from"].(string)
		if name == "" {
			return NewTextResult("create 需要 name:这个 worker 叫什么(简短)。"), nil
		}
		return NewTextResult(t.api.Create(name, summary, copyFrom)), nil
	case "join":
		id, _ := params["worker_id"].(string)
		if id == "" {
			return NewTextResult("join 需要 worker_id:先用 action=list 看有哪些 worker。"), nil
		}
		return NewTextResult(t.api.Join(id)), nil
	case "leave":
		return NewTextResult(t.api.Leave()), nil
	case "info":
		return NewTextResult(t.api.WorkerInfo()), nil
	case "update":
		summary, _ := params["summary"].(string)
		if summary == "" {
			return NewTextResult("update 需要 summary:这个 worker 在做什么(一两句)。"), nil
		}
		return NewTextResult(t.api.Update(summary)), nil
	case "history":
		limit := intParam(params, "limit", 20)
		if limit > 100 {
			limit = 100
		}
		return NewTextResult(t.api.History(limit)), nil
	case "messages":
		sid, _ := params["session_id"].(string)
		if sid == "" {
			return NewTextResult("messages 需要 session_id:先用 action=history 找同 worker 的会话。"), nil
		}
		chars := intParam(params, "chars", 6000)
		if chars > 24000 {
			chars = 24000
		}
		return NewTextResult(t.api.Messages(sid, chars)), nil
	case "note_read":
		return NewTextResult(t.api.NoteRead()), nil
	default:
		return NewTextResult("未知 action " + fmt.Sprint(action) + "，请用 list / create / join / leave / info / update / history / messages / note_read。"), nil
	}
}

func (t *WorkersTool) IsHidden() bool { return false }
