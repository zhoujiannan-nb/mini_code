package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
)

// maxConcurrentBg caps the number of simultaneously running background
// sub-agent jobs per parent session.
const maxConcurrentBg = 4

// finishedJobTTL is how long finished jobs are kept in memory (for
// task_output / task_list lookups) before they are pruned.
const finishedJobTTL = 2 * time.Hour

// SubagentJob is one dispatched sub-agent (foreground or background).
type SubagentJob struct {
	ID         string // = child session ID
	ParentID   string
	Role       string
	Goal       string
	Status     string // running | completed | failed | canceled
	Result     string
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{} // closed when the job settles
}

// SubagentService dispatches and tracks sub-agent jobs. It lives on the
// SessionManager (not on the TaskTool, which is recreated per turn), so
// background jobs survive across the parent's turns.
type SubagentService struct {
	mgr *SessionManager

	mu       sync.RWMutex
	jobs     map[string]*SubagentJob
	byParent map[string][]string

	// OnJobStart / OnJobEnd let the runtime register a background job's
	// cancel with the message hub (so the hub's task registry, the UI's
	// running indicator and the stop button all work for sub-agents, and
	// canceling the parent cascades to its children).
	OnJobStart func(childSessionID string, cancel context.CancelFunc)
	OnJobEnd   func(childSessionID string)

	// OnJobDone is called when a job's Prompt returns (success, failure or
	// interrupt). The runtime wires it to emit the terminal assistant /
	// status messages into the hub so a sub-agent's own session view
	// terminates cleanly (live deltas alone never end the stream).
	OnJobDone func(sessionID, content, reasoning string, interrupted bool)

	// PromptFunc runs one sub-agent task; overridable in tests.
	PromptFunc func(ctx context.Context, s *Session, goal string) (content string, reasoning string, err error)
}

func NewSubagentService(mgr *SessionManager) *SubagentService {
	return &SubagentService{
		mgr:      mgr,
		jobs:     make(map[string]*SubagentJob),
		byParent: make(map[string][]string),
	}
}

// JobInfo is the exported, JSON-friendly view of a job (used by the task_*
// tools and the web API).
type JobInfo = tools.JobInfo

// prepare creates the child session for a dispatch: same working directory
// as the parent, the parent's permission mode snapshotted, and — when fork
// is set — the parent's conversation history (minus its system message)
// copied in as the child's starting context.
func (s *SubagentService) prepare(parentID, role, goal string, fork bool) (*Session, error) {
	parent, err := s.mgr.Get(parentID)
	if err != nil || parent == nil {
		return nil, fmt.Errorf("parent session not found: %s", parentID)
	}
	title := goal
	if r := []rune(title); len(r) > 50 {
		title = string(r[:50]) + "…"
	}
	child, err := s.mgr.CreateSessionRecord(title, role, parent.WorkDir(), &parentID)
	if err != nil {
		return nil, err
	}
	// Permission inheritance: the child snapshots the parent's current
	// mode, so a cautious parent produces cautious children (their
	// destructive operations also need UI approval).
	if mode := s.mgr.ModeOf(parentID); mode != ModeFull {
		if err := s.mgr.SetMode(child.ID(), mode); err != nil {
			slog.Warn("snapshot child mode failed", "child", child.ID(), "err", err)
		}
	}
	// Model inheritance: the child snapshots the parent's model, so
	// sub-agent work runs on the same model the parent session uses
	// (an empty parent key means the default model — nothing to set).
	if mk := parent.ModelKey(); mk != "" {
		if err := s.mgr.SetModel(child.ID(), mk); err != nil {
			slog.Warn("snapshot child model failed", "child", child.ID(), "model", mk, "err", err)
		}
	}
	// Thinking-depth inheritance: the child snapshots the parent's per-session
	// override so sub-agent work thinks at the same depth (empty = model
	// default, nothing to set).
	if thinking, effort := parent.ReasoningOverride(); thinking != "" || effort != "" {
		child.SetReasoningOverride(thinking, effort)
	}
	if fork {
		hist := s.mgr.CopyMessages(parentID)
		if len(hist) > 0 && hist[0].Role == "system" {
			hist = hist[1:] // the child gets its own role's system prompt
		}
		child.Record().Messages = append([]provider.Message{}, hist...)
		if err := s.mgr.GetStore().Update(child.Record()); err != nil {
			slog.Warn("persist forked history failed", "child", child.ID(), "err", err)
		}
	}
	return child, nil
}

