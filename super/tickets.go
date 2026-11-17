package super

// v9 决策路由:Source 事件 → thread(绑定会话)直投 / resume / 自动首绑;
// 人工与工单走统一的状态机 queued→running→ready→closed。
//
// 与旧「router 传话」模型的分界线在这里:事件不再灌给 Router 让它转述,
// 而是按决策表直达 thread;Router 退役为纯审计,只在 ready 冒泡时被叫醒
// 记一行。并发上限写死 3(v9 拍板),排队的工单按 rowid FIFO 出队——
// src_id 随工单携带,出队时不会找不回源。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// maxRunningTickets 并发上限 3 写死:同时最多三张工单在跑,其余排队。
const maxRunningTickets = 3

// threadPreamble 是自动首绑线程的开场公约(随首条任务注入,不进系统提示)。
const threadPreamble = `本会话是 super 采集源「%s」(%s) 的固定 thread(订阅地址)。采集事件会直达本会话,格式【源事件·名称】。
处置纪律:范围内的事直接处理;范围外的一次性事项 hand back(回复一句"范围外,建议另开工单"),不要顺手做;
有产出就写清楚"做了什么、结论是什么",系统会自动把本回合记为工单 ready 并冒泡给审计。`

// --- source service ---

// CreateSource validates through the kind's Probe and stores the source.
// bind_mode=assign 在建源时即完成绑定(人类在表单里已指定会话);
// bind_mode=auto 的源在第一个事件到来时自动首绑。
func (s *Service) CreateSource(kind, name, configJSON string, pollSec int, bindMode, bindSession, ttl, actor string) (*Source, error) {
	ad, err := GetKind(kind)
	if err != nil {
		return nil, err
	}
	if !ad.Enabled() {
		return nil, fmt.Errorf("类型 %s 本期未启用", kind)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name 必填")
	}
	if configJSON == "" {
		configJSON = "{}"
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("config 不是合法 JSON: %v", err)
	}
	if _, err := ad.Probe(s.ctx, cfg); err != nil {
		return nil, fmt.Errorf("连通性检查失败: %w", err)
	}
	if pollSec < 60 {
		pollSec = 60
	}
	if pollSec > 86400 {
		pollSec = 86400
	}
	if bindMode != "assign" {
		bindMode = "auto"
	}
	if ttl != "forever" {
		ttl = "3d"
	}
	if bindMode == "assign" {
		if _, err := s.activeBindingTarget(bindSession); err != nil {
			return nil, err
		}
	}
	src := &Source{
		Kind: kind, Name: name, ConfigJSON: configJSON, Enabled: true, PollSec: pollSec,
		BindMode: bindMode, BindSession: bindSession, TTL: ttl,
	}
	if err := s.store.CreateSource(src); err != nil {
		return nil, err
	}
	if bindMode == "assign" {
		if err := s.store.BindSource(&Binding{SrcID: src.ID, Sess: bindSession, Binder: orNone2(actor, "human")}); err != nil {
			return nil, err
		}
	}
	s.audit(orNone2(actor, "human"), "source.create", src.ID,
		fmt.Sprintf("%s「%s」· 每 %ds · 落地=%s", kind, name, pollSec, bindMode), src.ID)
	return src, nil
}

// TestSource runs the kind Probe against the stored config (面板的"测连通")。
func (s *Service) TestSource(id string) (string, error) {
	src, err := s.store.GetSource(id)
	if err != nil || src == nil {
		return "", fmt.Errorf("采集源不存在: %s", id)
	}
	ad, err := GetKind(src.Kind)
	if err != nil {
		return "", err
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(src.ConfigJSON), &cfg); err != nil {
		return "", err
	}
	sum, err := ad.Probe(s.ctx, cfg)
	if err != nil {
		s.audit("human", "source.test.fail", id, truncate(err.Error(), 200), id)
		return "", err
	}
	s.audit("human", "source.test", id, sum, id)
	return sum, nil
}

