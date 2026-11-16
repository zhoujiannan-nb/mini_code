package super

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// scriptTimeout bounds one script execution; the runner kills past it.
const scriptTimeout = 120 * time.Second

// runnerLoop is the super runner tick: script schedules, the batch timeout
// flush and queue pickup all hang off it. It lives inside the minicode
// process lifetime (human requirement: minicode 生命周期常驻调度).
func (s *Service) runnerLoop(ctx context.Context) {
	s.startedAt = time.Now()
	s.discoverSchedules()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.discoverSchedules()
			s.runDue()
			s.checkFlushTimeout()
			s.PickUpQueue()
		}
	}
}

// discoverSchedules syncs the scripts folder into super_schedule: a published
// script goes live without any human ceremony (default: every 300s, buffer).
func (s *Service) discoverSchedules() {
	entries, err := os.ReadDir(s.scripts)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !scriptExts[ext] {
			continue
		}
		if sc, err := s.store.GetSchedule(e.Name()); err == nil && sc != nil {
			continue
		}
		sc := &Schedule{File: e.Name(), Kind: strings.TrimPrefix(ext, "."), EverySec: 300, Mode: "buf", Enabled: true}
		if err := s.store.UpsertSchedule(sc); err != nil {
			slog.Warn("super: schedule upsert failed", "file", e.Name(), "err", err)
			continue
		}
		s.audit("system", "schedule.discover", sc.File, "脚本发布即上线（默认 300s · 缓冲通道）", sc.File)
	}
}

var scriptExts = map[string]bool{".ps1": true, ".py": true, ".bat": true, ".cmd": true, ".exe": true}

// runDue fires every enabled schedule whose period elapsed.
func (s *Service) runDue() {
	scheds, err := s.store.ListSchedules()
	if err != nil {
		return
	}
	for _, sc := range scheds {
		if !sc.Enabled {
			continue
		}
		if sc.LastRun == "" {
			go s.runScript(sc.File, "auto")
			continue
		}
		last, err := time.Parse(time.RFC3339, sc.LastRun)
		if err != nil || time.Since(last) >= time.Duration(sc.EverySec)*time.Second {
			go s.runScript(sc.File, "auto")
		}
	}
}

// ToggleSchedule is the human per-script switch (actor=human audited).
func (s *Service) ToggleSchedule(file string) (bool, error) {
	sc, err := s.store.GetSchedule(file)
	if err != nil || sc == nil {
		return false, fmt.Errorf("脚本计划不存在: %s", file)
	}
	on := !sc.Enabled
	if err := s.store.ScheduleSetEnabled(file, on); err != nil {
		return false, err
	}
	s.audit("human", "human.config.script", file, "enabled="+fmt.Sprint(on), file)
	return on, nil
}

// RunScheduleNow is the human ▶-run-once trigger.
func (s *Service) RunScheduleNow(file string) {
	s.audit("human", "human.trigger", file, "立即跑一次（actor=human）", file)
	go s.runScript(file, "manual")
}

// runScript executes one schedule: super_process row around it, full output
// archived under outbox/, stdout (if any) published to the channel in the
// schedule's mode.
func (s *Service) runScript(file, trigger string) {
	s.runMu.Lock()
	if s.running[file] {
		s.runMu.Unlock()
		return
	}
	s.running[file] = true
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		delete(s.running, file)
		s.runMu.Unlock()
	}()

	abs := filepath.Join(s.scripts, file)
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		s.audit("runner", "proc.fail", file, "脚本文件不存在", file)
		return
	}
	sc, err := s.store.GetSchedule(file)
	if err != nil || sc == nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(file))
	var cmd *exec.Cmd
	switch ext {
	case ".ps1":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", abs)
	case ".py":
		cmd = exec.Command("python", abs)
	case ".bat", ".cmd":
		cmd = exec.Command("cmd", "/c", abs)
	case ".exe":
		cmd = exec.Command(abs)
	default:
		s.audit("runner", "proc.fail", file, "不支持的脚本类型 "+ext, file)
		return
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Dir = s.scripts
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		s.audit("runner", "proc.spawn.fail", file, truncate(err.Error(), 150), file)
		return
	}
	proc := &Process{Kind: "script", Name: file, PID: cmd.Process.Pid, Status: "run", ExitCode: -999, Ref: file}
	if err := s.store.ProcInsert(proc); err != nil {
		slog.Warn("super: proc row not recorded", "file", file, "err", err)
	}
	s.audit("runner", "proc.spawn", proc.ID, fmt.Sprintf("script · %s · pid %d · %s", file, proc.PID, trigger), file)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exit := 0
	select {
	case werr := <-done:
		if werr != nil {
			var ee *exec.ExitError
			if errors.As(werr, &ee) {
				exit = ee.ExitCode()
			} else {
				exit = -1
			}
		}
	case <-time.After(scriptTimeout):
		_ = cmd.Process.Kill()
		<-done
		exit = 124
		out.WriteString("\n[super runner] 超过 120s，已终止\n")
	}
	ms := time.Since(t0).Milliseconds()

	// archive the full output next to the database, reference it from history
	logName := fmt.Sprintf("%s.%s.log", file, t0.Format("20060102-150405"))
	_ = os.WriteFile(filepath.Join(s.outbox, logName),
		[]byte(fmt.Sprintf("# %s · exit %d · %dms\n%s", abs, exit, ms, out.String())), 0o644)

	if proc.ID != "" {
		_ = s.store.ProcEnd(proc.ID, exit)
	}
	_ = s.store.ScheduleSetResult(file, time.Now().Format(time.RFC3339), exit)
	s.audit("runner", "proc.exit", proc.ID, fmt.Sprintf("%s · exit %d · %dms · out→outbox/%s", file, exit, ms, logName), file)

	s.histMu.Lock()
	rec := ExecRec{Ts: t0.Format("15:04:05"), Exit: exit, Ms: ms, Tail: truncate(lastLine(out.String()), 160)}
	s.hist[file] = append([]ExecRec{rec}, s.hist[file]...)
	if len(s.hist[file]) > 8 {
		s.hist[file] = s.hist[file][:8]
	}
	s.histMu.Unlock()

	payload := strings.TrimSpace(out.String())
	if payload != "" {
		mode := sc.Mode
		if mode != "imm" {
			mode = "buf"
		}
		if _, err := s.Publish(mode, file, payload, "runner"); err != nil {
			slog.Warn("super: script output not published", "file", file, "err", err)
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
