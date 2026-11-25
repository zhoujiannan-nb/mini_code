package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoWriteResultEchoesFullList(t *testing.T) {
	tt := NewTodoTool()
	res, err := tt.Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{"content": "改 base.css", "status": "completed"},
			map[string]interface{}{"content": "跑测试", "status": "in_progress"},
			map[string]interface{}{"content": "部署", "status": "pending"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"改 base.css", "跑测试", "部署", "1 pending", "1 in progress", "1 completed"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("result should echo %q, got: %q", want, res.Text)
		}
	}
}