// ToggleSource is the per-source switch (audited under the caller's actor).
func (s *Service) ToggleSource(id, actor string) (bool, error) {
	src, err := s.store.GetSource(id)
	if err != nil || src == nil {
		return false, fmt.Errorf("采集源不存在: %s", id)
	}
	on := !src.Enabled
	if err := s.store.SourceSetEnabled(id, on); err != nil {
		return false, err
	}
	if actor == "" {
		actor = "human"
	}
	s.audit(actor, "source.toggle", id, "enabled="+fmt.Sprint(on), id)
	return on, nil
}

func (s *Service) DeleteSource(id, actor string) error {
	src, err := s.store.GetSource(id)
	if err != nil || src == nil {
		return fmt.Errorf("采集源不存在: %s", id)
	}
	if err := s.store.DeleteSource(id); err != nil {
		return err
	}
	s.audit(orNone2(actor, "human"), "source.delete", id, "「"+src.Name+"」(绑定同删,工单留档)", id)
	return nil
}

// --- binding (订阅即 thread 地址) ---

// activeBindingTarget validates a session as a bindable thread: it must be a
// live bound worker session (super 会话族正册)。
func (s *Service) activeBindingTarget(sessID string) (string, error) {
	sessID = strings.TrimSpace(sessID)
	if sessID == "" {
		return "", fmt.Errorf("bind_session 必填:指定一个已有 worker 会话作为 thread")
	}
	b, err := s.store.GetSessionBinding(sessID)
	if err != nil || b == nil {
		return "", fmt.Errorf("会话 %s 不是 super worker 的绑定会话，不能作 thread", sessID)
	}
	if b.Status != "active" {
		return "", fmt.Errorf("会话 %s 已关闭，不能作 thread", sessID)
	}
	return sessID, nil
}

// BindThread 人类改挂 thread(也用于复订)。
func (s *Service) BindThread(srcID, sessID, actor string) error {
	src, err := s.store.GetSource(srcID)
	if err != nil || src == nil {
		return fmt.Errorf("采集源不存在: %s", srcID)
	}
	sess, err := s.activeBindingTarget(sessID)
	if err != nil {
		return err
	}
	old, _ := s.store.GetBinding(srcID)
	if err := s.store.BindSource(&Binding{SrcID: srcID, Sess: sess, Binder: orNone2(actor, "human")}); err != nil {
		return err
	}
	detail := "thread → " + sess
	if old != nil {
		detail = fmt.Sprintf("thread %s → %s", old.Sess, sess)
	}
	s.audit(orNone2(actor, "human"), "source.bind", srcID, "「"+src.Name+"」 "+detail, srcID+","+sess)
	return nil
}

// Unbind 退订:关闭绑定进入宽限期(3d;forever 则永不过期)。
func (s *Service) Unbind(srcID string, forever bool, actor string) error {
	b, err := s.store.GetBinding(srcID)
	if err != nil || b == nil {
		return fmt.Errorf("该源没有绑定")
	}
	if err := s.store.BindingUnsubscribe(srcID, forever); err != nil {
		return err
	}
	grace := "3d"
	if forever {
		grace = "永久"
	}
	s.audit(orNone2(actor, "human"), "source.unbind", srcID,
		fmt.Sprintf("退订(宽限 %s) thread=%s", grace, b.Sess), srcID+","+b.Sess)
	return nil
}

// gcBindings drops 宽限已过期的退订绑定 → 源回到未绑定状态。
func (s *Service) gcBindings() {
	cutoff := time.Now().Add(-72 * time.Hour).Format(time.RFC3339)
	dropped, err := s.store.BindingDropExpired(cutoff)
	if err != nil || len(dropped) == 0 {
		return
	}
	for _, id := range dropped {
		s.audit("runner", "binding.drop", id, "退订宽限 3d 已过,绑定回收(源回到未绑定)", id)
	}
}

// --- ticket 状态机 ---

