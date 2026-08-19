//go:build windows

package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCancelKillsProcessTree is the regression test for the "cannot
// interrupt" bug: cmd.exe was killed on cancel but its child (ping)
// survived as an orphan holding the output pipes, so cmd.Wait blocked for
// the full ping duration. With the job object, the whole tree must die
// within a second of cancellation.
func TestCancelKillsProcessTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd", "/C", "ping 127.0.0.1 -n 120 -w 1000")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	detach := attachProcessTree(cmd)
	go func() { <-ctx.Done(); detach() }()
	defer detach()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	// Let ping start, then cancel.
	time.Sleep(2 * time.Second)
	t0 := time.Now()
	cancel()

	select {
	case err := <-waitDone:
		elapsed := time.Since(t0)
		if elapsed > 5*time.Second {
			t.Errorf("Wait took %v after cancel, want < 5s (orphan was holding pipes)", elapsed)
		}
		t.Logf("Wait returned after %v: %v", elapsed, err)
	case <-time.After(20 * time.Second):
		t.Fatal("Wait did not return 20s after cancel — orphan pipe bug")
	}

	// The orphaned ping must be gone (job kill-on-close).
	time.Sleep(500 * time.Millisecond)
	out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq ping.exe", "/NH").Output()
	if strings.Contains(string(out), "ping.exe") {
		t.Fatalf("ping.exe survived cancellation:\n%s", out)
	}
}
