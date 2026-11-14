package tools

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// chunkLog records what a streaming exec reported, with the wall time of each
// piece, so the test can tell "reported while running" from "reported at the end".
type chunkLog struct {
	mu     sync.Mutex
	items  []string
	times  []time.Time
	closed time.Time
}

func (c *chunkLog) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, s)
	c.times = append(c.times, time.Now())
}

func (c *chunkLog) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.items, "")
}

func (c *chunkLog) first() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.times) == 0 {
		return time.Time{}, false
	}
	return c.times[0], true
}

// TestExecStreamedReportsOutputWhileRunning is the point of the whole feature:
// the UI must see output before the command finishes, and the final result must
// be exactly what the non-streaming path returns.
func TestExecStreamedReportsOutputWhileRunning(t *testing.T) {
	t.Setenv("MC_EXEC_GATE", "0") // the sleep here is the fixture, not model behaviour
	command := "echo one; sleep 1; echo two"
	if runtime.GOOS == "windows" {
		// The pwsh tool speaks PowerShell on Windows; spell the wait and the
		// sequence in its own dialect.
		command = "echo one; Start-Sleep 1; echo two"
	}

	var log chunkLog
	start := time.Now()
	tool := NewExecTool(30, t.TempDir())
	t.Cleanup(func() { _ = tool.Close() })
	res, err := tool.
		ExecuteStreamed(context.Background(), map[string]interface{}{"command": command}, log.add)
	end := time.Now()
	if err != nil {
		t.Fatalf("ExecuteStreamed: %v", err)
	}

	out := log.String()
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
		t.Fatalf("stream missed output, got %q", out)
	}
	first, ok := log.first()
	if !ok {
		t.Fatal("no output was streamed at all")
	}
	if !first.Before(end) {
		t.Fatalf("first chunk arrived after the command finished")
	}
	// The command waits ~1s in the middle: the first line must have been
	// reported well before the end, otherwise this is just a dump at close.
	if end.Sub(first) < 300*time.Millisecond {
		t.Errorf("first chunk came %v before the end; expected the early part while still running", end.Sub(first))
	}
	if first.Before(start) {
		t.Error("chunk timestamp predates the call")
	}
	// The result the model sees is unchanged by the live feed.
	text := ""
	if res != nil {
		text = res.Text
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(text, want) {
			t.Fatalf("final result lost %q: %q", want, text)
		}
	}
}

// TestExecStreamedMarksStderr: the live feed separates the two streams, since a
// merged blob makes a failing command look like normal output.
func TestExecStreamedMarksStderr(t *testing.T) {
	command := "echo bad 1>&2"
	if runtime.GOOS == "windows" {
		command = "echo bad 1>&2"
	}
	var log chunkLog
	tool := NewExecTool(20, t.TempDir())
	t.Cleanup(func() { _ = tool.Close() })
	res, err := tool.
		ExecuteStreamed(context.Background(), map[string]interface{}{"command": command}, log.add)
	if err != nil {
		t.Fatalf("ExecuteStreamed: %v", err)
	}
	if !strings.Contains(log.String(), "[stderr]") {
		t.Fatalf("stderr was not labelled in the live feed: %q", log.String())
	}
	if res != nil && !strings.Contains(res.Text, "bad") {
		t.Fatalf("stderr lost from the final result: %q", res.Text)
	}
}

// TestExecUnstreamedStillWorks guards the delegation: Execute must not start
// depending on a sink it never passes.
func TestExecUnstreamedStillWorks(t *testing.T) {
	tool := NewExecTool(20, t.TempDir())
	t.Cleanup(func() { _ = tool.Close() })
	res, err := tool.Execute(context.Background(),
		map[string]interface{}{"command": "echo plain"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "plain") {
		t.Fatalf("unexpected result: %q", res.Text)
	}
}
