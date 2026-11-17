package session

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

func gateLoop(t *testing.T, dir string) *AgentLoop {
	t.Helper()
	l := &AgentLoop{}
	l.SetObservation(tools.NewObservation(), dir)
	return l
}

func params(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestWriteGateRefusesUnreadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("important\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)

	for _, tool := range []string{"edit", "write", "edit_file", "write_file", "delete_file"} {
		deny := l.checkWriteBeforeRead(tool, params("file_path", "a.txt"))
		if deny == "" {
			t.Fatalf("%s on an unread file was allowed", tool)
		}
		if !strings.Contains(deny, "has not been read") || !strings.Contains(deny, "call read on") {
			t.Fatalf("%s refusal is not actionable: %s", tool, deny)
		}
		if deny := l.checkWriteBeforeRead(tool, params("path", "a.txt")); deny == "" {
			t.Fatalf("%s on an unread file was allowed via the legacy path key", tool)
		}
	}
}

func TestWriteGateAllowsAfterContentRead(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("important\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)
	l.obs.NoteContentRead(fp)

	if deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt")); deny != "" {
		t.Fatalf("edit after read was refused: %s", deny)
	}
	if deny := l.checkWriteBeforeRead("edit_file", params("path", filepath.Join(dir, "a.txt"))); deny != "" {
		t.Fatalf("absolute path after read was refused: %s", deny)
	}
}

func TestWriteGateRefusesStaleRead(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)
	l.obs.NoteContentRead(fp)

	if err := os.WriteFile(fp, []byte("v2 changed externally\n"), 0644); err != nil {
		t.Fatal(err)
	}
	deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt"))
	if deny == "" {
		t.Fatal("edit after an external change was allowed")
	}
	if !strings.Contains(deny, "changed on disk") {
		t.Fatalf("expected a stale-read refusal, got: %s", deny)
	}
}

func TestWriteGateIgnoresListingOnly(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)
	l.obs.NoteRead(fp)

	deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt"))
	if deny == "" || !strings.Contains(deny, "only been listed") {
		t.Fatalf("listing authorised an overwrite: %q", deny)
	}
}

func TestWriteGateAllowsCreateAndAppend(t *testing.T) {
	dir := t.TempDir()
	l := gateLoop(t, dir)

	if deny := l.checkWriteBeforeRead("write_file", params("path", "new.txt", "content", "hi")); deny != "" {
		t.Fatalf("creating a new file was refused: %s", deny)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if deny := l.checkWriteBeforeRead("write_file", params("path", "old.txt", "content", "y", "append", true)); deny != "" {
		t.Fatalf("append was refused: %s", deny)
	}
	if deny := l.checkWriteBeforeRead("read_file", params("path", "old.txt")); deny != "" {
		t.Fatalf("read was gated: %s", deny)
	}
}

func TestNoteToolOutcomeAuthorisesNextEdit(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)
	if deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt")); deny == "" {
		t.Fatal("precondition: the file should start unread")
	}
	if err := os.WriteFile(fp, []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l.noteToolOutcome("edit_file", params("path", "a.txt"), tools.NewTextResult("Successfully edited "+fp))

	if deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt")); deny != "" {
		t.Fatalf("the edit did not authorise the next one: %s", deny)
	}
}

func TestNoteToolOutcomeIgnoresFailures(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l := gateLoop(t, dir)
	for _, text := range []string{
		"Error: old_text not found in a.txt (tried exact and whitespace-tolerant match).",
		"Warning: old_text appears 2 times at lines 1, 3. Provide more context to disambiguate, or set replace_all=true.",
		"Interrupted",
	} {
		l.noteToolOutcome("edit_file", params("path", "a.txt"), tools.NewTextResult(text))
		if deny := l.checkWriteBeforeRead("edit_file", params("path", "a.txt")); deny == "" {
			t.Fatalf("a failed edit (%q) marked the file as read", text)
		}
	}
}

func TestEditWithoutReadIsRefusedEndToEnd(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"edit","arguments":"{\"file_path\":\"a.txt\",\"old_string\":\"hello\",\"new_string\":\"bye\"}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	edit := tools.NewEditFileTool(dir)
	reg := tools.NewToolRegistry()
	reg.Register(edit)
	loop := NewAgentLoop(client, 4, reg, []provider.ToolSchema{tools.ToSchema(edit)}, nil, nil, nil, nil)
	loop.compactionOn = false
	loop.SetObservation(tools.NewObservation(), dir)

	res, err := loop.Run(context.Background(), "sys", "change it", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	refused := false
	for _, m := range res.Messages {
		if m.Role == "tool" && strings.Contains(m.GetText(), "refusing to modify") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the edit was not refused; tool results: %+v", res.Messages)
	}
	if got, _ := os.ReadFile(fp); string(got) != "hello\n" {
		t.Fatalf("the refused edit still changed the file: %q", string(got))
	}

	if err := os.WriteFile(fp, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	requests = 0
	loop2 := NewAgentLoop(client, 4, reg, []provider.ToolSchema{tools.ToSchema(edit)}, nil, nil, nil, nil)
	loop2.compactionOn = false
	obs := tools.NewObservation()
	obs.NoteContentRead(fp)
	loop2.SetObservation(obs, dir)
	if _, err := loop2.Run(context.Background(), "sys", "change it", nil); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if got, _ := os.ReadFile(fp); string(got) != "bye\n" {
		t.Fatalf("the edit after a read did not apply: %q", string(got))
	}
}