// CreateTicket 开一张工单:srcID="" 为人工工单(router/人类);text 是原话,
// note 是 ≤30 字转述。能落地立刻落地,并发满则 queued 按 FIFO 等名额。
func (s *Service) CreateTicket(srcID, text, note, title, sessID, actor string) (*Ticket, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("text(原话)不能为空")
	}
	srcID = strings.TrimSpace(srcID)
	var src *Source
	if srcID != "" {
		var err error
		src, err = s.store.GetSource(srcID)
		if err != nil || src == nil {
			return nil, fmt.Errorf("采集源不存在: %s", srcID)
		}
	}
	sessID = strings.TrimSpace(sessID)
	if sessID != "" {
		v, err := s.activeBindingTarget(sessID)
		if err != nil {
			return nil, err
		}
		sessID = v
	}
	if title == "" {
		title = truncate(text, 40)
	}
	t := &Ticket{SrcID: srcID, Title: truncate(title, 80), Note: truncate(strings.TrimSpace(note), 60),
		Text: truncate(text, 6000), Session: sessID, Actor: orNone2(actor, "human")}
	if err := s.store.CreateTicket(t); err != nil {
		return nil, err
	}
	s.audit(orNone2(actor, "human"), "ticket.create", t.ID,
		fmt.Sprintf("开单=%s · %s → %s", map[string]string{"human": "人类", "super": "router", "auto": "系统"}[t.Actor], t.Title, orNone2(sessID, "待落地")), t.ID+","+srcID)
	go s.PickUpTickets()
	return t, nil
}

// CloseTicket 人类验收(或 router 记录完结)→ closed。
func (s *Service) CloseTicket(id, actor string) error {
	t, err := s.store.GetTicket(id)
	if err != nil || t == nil {
		return fmt.Errorf("工单不存在: %s", id)
	}
	if t.State == "closed" {
		return fmt.Errorf("工单 %s 已是 closed", id)
	}
	if err := s.store.TicketSetState(id, "closed", ""); err != nil {
		return err
	}
	s.audit(orNone2(actor, "human"), "ticket.close", id, "验收:「"+t.Title+"」", id+","+t.Session)
	go s.PickUpTickets()
	return nil
}

// PickUpTickets FIFO 出队:running < 3 时按创建序把 queued 工单落地。
func (s *Service) PickUpTickets() {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	for {
		running, err := s.store.TicketCountState("running")
		if err != nil || running >= maxRunningTickets {
			return
		}
		if !s.pickOne() {
			return
		}
	}
}

// pickOne tries the next dispatchable queued ticket; reports work done.
// A ticket that cannot land (thread 没了/未绑定且无会话) stays queued with
// the reason audited — 不丢弃,人类修好绑定它自然出队。
func (s *Service) pickOne() bool {
	qs, err := s.store.TicketQueuedFIFO(8)
	if err != nil || len(qs) == 0 {
		return false
	}
	for _, t := range qs {
		sess, delivered, err := s.resolveLanding(t)
		if err != nil {
			_ = s.store.AuditInsert("runner", "ticket.pick.fail", t.ID, truncate(err.Error(), 200), t.ID)
			continue
		}
		if !delivered {
			// 首绑路径已把开场白装进 spawn task(delivered=true),不再重复投递。
			opening := s.openingText(t)
			if err := s.mgr.DeliverToSession(sess, opening); err != nil {
				_ = s.store.AuditInsert("runner", "ticket.pick.fail", t.ID, "投递失败: "+truncate(err.Error(), 150), t.ID)
				continue
			}
		}
		if err := s.store.TicketSetState(t.ID, "running", sess); err != nil {
			return false
		}
		_ = s.store.SetSessionStatus(sess, "active") // resume: 关闭过的 thread 被工单唤醒
		s.audit("runner", "ticket.dispatch", t.ID, "「"+t.Title+"」→ "+sess, t.ID+","+sess)
		return true
	}
	return false
}

