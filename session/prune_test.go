package session

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// ---- pruneToolText ----

func TestPruneToolTextShortUntouched(t *testing.T) {
	if _, ok := pruneToolText(strings.Repeat("x", defaultPrune.Threshold), ""); ok {
		t.Fatal("at-threshold text must not be pruned")
	}
}

func TestPruneToolTextHeadMarkerTail(t *testing.T) {
	full := strings.Repeat("A", defaultPrune.Head) +
		strings.Repeat("M", 5000) +
		strings.Repeat("B", defaultPrune.Tail)

	pruned, ok := pruneToolText(full, "")
	if !ok {
		t.Fatal("expected pruning")
	}
	if !strings.HasPrefix(pruned, strings.Repeat("A", defaultPrune.Head)) {
		t.Fatal("head lost")
	}
	if !strings.HasSuffix(pruned, strings.Repeat("B", defaultPrune.Tail)) {
		t.Fatal("tail lost")
	}
	if !strings.Contains(pruned, "middle pruned: 5000 chars omitted") {
		t.Fatalf("marker wrong: %q", pruned)
	}
	if n := len([]rune(pruned)); n >= len([]rune(full)) {
		t.Fatalf("prune did not shrink: %d >= %d", n, len([]rune(full)))
	}
	// Idempotent: the pruned copy is now under threshold.
	if _, ok := pruneToolText(pruned, ""); ok {
		t.Fatal("pruned copy must not re-prune")
	}
}

func TestPruneToolTextRunesSafe(t *testing.T) {
	full := strings.Repeat("中", defaultPrune.Threshold+4000) // every rune 3 bytes
	pruned, ok := pruneToolText(full, "")
	if !ok || !utf8.ValidString(pruned) || strings.Contains(pruned, "\uFFFD") {
		t.Fatalf("rune-boundary safety broken: %q", pruned)
	}
	// Head+Tail leaves pruneMarkerRoom slack, so the omitted count is
	// overage + room — derive it from the config instead of hardcoding
	// (env-tunable-safe, same formula pruneToolText uses).
	omitted := utf8.RuneCountInString(full) - defaultPrune.Head - defaultPrune.Tail
	want := fmt.Sprintf("middle pruned: %d chars omitted", omitted)
	if !strings.Contains(pruned, want) {
		t.Fatalf("omitted count must count runes (%q): %q", want, pruned)
	}
}

func TestPruneToolTextSpillPathInMarker(t *testing.T) {
	pruned, ok := pruneToolText(strings.Repeat("x", defaultPrune.Threshold+1), `C:\spill\s1-c1.txt`)
	if !ok || !strings.Contains(pruned, `full output: C:\spill\s1-c1.txt`) {
		t.Fatalf("spill path missing: %q", pruned)
	}
	// The marker must also carry the recovery recipe, not just the path —
	// a bare path pushed models into blind re-read loops.
	if !strings.Contains(pruned, "read_file offset/limit") {
		t.Fatalf("marker missing recovery hint: %q", pruned)
	}
}

func TestPruneExemptTools(t *testing.T) {
	for _, n := range []string{"read_file", "list_dir", "glob"} {
		if !pruneExempt(n) {
			t.Errorf("%s must be exempt from pruning", n)
		}
	}
	for _, n := range []string{"exec", "search_files", "write_file"} {
		if pruneExempt(n) {
			t.Errorf("%s must NOT be exempt", n)
		}
	}
}

func TestSpillToolTextWritesFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spill")
	path := spillToolText(dir, "sess/1", "call:2", "full body")
	if path == "" {
		t.Fatal("spill returned empty path")
	}
	base := filepath.Base(path)
	if strings.ContainsAny(base, "/:\\") || !strings.HasPrefix(base, "sess_1-call_2") {
		t.Fatalf("unsafe file name: %s", base)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "full body" {
		t.Fatalf("spill content: %q, %v", b, err)
	}
	if p := spillToolText("", "s", "c", "x"); p != "" {
		t.Fatal("empty dir must disable spilling")
	}
}

// ---- loop-level prune events ----

type bigEchoTool struct{}

