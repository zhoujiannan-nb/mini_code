package super

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/config"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

// Service is the lite Super-Agent: the resident entry session plus the
// schedule runner (到点把一句话投给绑定会话). Everything heavier than that
// has been deliberately deleted — no workers, no channel, no tickets.
type Service struct {
	store *Store
	mgr   *session.SessionManager
	hub   *channels.Hub // 保留注入位:面板显示绑定会话是否忙碌

	dir string // ~/.mini_code/super(super 会话的专属工作区在其下)

	mu        sync.Mutex
	superID   string
	startedAt time.Time

	ctx context.Context
}

func NewService(db *util.Database, mgr *session.SessionManager, hub *channels.Hub) *Service {
	dir, err := config.GetConfigDir()
	base := filepath.Join(os.TempDir(), "mini_code_super")
	if err == nil {
		base = filepath.Join(dir, "super")
	}
	s := &Service{
		store: NewStore(db),
		mgr:   mgr,
		hub:   hub,
		dir:   base,
		ctx:   context.Background(),
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		slog.Warn("super: cannot create dir", "dir", s.dir, "err", err)
	}
	return s
}

// --- lifecycle ---

// Start launches the runner tick. Safe to call once per process.
func (s *Service) Start(ctx context.Context) {
	s.ctx = ctx
	s.startedAt = time.Now()
	_ = s.store.AuditInsert("system", "super.boot", "runner", "super(定时任务版)已随 minicode 常驻启动", "")
	go s.runnerLoop(ctx)
}

// runnerLoop is the resident 15s ticker: fire every due schedule. It lives
// with the minicode process lifetime (人类要求:minicode 生命周期常驻调度)。
func (s *Service) runnerLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runDue()
		}
	}
}

func (s *Service) runDue() {
	scheds, err := s.store.ListSchedules()
	if err != nil {
		return
	}
	now := time.Now()
	for _, sc := range scheds {
		if sc.Due(now) {
			go s.deliver(sc, "auto")
		}
	}
}

// --- super session (entry) ---

// SuperSessionID returns the cached id of the resident super session.
func (s *Service) SuperSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.superID
}

// RelatedSessionIDs: sessions the normal sidebar must hide — just the
// resident super session. 普通会话绑定给定时任务后仍是普通会话(侧栏照旧
// 可见);面板里只通过抽屉"看见"它们,不夺走人类的原入口。
func (s *Service) RelatedSessionIDs() map[string]bool {
	out := map[string]bool{}
	if id := s.SuperSessionID(); id != "" {
		out[id] = true
	}
	return out
}

// EnsureSuper finds or creates the one resident super session (agent_role
// "super") and pins its title so the automatic titler never renames it.
func (s *Service) EnsureSuper() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.superID != "" {
		if sess, err := s.mgr.Get(s.superID); err == nil && sess != nil {
			return s.superID, nil
		}
		s.superID = ""
	}
	if id := s.store.StateGet("super_session_id"); id != "" {
		if sess, err := s.mgr.Get(id); err == nil && sess != nil && sess.AgentRole() == "super" {
			s.superID = id
			return id, nil
		}
	}
	if row, err := s.store.db.FetchOne(
		"SELECT session_id FROM sessions WHERE agent_role = 'super' AND COALESCE(parent_id,'') = '' ORDER BY created_at ASC LIMIT 1"); err == nil && row != nil {
		id := asStr(row["session_id"])
		if sess, err := s.mgr.Get(id); err == nil && sess != nil {
			s.superID = id
			_ = s.store.StateSet("super_session_id", id)
			return id, nil
		}
	}
	dir := filepath.Join(s.dir, "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create super workspace %s: %w", dir, err)
	}
	sess, err := s.mgr.CreateSessionRecord("Super-Agent", "super", dir, nil)
	if err != nil {
		return "", fmt.Errorf("create super session: %w", err)
	}
	if err := s.mgr.SetReasoning(sess.ID(), "disabled", ""); err != nil {
		slog.Debug("super: thinking-off not applied", "session", sess.ID(), "err", err)
	}
	if err := s.mgr.RenameSession(sess.ID(), "Super-Agent"); err != nil {
		slog.Warn("super: title not pinned", "session", sess.ID(), "err", err)
	}
	s.superID = sess.ID()
	_ = s.store.StateSet("super_session_id", sess.ID())
	_ = s.store.AuditInsert("system", "super.session", sess.ID(), "super 会话已创建（定时任务入口）", sess.ID())
	return sess.ID(), nil
}

