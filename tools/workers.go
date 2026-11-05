package tools

import (
	"context"
	"fmt"
)

// WorkerDispatcher is the backend of the workers tool. It is implemented by
// session.WorkerToolAPI — the worker service bound to one session, so every
// action that needs "my worker" resolves from the calling session. All methods
// return plain text shown to the model (the same convention as
// GroupChatDispatcher: no structured payloads, no error plumbing).
type WorkerDispatcher interface {
	// WorkerInfo renders the calling session's own worker (or the fact that it
	// has none yet).
	WorkerInfo() string
	// List renders every joinable worker with its summary, workspace and size.
	List() string
	// Create makes a new worker under the session's own directory and joins it.
	Create(name, summary, copyFrom string) string
	// Join attaches the calling session to an existing worker.
	Join(workerID string) string
	// Leave detaches the calling session from its worker.
	Leave() string
	// Update rewrites the worker's summary (its "what this is for" blurb).
	Update(summary string) string
	// History lists the worker's sibling sessions (title, status, size).
	History(limit int) string
	// Messages dumps one sibling session's conversation (truncated).
	Messages(sessionID string, chars int) string
	// NoteRead returns the worker's current convention (with its version).
	NoteRead() string
	// NoteWrite rewrites the convention and bumps its version (runtime-gated:
	// always needs an explicit user approval, see session/gate.go).
	NoteWrite(content string) string
}

// WorkersTool lets a session discover, create and join workers (工作大类), and
// read or rewrite its worker's convention file. Registered for root sessions
// only, attached or not (the schema never differs between the two states, which
// is what keeps the tool list — and therefore the request prefix — stable while
// a session is deciding, joining and working).
type WorkersTool struct {
	api WorkerDispatcher
}

func NewWorkersTool(api WorkerDispatcher) *WorkersTool { return &WorkersTool{api: api} }
func (t *WorkersTool) SetContext(api WorkerDispatcher)  { t.api = api }

func (t *WorkersTool) Name() string { return "workers" }

// Description is deliberately static: no live worker names or directories in
// it, so the tool schema is byte-identical for every session (a schema that
// varies per session would make every session pay for its own cold prefix).
func (t *WorkersTool) Description() string {
	return "工作大类(Worker)管理。Worker = 一条工作线:一个共享工作目录 + 一份共享公约(存在 worker 记录里,带版本)。" +
		"同一个 worker 下的会话共用该目录,并自动收到那份公约(所以公约必须写得简短、可执行)。\n" +
		"list=列出所有 worker(名称/摘要/工作目录/会话数),用于决定加入哪个;\n" +
		"create=新建 worker(name 必填,summary 建议填,会在当前目录下建立 worker-<名字> 工作目录并自动加入;copy_from 可把已有目录/文件复制进去作为起点);\n" +
		"join=把本会话加入指定 worker(worker_id 必填);leave=退出当前 worker;\n" +
		"info=看本会话当前所属 worker 与其工作目录;\n" +
		"update=改写本 worker 的摘要(summary),摘要会影响别的会话怎么选 worker;\n" +
		"history=列出同 worker 的兄弟会话(可跨会话了解进度);messages=读取某个兄弟会话的历史(跨会话取上下文);\n" +
		"note_read=读本 worker 的最新公约;note_write=改写公约并升版本,同 worker 的其他会话下一轮自动收到更新——不得擅自写入,必须先得到用户明确同意。\n" +
		"新会话若尚未归属任何 worker:先 list 再决定 join / create / 不加入(一次性小问题不必建 worker)。"
}

func (t *WorkersTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"list", "create", "join", "leave", "info", "update", "history", "messages", "note_read", "note_write"},
				"description": "list=列出 worker;create=新建并加入;join=加入;leave=退出;info=本会话所属 worker;" +
					"update=改写摘要;history=兄弟会话列表;messages=读兄弟会话历史;note_read=读公约;note_write=改写公约(需用户同意)",
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
			"content": map[string]interface{}{
				"type":        "string",
				"description": "仅 note_write:公约的新全文(整篇覆盖,保持简短,最多 6000 字)",
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
	case "note_write":
		content, _ := params["content"].(string)
		if content == "" {
			return NewTextResult("note_write 需要 content:公约的新全文(整篇覆盖)。想只读一遍,用 note_read。"), nil
		}
		return NewTextResult(t.api.NoteWrite(content)), nil
	default:
		return NewTextResult("未知 action " + fmt.Sprint(action) + "，请用 list / create / join / leave / info / update / history / messages / note_read / note_write。"), nil
	}
}

func (t *WorkersTool) IsHidden() bool { return false }
