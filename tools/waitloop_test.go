package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPureWaitSeconds(t *testing.T) {
	cases := []struct {
		command string
		want    int
	}{
		// Pure waits: nothing in them observes anything.
		{`ping -n 40 127.0.0.1 >nul & echo waited`, 40},
		{`timeout /t 60 /nobreak >nul`, 60},
		{`cd /d D:\somewhere\repo && timeout /t 60 /nobreak >nul`, 60},
		{`powershell -NoProfile -Command "Start-Sleep -Seconds 45"`, 45},
		{`Start-Sleep -Seconds 30`, 30},
		{`ping -n 25 127.0.0.1`, 25},
		// Not waits: something is looked at, so this is a poll, and a poll is
		// behavior to measure rather than a command to veto.
		{`ping -n 5 127.0.0.1 >nul & type out.txt`, 0},
		{`powershell -NoProfile -Command "Start-Sleep -Seconds 45; Get-ChildItem x"`, 0},
		{`"python.exe" -c "import time;time.sleep(25);print(1)"`, 0},
		{`ping -n 40 127.0.0.1 | findstr reply`, 0},
		{`dir`, 0},
		// Below the intercept threshold: a short settle before a retry is not
		// babysitting. The count is still reported; the caller decides.
		{`ping -n 10 127.0.0.1 >nul`, 10},
	}
	for _, tc := range cases {
		if got := pureWaitSeconds(tc.command); got != tc.want {
			t.Errorf("pureWaitSeconds(%q) = %d, want %d", tc.command, got, tc.want)
		}
	}
}

// TestWaitLoopIntercepted pins that a pure wait never reaches a process, on
// either path: the job path must report "nothing started" (so the loop can fall
// through) and the ordinary path must hand the model the explanation.
func TestWaitLoopIntercepted(t *testing.T) {
	tool := NewExecTool(30, t.TempDir())
	defer tool.Close() // the short case below does run, on a real shell in that dir
	cmd := `ping -n 40 127.0.0.1 >nul & echo waited`
	call, err := tool.StartAsync(context.Background(), "s", map[string]interface{}{"command": cmd}, nil)
	if call != nil {
		t.Fatalf("a pure wait started a job: %+v", call)
	}
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("StartAsync(pure wait) err = %v, want the skip text", err)
	}
	res, serr := tool.Execute(context.Background(), map[string]interface{}{"command": cmd})
	if serr != nil || res == nil {
		t.Fatalf("Execute(pure wait): err=%v res=%v", serr, res)
	}
	if !strings.Contains(res.Text, "skipped") || !strings.Contains(res.Text, "job_output") {
		t.Errorf("Execute(pure wait) = %q, want the skip text pointing at job_output", res.Text)
	}
	// And the short one still runs: 1s of settling must not become a lecture.
	res2, err2 := tool.Execute(context.Background(), map[string]interface{}{"command": "ping -n 2 127.0.0.1 >nul & echo ok"})
	if err2 != nil || res2 == nil || strings.Contains(res2.Text, "skipped") {
		t.Fatalf("short settle was intercepted: err=%v res=%v", err2, res2)
	}
}
