package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeWorkers struct{ note string }

func (f *fakeWorkers) List() string                                 { return "list" }
func (f *fakeWorkers) WorkerInfo() string                           { return "info" }
func (f *fakeWorkers) Create(name, summary, copyFrom string) string { return "create" }
func (f *fakeWorkers) Join(workerID string) string                  { return "join " + workerID }
func (f *fakeWorkers) Leave() string                                { return "leave" }
func (f *fakeWorkers) Update(summary string) string                 { return "update" }
func (f *fakeWorkers) History(limit int) string                     { return "history" }
func (f *fakeWorkers) Messages(sessionID string, chars int) string  { return "messages" }
func (f *fakeWorkers) NoteRead() string                             { return f.note }

func TestWorkersToolConventionReadOnly(t *testing.T) {
	tool := NewWorkersTool(&fakeWorkers{note: "v1 干活前先跑测试"})
	buf, err := json.Marshal(tool.Parameters())
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	schema := string(buf)
	for _, want := range []string{"note_read", "list", "create", "messages"} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema lost %q:\n%s", want, schema)
		}
	}
	for _, banned := range []string{"note_write", `"content"`} {
		if strings.Contains(schema, banned) {
			t.Fatalf("schema still advertises %q:\n%s", banned, schema)
		}
	}
	if d := tool.Description(); strings.Contains(d, "note_write") {
		t.Fatalf("description still offers note_write:\n%s", d)
	}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "note_write", "content": "新约定"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "未知 action") {
		t.Fatalf("note_write should fall through to the unknown-action hint: %q", res.Text)
	}
	res, err = tool.Execute(context.Background(), map[string]interface{}{"action": "note_read"})
	if err != nil || res.Text != "v1 干活前先跑测试" {
		t.Fatalf("note_read: %q (%v)", res.Text, err)
	}
}