// resolveLanding decides where a queued ticket runs: its explicit session →
// the source's active binding (复活宽限期内的 thread) → 自动首绑(new thread)。
// delivered=true 表示开场白已随落地动作投出(首绑的 spawn task 内含),
// 调用方不得再投第二遍。
func (s *Service) resolveLanding(t *Ticket) (sess string, delivered bool, err error) {
	if t.Session != "" {
		if _, gerr := s.mgr.Get(t.Session); gerr == nil {
			return t.Session, false, nil
		}
		return "", false, fmt.Errorf("指定会话 %s 已不存在", t.Session)
	}
	if t.SrcID == "" {
		return "", false, fmt.Errorf("人工工单未指定会话，无处落地")
	}
	b, _ := s.store.GetBinding(t.SrcID)
	if b != nil && b.State == "active" {
		if _, gerr := s.mgr.Get(b.Sess); gerr == nil {
			return b.Sess, false, nil
		}
	}
	src, _ := s.store.GetSource(t.SrcID)
	if src == nil {
		return "", false, fmt.Errorf("源已删除，工单可关闭")
	}
	if src.BindMode == "assign" {
		if _, berr := s.activeBindingTarget(src.BindSession); berr == nil {
			_ = s.store.BindSource(&Binding{SrcID: src.ID, Sess: src.BindSession, Binder: "auto"})
			s.audit("runner", "source.bind", src.ID, "按建源意向复挂 thread "+src.BindSession, src.ID+","+src.BindSession)
			return src.BindSession, false, nil
		}
		return "", false, fmt.Errorf("assign 指定会话不可用(%s)", truncate(src.BindSession, 16))
	}
	// auto: 首绑 — 每源一个 src-<name> worker,thread 固定会话。
	sid, ferr := s.firstBind(src, t)
	return sid, true, ferr
}

// firstBind 自动首绑:建 src-<name> worker(已存在则复用)、开 thread 会话、
// 登记绑定。返回 thread 会话 id。
func (s *Service) firstBind(src *Source, t *Ticket) (string, error) {
	name := "src-" + sanitizeTaskName(src.Name)
	if name == "src-" {
		name = "src-" + src.ID[4:]
	}
	w, _ := s.store.GetWorkerByName(name)
	if w != nil && w.Status != "active" {
		w = nil // 退役过的不复用,新开会留新 id 后缀
		name = fmt.Sprintf("%s-%d", name, time.Now().Unix()%100000)
	}
	if w == nil {
		var err error
		w, err = s.CreateWorker(name, fmt.Sprintf("采集源「%s」(%s) 的固定执行体:处理该源直达 thread 的事件与工单。", src.Name, src.Kind), "")
		if err != nil {
			return "", fmt.Errorf("首绑建 worker 失败(名额 %d?): %w", s.Config().WorkerCap, err)
		}
	}
	task := fmt.Sprintf(threadPreamble+"\n\n---\n\n%s", src.Name, src.Kind, s.openingText(t))
	sessID, err := s.SpawnSession(w.ID, task, "thread·"+truncate(src.Name, 30))
	if err != nil {
		return "", err
	}
	if err := s.store.BindSource(&Binding{SrcID: src.ID, Sess: sessID, Binder: "auto"}); err != nil {
		s.audit("runner", "source.bind.fail", src.ID, "首绑会话已建但绑定登记失败: "+truncate(err.Error(), 120), src.ID+","+sessID)
		return "", err
	}
	s.audit("auto", "source.firstbind", src.ID,
		fmt.Sprintf("「%s」自动首绑 → worker %s · thread %s", src.Name, w.ID, sessID), src.ID+","+w.ID+","+sessID)
	s.notifyRouter(fmt.Sprintf("【首绑完成】采集源「%s」(%s) 自动建立 thread 会话 %s(worker %s),首个工单 %s 已派发。Router 只记录,无需回复。",
		src.Name, src.Kind, sessID, w.ID, t.ID))
	return sessID, nil
}