func (bigEchoTool) Name() string        { return "echotool" }
func (bigEchoTool) Description() string { return "big echo test tool" }
func (bigEchoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (bigEchoTool) IsHidden() bool { return false }
func (bigEchoTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult(strings.Repeat("A", 40000)), nil
}

// TestLoopPrunesOversizedToolResult asserts the dsh-style triple:
// full tool/result row → compaction/prune shadow event → pruned replacement
// row (same call_id, replaces set); the model history carries the pruned copy
// and the full text lands in the spill file.
func TestLoopPrunesOversizedToolResult(t *testing.T) {
	var requests int
	handler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_p1","type":"function","function":{"name":"echotool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(bigEchoTool{})
	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(bigEchoTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)
	spillRoot := t.TempDir()
	loop.SetSpillDir(spillRoot, "sessP")

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Run: err=%v", err)
	}

	types := rec.types()
	fullIdx, pruneIdx, replIdx := -1, -1, -1
	for i, ty := range types {
		p := rec.events[i].payload
		switch ty {
		case EventToolResult:
			if p != nil && p["replaces"] != nil {
				replIdx = i
			} else if _, isResult := p["result"]; isResult && fullIdx < 0 {
				fullIdx = i
			}
		case EventCompactionPrune:
			pruneIdx = i
		}
	}
	if fullIdx < 0 || pruneIdx < 0 || replIdx < 0 {
		t.Fatalf("missing prune triple, types=%v", types)
	}
	if !(fullIdx < pruneIdx && pruneIdx < replIdx) {
		t.Fatalf("wrong order: full=%d prune=%d repl=%d", fullIdx, pruneIdx, replIdx)
	}
	if n := len(rec.events[fullIdx].payload["result"].(string)); n != 40000 {
		t.Fatalf("full row lost its fullness: %d", n)
	}
	pp := rec.events[pruneIdx].payload
	if pp["call_id"] != "call_p1" || pp["chars_before"] != 40000 {
		t.Fatalf("prune payload: %v", pp)
	}
	repl := rec.events[replIdx].payload
	if pr := repl["result"].(string); len([]rune(pr)) >= 40000 || !strings.Contains(pr, "middle pruned") {
		t.Fatal("replacement not pruned")
	}

	// Model history: the tool message carries the pruned copy.
	var toolMsg string
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolMsg = m.GetText()
		}
	}
	if !strings.Contains(toolMsg, "middle pruned") {
		t.Fatal("history must carry the pruned copy")
	}
	// Spill file holds the full text.
	spill := filepath.Join(spillRoot, "sessP-call_p1.txt")
	b, err := os.ReadFile(spill)
	if err != nil || len(b) != 40000 {
		t.Fatalf("spill file: err=%v len=%d", err, len(b))
	}
}

// bigReadTool answers under the exempt name read_file.
type bigReadTool struct{}

func (bigReadTool) Name() string        { return "read_file" }
func (bigReadTool) Description() string { return "big read test tool" }
func (bigReadTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (bigReadTool) IsHidden() bool { return false }
func (bigReadTool) Execute(ctx context.Context, params map[string]interface{}) (*tools.ToolResult, error) {
	return tools.NewTextResult(strings.Repeat("R", 40000)), nil
}

// TestLoopExemptsReadFile pins the dsh spill-policy read exemption: a huge
// read_file result must reach the history intact. Cutting it would force the
// model to re-read the spill file through read_file, which would be pruned
// again — the read→spill→read loop dsh explicitly tests against.
func TestLoopExemptsReadFile(t *testing.T) {
	var requests int
	handler := func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			writeSSEChunk(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_r1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			writeSSEChunk(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}
	client, closeFn := newLedgerClient(t, handler)
	defer closeFn()

	reg := tools.NewToolRegistry()
	reg.Register(bigReadTool{})
	loop := NewAgentLoop(client, 5, reg, []provider.ToolSchema{tools.ToSchema(bigReadTool{})}, nil, nil, nil, nil)
	loop.compactionOn = false
	rec := &eventRecorder{}
	loop.SetEventSink(rec.sink)
	loop.SetSpillDir(t.TempDir(), "sessR")

	res, err := loop.Run(context.Background(), "sys", "go", nil)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Run: err=%v", err)
	}
	for _, ty := range rec.types() {
		if ty == EventCompactionPrune {
			t.Fatal("read_file result must be exempt from pruning")
		}
	}
	var toolMsg string
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolMsg = m.GetText()
		}
	}
	if len([]rune(toolMsg)) != 40000 || strings.Contains(toolMsg, "middle pruned") {
		t.Fatalf("read_file result was cut: %d runes", len([]rune(toolMsg)))
	}
}

// ---- concurrent multi-session appends (sharded ledger lock) ----

// TestAppendEventsConcurrentSessions: 8 sessions × 25 events appended in
// parallel must all succeed with contiguous per-session seq 1..25.
func TestAppendEventsConcurrentSessions(t *testing.T) {
	store := newEventTestStore(t)
	const sessions, per = 8, 25
	var wg sync.WaitGroup
	errs := make(chan string, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := "sess-" + string(rune('a'+i))
			for j := 0; j < per; j++ {
				seq, err := store.AppendEvent(sid, EventToolResult, 0, map[string]any{"j": j})
				if err != nil {
					errs <- sid + ": " + err.Error()
					return
				}
				if seq != int64(j+1) {
					errs <- sid + ": seq out of order"
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}
