package super

import (
	"context"
	"fmt"
	"time"
)

// runnerLoop is the super runner tick. It lives inside the minicode process
// lifetime (human requirement: minicode 生命周期常驻调度).
//
// 身份说明:runner 是 super 子系统自带的基础设施(本进程里的 15s 定时器),
// 不是 super worker,也不是 super agent 在"干活"。它只做三件事:到点把任务
// 文本投递进消息通道、兜底冲刷缓冲、捞起排队任务。真正的执行永远发生在人类
// 或 super 派给 worker 的地方。定时任务没有脚本——任务就是一句话,存 db。
func (s *Service) runnerLoop(ctx context.Context) {
	s.startedAt = time.Now()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runDue()
			s.checkFlushTimeout()
			s.PickUpQueue()
		}
	}
}

// runDue fires every enabled task schedule whose period elapsed.
func (s *Service) runDue() {
	scheds, err := s.store.ListSchedules()
	if err != nil {
		return
	}
	for _, sc := range scheds {
		if !sc.Enabled || sc.Kind != "task" {
			continue
		}
		if sc.LastRun == "" {
			go s.deliverTaskSchedule(sc, "auto")
			continue
		}
		last, err := time.Parse(time.RFC3339, sc.LastRun)
		if err != nil || time.Since(last) >= time.Duration(sc.EverySec)*time.Second {
			go s.deliverTaskSchedule(sc, "auto")
		}
	}
}

// ToggleSchedule is the per-schedule switch (audited under the caller's
// actor: "human" from the panel, "super" from its own tool).
func (s *Service) ToggleSchedule(file, actor string) (bool, error) {
	sc, err := s.store.GetSchedule(file)
	if err != nil || sc == nil {
		return false, fmt.Errorf("计划不存在: %s", file)
	}
	on := !sc.Enabled
	if err := s.store.ScheduleSetEnabled(file, on); err != nil {
		return false, err
	}
	if actor == "" {
		actor = "human"
	}
	s.audit(actor, "schedule.toggle", file, "enabled="+fmt.Sprint(on), file)
	return on, nil
}

// RunScheduleNow delivers one schedule onto the channel immediately.
func (s *Service) RunScheduleNow(file, actor string) {
	if actor == "" {
		actor = "human"
	}
	sc, err := s.store.GetSchedule(file)
	if err != nil || sc == nil {
		s.audit(actor, "schedule.run_now.fail", file, "计划不存在", file)
		return
	}
	s.audit(actor, "schedule.run_now", file, "立即投递一次", file)
	go s.deliverTaskSchedule(sc, "manual")
}

// deliverTaskSchedule fires one task schedule: the task text lands on the
// message channel (buffered unless the schedule opts into imm), and the
// router picks it up on the next flush to decide which worker executes it.
// This is how a "scheduled job" exists without super ever writing code.
func (s *Service) deliverTaskSchedule(sc *Schedule, trigger string) {
	mode := "buf"
	if sc.Mode == "imm" {
		mode = "imm"
	}
	payload := fmt.Sprintf("【定时任务·%s】%s\n（来自计划 %s，每 %ds 触发，trigger=%s。请按分派纪律安排 worker 执行。）",
		mode, sc.Note, sc.File, sc.EverySec, trigger)
	if _, err := s.Publish(mode, sc.File, payload, "schedule"); err != nil {
		s.audit("runner", "schedule.deliver.fail", sc.File, truncate(err.Error(), 150), sc.File)
		return
	}
	_ = s.store.ScheduleSetResult(sc.File, time.Now().Format(time.RFC3339), 0)
	s.histMu.Lock()
	rec := ExecRec{Ts: time.Now().Format("15:04:05"), Exit: 0, Ms: 0, Tail: "已投递" + mode + "通道"}
	s.hist[sc.File] = append([]ExecRec{rec}, s.hist[sc.File]...)
	if len(s.hist[sc.File]) > 8 {
		s.hist[sc.File] = s.hist[sc.File][:8]
	}
	s.histMu.Unlock()
	s.audit("runner", "schedule.deliver", sc.File,
		fmt.Sprintf("%s · 每 %ds · %s · %s", mode, sc.EverySec, trigger, truncate(sc.Note, 100)), sc.File)
}