func (s *SubagentService) registerJob(job *SubagentJob) {
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.byParent[job.ParentID] = append(s.byParent[job.ParentID], job.ID)
	s.mu.Unlock()
}

func (s *SubagentService) job(id string) (*SubagentJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

// pruneFinished drops finished jobs older than finishedJobTTL.
func (s *SubagentService) pruneFinished() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.Lock()
		finished := j.Status != "running" && now.Sub(j.FinishedAt) > finishedJobTTL
		j.mu.Unlock()
		if !finished {
			continue
		}
		delete(s.jobs, id)
		if kids, ok := s.byParent[j.ParentID]; ok {
			out := kids[:0]
			for _, k := range kids {
				if k != id {
					out = append(out, k)
				}
			}
			s.byParent[j.ParentID] = out
		}
	}
}

// DispatchSync runs a sub-agent in the foreground: it blocks (on the
// caller's context) until the child finishes and returns its final reply.
func (s *SubagentService) DispatchSync(ctx context.Context, parentID, role, goal string, fork bool) (string, string, error) {
	child, err := s.prepare(parentID, role, goal, fork)
	if err != nil {
		return "", "", err
	}
	job := &SubagentJob{
		ID: child.ID(), ParentID: parentID, Role: role, Goal: goal,
		Status: "running", StartedAt: time.Now(), done: make(chan struct{}),
	}
	s.registerJob(job)
	slog.Info("sub-agent dispatched (foreground)", "child", child.ID(), "parent", parentID, "role", role, "fork", fork)

	content, reasoning, err := s.runChild(ctx, child, goal)
	s.settleJob(job, child, content, reasoning, err, false)
	return content, child.ID(), err
}

// DispatchBg starts a sub-agent in the background and returns immediately
// with the job (child session) ID. A settlement notice is injected into
// the parent's conversation when the job finishes.
func (s *SubagentService) DispatchBg(parentID, role, goal string, fork bool) (string, error) {
	s.pruneFinished()
	s.mu.RLock()
	running := 0
	for _, id := range s.byParent[parentID] {
		if j, ok := s.jobs[id]; ok {
			j.mu.Lock()
			isRunning := j.Status == "running"
			j.mu.Unlock()
			if isRunning {
				running++
			}
		}
	}
	s.mu.RUnlock()
	if running >= maxConcurrentBg {
		return "", fmt.Errorf("too many concurrent background sub-agents (%d); wait for some to finish (task_list) or cancel one (task_cancel)", maxConcurrentBg)
	}

	child, err := s.prepare(parentID, role, goal, fork)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &SubagentJob{
		ID: child.ID(), ParentID: parentID, Role: role, Goal: goal,
		Status: "running", StartedAt: time.Now(), cancel: cancel, done: make(chan struct{}),
	}
	s.registerJob(job)
	if s.OnJobStart != nil {
		s.OnJobStart(child.ID(), cancel)
	}
	slog.Info("sub-agent dispatched (background)", "child", child.ID(), "parent", parentID, "role", role, "fork", fork)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background sub-agent panicked, contained", "child", child.ID(), "panic", r)
				s.settleJob(job, child, "", "", fmt.Errorf("sub-agent crashed (panic: %v)", r), false)
			}
		}()
		content, reasoning, err := s.runChild(ctx, child, goal)
		s.settleJob(job, child, content, reasoning, err, true)
	}()
	return child.ID(), nil
}

// runChild executes the sub-agent's Prompt (injectable for tests).
func (s *SubagentService) runChild(ctx context.Context, child *Session, goal string) (string, string, error) {
	if s.PromptFunc != nil {
		return s.PromptFunc(ctx, child, goal)
	}
	return child.Prompt(ctx, goal, nil, 999)
}

// settleJob finalizes a job: status, terminal hub events, and — for
// background jobs — the settlement notice in the parent's conversation.
func (s *SubagentService) settleJob(job *SubagentJob, child *Session, content, reasoning string, err error, background bool) {
	interrupted := child.Status() == "interrupted"
	job.mu.Lock()
	switch {
	case err != nil:
		job.Status = "failed"
		job.Err = err.Error()
	case interrupted:
		job.Status = "canceled"
		job.Err = "canceled"
	default:
		job.Status = "completed"
		job.Result = content
	}
	job.FinishedAt = time.Now()
	job.mu.Unlock()
	close(job.done)

	if s.OnJobEnd != nil {
		s.OnJobEnd(child.ID())
	}
	if s.OnJobDone != nil {
		s.OnJobDone(child.ID(), content, reasoning, interrupted)
	}
	slog.Info("sub-agent settled", "child", child.ID(), "status", job.Status)

	if background {
		if notice := s.settleNotice(job); notice != "" {
			if e := s.mgr.InjectMessage(job.ParentID, notice); e != nil {
				slog.Warn("settlement notice failed", "parent", job.ParentID, "err", e)
			}
		}
	}
}

