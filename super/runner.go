package super

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// runnerLoop is the super runner tick. It lives inside the minicode process
// lifetime (human requirement: minicode 生命周期常驻调度).
//
// 身份说明:runner 是 super 子系统自带的基础设施(本进程里的 15s 定时器),
// 不是 super worker,也不是 super agent 在"干活"。v9 之后它干五件事:
// 轮询到点的采集源(pollSources)、按决策表路由事件(在 tickets.go)、
// FIFO 放排队工单(PickUpTickets)、回收退订过期的绑定(gcBindings)、
// 以及旧世界的兜底(定时任务投递/缓冲冲刷/排队捞起)。
func (s *Service) runnerLoop(ctx context.Context) {
	s.startedAt = time.Now()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollSources()
			s.PickUpTickets()
			s.gcBindings()
			s.runDue()
			s.checkFlushTimeout()
			s.PickUpQueue()
		}
	}
}

// pollSources advances every enabled source whose poll clock came due. Each
// source gets its own goroutine — a git ls-remote can hit the network and a
// slow one must never hold the tick (the adapter itself carries a 30s cap).
func (s *Service) pollSources() {
	srcs, err := s.store.ListSources()
	if err != nil {
		return
	}
	for _, src := range srcs {
		if !src.Enabled {
			continue
		}
		if src.LastRun != "" {
			last, err := time.Parse(time.RFC3339, src.LastRun)
			if err == nil && time.Since(last) < time.Duration(src.PollSec)*time.Second {
				continue
			}
		}
		go s.pollSource(src)
	}
}

func (s *Service) pollSource(src *Source) {
	ad, err := GetKind(src.Kind)
	if err != nil || !ad.Enabled() {
		_ = s.store.SourceSetEnabled(src.ID, false)
		s.audit("runner", "source.disabled", src.ID, "类型 "+src.Kind+" 未注册/未启用,已自动停用", src.ID)
		return
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(src.ConfigJSON), &cfg); err != nil {
		_ = s.store.SourceSetResult(src.ID, time.Now().Format(time.RFC3339), "", "config JSON 损坏: "+err.Error())
		return
	}
	trig, newCursor, err := ad.Poll(s.ctx, cfg, src.Cursor)
	now := time.Now().Format(time.RFC3339)
	if err != nil {
		_ = s.store.SourceSetResult(src.ID, now, "", err.Error())
		if src.ErrCount == 0 {
			// 首次失败冒泡一次,后续静默只记审计(重试到成功自然清零)。
			s.audit("runner", "source.err", src.ID, truncate(err.Error(), 200), src.ID)
			s.notifyRouter(fmt.Sprintf("【采集源报错】「%s」(%s) 轮询失败:%s。Router 记录即可;连续失败人类可在采集源页签停用。",
				src.Name, src.Kind, truncate(err.Error(), 120)))
		}
		return
	}
	if src.ErrCount > 0 {
		s.audit("runner", "source.recover", src.ID, "轮询恢复正常", src.ID)
	}
	_ = s.store.SourceSetResult(src.ID, now, newCursor, "")
	if trig == nil {
		return // 基线静默 / 无变化
	}
	trig.SrcKey = src.ID
	s.audit("runner", "source.event", src.ID, truncate(trig.Text, 160), src.ID)
	s.HandleTrigger(src, trig)
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