// openingText 工单开场:原话一字不改 + ≤30字备注。
func (s *Service) openingText(t *Ticket) string {
	head := "【工单 " + t.ID + "】"
	if t.SrcID != "" {
		if src, _ := s.store.GetSource(t.SrcID); src != nil {
			head = fmt.Sprintf("【工单 %s · 源「%s」】", t.ID, src.Name)
		}
	}
	msg := head + "\n原话:\n" + t.Text
	if t.Note != "" {
		msg += "\n（备注·" + t.Note + "）"
	}
	return msg + "\n（完成后写清结论;范围外事项 hand back,别顺手做。）"
}

// --- 事件路由(v9 决策表) ---

// HandleTrigger routes one source event: 直投 active thread / 宽限复订 /
// resume+新工单 / 未绑定 → 建单交给 PickUpTickets(auto 首绑或 assign 复挂)。
func (s *Service) HandleTrigger(src *Source, tr *Trigger) {
	tr.SrcKey = src.ID
	b, _ := s.store.GetBinding(src.ID)
	if b != nil {
		alive := false
		if sess, err := s.mgr.Get(b.Sess); err == nil && sess != nil {
			alive = true
		}
		if alive {
			if b.State == "unsubscribed" {
				if err := s.store.BindingReactivate(src.ID); err == nil {
					s.audit("runner", "binding.reactivate", src.ID, "宽限期内事件再达,自动复订 thread "+b.Sess, src.ID+","+b.Sess)
				}
			}
			act, _ := s.store.TicketActiveBySrc(src.ID)
			if act != nil && act.Session == b.Sess {
				// 直投:已有在办工单罩着这个 thread,事件作为来件直达,不过 Router。
				if err := s.mgr.DeliverToSession(b.Sess, fmt.Sprintf("【源事件·%s】%s\n%s", src.Name, tr.Actor, tr.Text)); err != nil {
					s.audit("runner", "source.deliver.fail", src.ID, truncate(err.Error(), 150), src.ID+","+b.Sess)
					return
				}
				s.audit("runner", "source.deliver", src.ID, "直投 thread "+b.Sess+" · "+truncate(tr.Text, 100), src.ID+","+b.Sess)
				return
			}
			// thread 上没有在办工单(ready/closed 后追进来的) → resume + 新工单
			t := &Ticket{SrcID: src.ID, Title: truncate(tr.Text, 40), Text: truncate(tr.Text, 6000),
				Session: b.Sess, Actor: "auto"}
			if err := s.store.CreateTicket(t); err == nil {
				s.audit("auto", "ticket.create", t.ID, "源事件(ready 后追聊=resume) → "+b.Sess, t.ID+","+src.ID)
				_ = s.store.SetSessionStatus(b.Sess, "active")
				go s.PickUpTickets()
			}
			return
		}
	}
	// 未绑定(或 thread 会话已不存在):落地方式由建源表单声明。
	if src.BindMode == "assign" {
		if _, err := s.activeBindingTarget(src.BindSession); err != nil {
			s.audit("runner", "source.event.stall", src.ID, "assign 会话不可用:"+truncate(err.Error(), 150), src.ID)
			s.notifyRouter(fmt.Sprintf("【事件滞留】采集源「%s」的事件到了,但指定会话 %s 不可用(%s)。请提醒人类在采集源页签重绑。",
				src.Name, truncate(src.BindSession, 16), truncate(err.Error(), 80)))
			return
		}
	}
	t := &Ticket{SrcID: src.ID, Title: truncate(tr.Text, 40), Text: truncate(tr.Text, 6000), Actor: "auto"}
	if err := s.store.CreateTicket(t); err != nil {
		s.audit("runner", "ticket.create.fail", src.ID, truncate(err.Error(), 150), src.ID)
		return
	}
	s.audit("auto", "ticket.create", t.ID, "源事件建单 · "+truncate(tr.Text, 100), t.ID+","+src.ID)
	go s.PickUpTickets()
}

// notifyRouter injects one audit-grade line into the resident Router session.
// v9: these lines are the Router's only diet — 记录即可,不打断人类。
func (s *Service) notifyRouter(text string) {
	s.inject(text)
}