// inject delivers one text to the super session itself.
func (s *Service) inject(text string) {
	id, err := s.EnsureSuper()
	if err != nil {
		return
	}
	if err := s.mgr.DeliverToSession(id, text); err != nil {
		_ = s.store.AuditInsert("system", "super.inject.fail", id, truncate(err.Error(), 150), id)
	}
}

// --- schedule service ---

const (
	minEverySec = 30
	maxEverySec = 30 * 86400
)

// CreateSchedule registers one 定时任务: every everySec, `text` is delivered
// verbatim to the bound session ("" = the super session itself).
func (s *Service) CreateSchedule(name, text, sessionID string, everySec int, actor string) (*Schedule, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("要发的那句话(text)不能为空")
	}
	if everySec < minEverySec {
		everySec = minEverySec
	}
	if everySec > maxEverySec {
		return nil, fmt.Errorf("间隔超过 30 天上限")
	}
	if sessionID != "" {
		if sess, err := s.mgr.Get(sessionID); err != nil || sess == nil {
			return nil, fmt.Errorf("绑定会话不存在: %s", truncate(sessionID, 16))
		}
	} else if id, err := s.EnsureSuper(); err == nil {
		sessionID = id
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = truncate(text, 24)
	}
	sc := &Schedule{
		ID: "sch-" + util.RandomHex(6), Name: truncate(name, 60), Text: text,
		Session: sessionID, EverySec: everySec, Enabled: true,
	}
	if err := s.store.CreateSchedule(sc); err != nil {
		return nil, err
	}
	if actor == "" {
		actor = "human"
	}
	_ = s.store.AuditInsert(actor, "schedule.create", sc.ID,
		fmt.Sprintf("每 %s · →%s · %s", everyHuman(everySec), truncate(sessionID, 12), truncate(text, 100)), sc.ID)
	return sc, nil
}

func (s *Service) DeleteSchedule(id, actor string) error {
	sc, err := s.store.GetSchedule(id)
	if err != nil || sc == nil {
		return fmt.Errorf("定时任务不存在: %s", id)
	}
	if err := s.store.DeleteSchedule(id); err != nil {
		return err
	}
	if actor == "" {
		actor = "human"
	}
	_ = s.store.AuditInsert(actor, "schedule.delete", id, truncate(sc.Text, 100), id)
	return nil
}

// ToggleSchedule flips one switch (audited under the caller's actor).
func (s *Service) ToggleSchedule(id, actor string) (bool, error) {
	sc, err := s.store.GetSchedule(id)
	if err != nil || sc == nil {
		return false, fmt.Errorf("定时任务不存在: %s", id)
	}
	on := !sc.Enabled
	if err := s.store.ScheduleSetEnabled(id, on); err != nil {
		return false, err
	}
	if actor == "" {
		actor = "human"
	}
	_ = s.store.AuditInsert(actor, "schedule.toggle", id, "enabled="+strconv.FormatBool(on), id)
	return on, nil
}

// RunScheduleNow delivers one schedule immediately (manual trigger).
func (s *Service) RunScheduleNow(id, actor string) error {
	sc, err := s.store.GetSchedule(id)
	if err != nil || sc == nil {
		return fmt.Errorf("定时任务不存在: %s", id)
	}
	if actor == "" {
		actor = "human"
	}
	_ = s.store.AuditInsert(actor, "schedule.run_now", id, "立即投递一次", id)
	go s.deliver(sc, "manual")
	return nil
}

