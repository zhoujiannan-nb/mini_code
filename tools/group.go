package tools

import (
	"context"
	"fmt"
)

// GroupChatDispatcher is the backend of the group_chat tool. It is
// implemented by session.GroupToolAPI (a group-chat service bound to one
// member session). All methods return plain text shown to the model.
type GroupChatDispatcher interface {
	GroupTitle() string
	OwnName() string
	Info() string
	Members() string
	History(limit int) string
	Send(target, content string) string
}

// GroupChatTool lets a group-member agent read the group channel (info /
// members / history) and send messages to a member or the whole group.
type GroupChatTool struct {
	api GroupChatDispatcher
}

func NewGroupChatTool() *GroupChatTool                      { return &GroupChatTool{} }
func (t *GroupChatTool) SetContext(api GroupChatDispatcher) { t.api = api }

func (t *GroupChatTool) Name() string { return "group_chat" }

func (t *GroupChatTool) Description() string {
	group := "(未知)"
	own := "(未知)"
	if t.api != nil {
		group = t.api.GroupTitle()
		own = t.api.OwnName()
	}
	return fmt.Sprintf(
		"你所在的群聊频道:群名 %q,你的群内名称 %q。"+
			"action=info 查看群信息(群背景+成员);"+
			"members 查看成员群内名称及运行状态;"+
			"history 查看最近群聊记录(默认 20 条,最多 100);"+
			"send 发消息,target 传成员群内名称或 'group'。"+
			"target='group' 的消息只在群聊中展示(用户可见),不会通知其他成员;"+
			"要让指定成员收到并处理,用 target=该成员群内名称(或在正文中写 @该成员名,可多个);"+
			"@用户(群外)仅展示,不触发任何人。",
		group, own)
}

func (t *GroupChatTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"info", "members", "history", "send"},
				"description": "info=群信息;members=成员列表;history=群聊历史;send=发消息",
			},
			"target": map[string]interface{}{
				"type":        "string",
				"description": "仅 send 使用:接收方的群内名称,或 'group' 发给全群(send 时必填)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "仅 send 使用:消息正文",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "仅 history 使用:取最近多少条,默认 20,最多 100",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GroupChatTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	if t.api == nil {
		return NewTextResult("group_chat 工具不可用:当前会话不在任何群聊中。"), nil
	}
	action, _ := params["action"].(string)
	switch action {
	case "info":
		return NewTextResult(t.api.Info()), nil
	case "members":
		return NewTextResult(t.api.Members()), nil
	case "history":
		limit := 20
		if v, ok := params["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		if limit > 100 {
			limit = 100
		}
		return NewTextResult(t.api.History(limit)), nil
	case "send":
		content, _ := params["content"].(string)
		target, _ := params["target"].(string)
		if content == "" {
			return NewTextResult("content 不能为空。"), nil
		}
		if target == "" {
			return NewTextResult("target 不能为空:传接收方成员的群内名称,或 'group' 发给全群。"), nil
		}
		return NewTextResult(t.api.Send(target, content)), nil
	default:
		return NewTextResult("未知 action " + action + ",请使用 info / members / history / send。"), nil
	}
}

func (t *GroupChatTool) IsHidden() bool { return false }