// settleNotice renders the settlement message injected into the parent's
// conversation when a background job finishes.
func (s *SubagentService) settleNotice(job *SubagentJob) string {
	id8 := job.ID
	if len(id8) > 8 {
		id8 = id8[:8]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Subagent %s %s] role=%s\nGoal: %q\n", id8, job.Status, job.Role, truncateRunes(job.Goal, 160))
	switch job.Status {
	case "completed":
		if job.Result == "" {
			b.WriteString("Result: (no output)\n")
		} else {
			fmt.Fprintf(&b, "Result (truncated to 800 chars):\n%s\n", truncateRunes(job.Result, 800))
		}
		b.WriteString("Use task_output with this id if you need the full result.")
	case "failed":
		fmt.Fprintf(&b, "Error: %s", job.Err)
	case "canceled":
		b.WriteString("The sub-agent was canceled before finishing.")
	}
	return b.String()
}

// Output returns a job's result. With wait=true it blocks (on ctx) until
// the job settles or waitSeconds elapses.
func (s *SubagentService) Output(ctx context.Context, id string, wait bool, waitSeconds int) (string, error) {
	job, ok := s.job(id)
	if !ok {
		return "", fmt.Errorf("unknown sub-agent job %q (use task_list to see known jobs)", id)
	}
	if wait {
		if waitSeconds <= 0 {
			waitSeconds = 60
		}
		if waitSeconds > 300 {
			waitSeconds = 300
		}
		select {
		case <-job.done:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(waitSeconds) * time.Second):
			return "", fmt.Errorf("sub-agent %s still running after %ds; call task_output again to keep waiting", id, waitSeconds)
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	switch job.Status {
	case "running":
		return "", fmt.Errorf("sub-agent %s is still running", id)
	case "completed":
		if job.Result == "" {
			return "(no output)", nil
		}
		return job.Result, nil
	case "failed":
		return "", fmt.Errorf("sub-agent %s failed: %s", id, job.Err)
	case "canceled":
		return "", fmt.Errorf("sub-agent %s was canceled", id)
	}
	return "", fmt.Errorf("sub-agent %s in unknown state %q", id, job.Status)
}

// Status returns the current state of one job.
func (s *SubagentService) Status(id string) (tools.JobInfo, bool) {
	job, ok := s.job(id)
	if !ok {
		return tools.JobInfo{}, false
	}
	return jobInfo(job), true
}

// List returns the known jobs of one parent (running + recently finished).
func (s *SubagentService) List(parentID string) []tools.JobInfo {
	s.pruneFinished()
	s.mu.RLock()
	ids := append([]string{}, s.byParent[parentID]...)
	s.mu.RUnlock()
	out := make([]tools.JobInfo, 0, len(ids))
	for _, id := range ids {
		if j, ok := s.job(id); ok {
			out = append(out, jobInfo(j))
		}
	}
	return out
}

// Cancel stops a running job. It reports whether a running job was found.
func (s *SubagentService) Cancel(id string) bool {
	job, ok := s.job(id)
	if !ok {
		return false
	}
	job.mu.Lock()
	running := job.Status == "running"
	cancel := job.cancel
	job.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	slog.Info("cancelling sub-agent job", "job", id)
	cancel()
	return true
}

// CancelAll stops every running job of a parent ("父取消,大家一起死").
// It returns the number of jobs canceled.
func (s *SubagentService) CancelAll(parentID string) int {
	s.mu.RLock()
	ids := append([]string{}, s.byParent[parentID]...)
	s.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if s.Cancel(id) {
			n++
		}
	}
	return n
}

func jobInfo(j *SubagentJob) tools.JobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	info := tools.JobInfo{
		ID:        j.ID,
		Role:      j.Role,
		Goal:      j.Goal,
		Status:    j.Status,
		Result:    j.Result,
		Error:     j.Err,
		StartedAt: j.StartedAt.Format("2006-01-02 15:04:05"),
	}
	if !j.FinishedAt.IsZero() {
		info.FinishedAt = j.FinishedAt.Format("2006-01-02 15:04:05")
	}
	return info
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