// deliver is the one and only action of the whole subsystem: put the
// sentence into the bound session. A vanished session disables the schedule
// instead of spamming the audit every tick.
func (s *Service) deliver(sc *Schedule, trigger string) {
	if sc.Session == "" {
		_ = s.store.AuditInsert("runner", "schedule.fail", sc.ID, "未绑定会话,已停用", sc.ID)
		_ = s.store.ScheduleSetEnabled(sc.ID, false)
		return
	}
	if sess, err := s.mgr.Get(sc.Session); err != nil || sess == nil {
		_ = s.store.AuditInsert("runner", "schedule.fail", sc.ID, "绑定会话已不存在,任务停用", sc.ID+","+sc.Session)
		_ = s.store.ScheduleSetEnabled(sc.ID, false)
		return
	}
	msg := fmt.Sprintf("【定时任务·%s】%s", sc.Name, sc.Text)
	if err := s.mgr.DeliverToSession(sc.Session, msg); err != nil {
		_ = s.store.AuditInsert("runner", "schedule.fail", sc.ID, "投递失败:"+truncate(err.Error(), 150), sc.ID+","+sc.Session)
		return
	}
	_ = s.store.ScheduleSetResult(sc.ID, time.Now().Format(time.RFC3339))
	_ = s.store.AuditInsert("runner", "schedule.fire", sc.ID,
		fmt.Sprintf("%s · 每 %s → %s", trigger, everyHuman(sc.EverySec), truncate(sc.Text, 100)), sc.ID+","+sc.Session)
}

// --- overview ---

type Overview struct {
	SuperSessionID  string      `json:"super_session_id"`
	Schedules       []*Schedule `json:"schedules"`
	Sessions        []*SessPick `json:"sessions"`
	Audits          []*AuditRow `json:"audits"`
	RunnerStartedAt string      `json:"runner_started_at"`
}

func (s *Service) Overview() *Overview {
	ov := &Overview{
		SuperSessionID:  s.SuperSessionID(),
		Schedules:       []*Schedule{},
		Sessions:        []*SessPick{},
		Audits:          []*AuditRow{},
		RunnerStartedAt: s.startedAt.Format(time.RFC3339),
	}
	if sc, err := s.store.ListSchedules(); err == nil {
		ov.Schedules = sc
	}
	if ss, err := s.store.ListBindableSessions(); err == nil {
		ov.Sessions = ss
	}
	if a, err := s.store.AuditRecent(80); err == nil {
		ov.Audits = a
	}
	return ov
}

// --- registration hook (implements session.SuperRegistrar) ---

// RegisterTools adds the lite surface to the super session: exactly one tool.
func (s *Service) RegisterTools(reg *tools.ToolRegistry, sessionID string) {
	reg.Register(newScheduleTool(s))
}

// --- helpers ---

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func everyHuman(sec int) string {
	switch {
	case sec >= 86400 && sec%86400 == 0:
		return fmt.Sprintf("%dd", sec/86400)
	case sec >= 3600 && sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec >= 60 && sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

var everyUnit = map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400}

// ParseEvery is the exported wrapper for the panel API ("30m" / "2h" / …).
func ParseEvery(v string) (int, error) { return parseEvery(v) }

// parseEvery accepts "90m" / "3h" / "2h30m" / "1d" / bare seconds and
// clamps into [30s, 30d]. Empty means the 1h default.
func parseEvery(v string) (int, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 3600, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < minEverySec {
			n = minEverySec
		}
		if n > maxEverySec {
			return 0, fmt.Errorf("every 超过 30 天上限: %s", v)
		}
		return n, nil
	}
	total, seen, num := 0, false, ""
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 's' || r == 'm' || r == 'h' || r == 'd':
			if num == "" {
				return 0, fmt.Errorf("every 格式不对: %s（单位前要带数字）", v)
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, fmt.Errorf("every 格式不对: %s", v)
			}
			total += n * everyUnit[string(r)]
			seen = true
			num = ""
		case r == ' ' || r == ',':
			if num != "" {
				n, err := strconv.Atoi(num)
				if err != nil {
					return 0, fmt.Errorf("every 格式不对: %s", v)
				}
				total += n
				num = ""
			}
		default:
			return 0, fmt.Errorf("every 格式不对: %s（只认 s/m/h/d）", v)
		}
	}
	if !seen {
		return 0, fmt.Errorf("every 格式不对: %s（例 3h / 90m / 2h30m）", v)
	}
	if total < minEverySec {
		total = minEverySec
	}
	if total > maxEverySec {
		return 0, fmt.Errorf("every 超过 30 天上限: %s", v)
	}
	return total, nil
}
