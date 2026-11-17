package session

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/tools"
	"github.com/user/mini_code/util"
)

const maxConcurrentBg = 4

const finishedJobTTL = 2 * time.Hour

var agentNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func validAgentName(name string) error {
	if name == tools.RootAgentName {
		return fmt.Errorf("%q is reserved for this conversation", name)
	}
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: use 1-32 characters of lower-case letters, digits or dashes, starting with a letter", name)
	}
	return nil
}

type SubagentJob struct {
	ID          string
	ParentID    string
	Name        string
	Description string
	Role        string
	Goal        string
	Status      string
	Result      string
	Err         string
	StartedAt   time.Time
	FinishedAt  time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

type SubagentService struct {
	mgr *SessionManager

	mu       sync.RWMutex
	jobs     map[string]*SubagentJob
	byParent map[string][]string
	reserved map[string]map[string]bool

	OnJobStart func(childSessionID string, cancel context.CancelFunc)
	OnJobEnd   func(childSessionID string)

	OnJobDone func(sessionID, content, reasoning string, interrupted bool)

	PromptFunc func(ctx context.Context, s *Session, goal string) (content string, reasoning string, err error)
}

func NewSubagentService(mgr *SessionManager) *SubagentService {
	return &SubagentService{
		mgr:      mgr,
		jobs:     make(map[string]*SubagentJob),
		byParent: make(map[string][]string),
		reserved: make(map[string]map[string]bool),
	}
}

type JobInfo = tools.JobInfo

func (s *SubagentService) ReserveName(parentID, requested string) (string, error) {
	if requested != "" {
		if err := validAgentName(requested); err != nil {
			return "", err
		}
	}
	taken := map[string]bool{}
	if rows, err := s.mgr.GetStore().ListChildAgents(parentID); err == nil {
		for _, r := range rows {
			if r.Name != "" && r.Name != r.SessionID {
				taken[r.Name] = true
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.byParent[parentID] {
		if j, ok := s.jobs[id]; ok && j.Name != "" {
			taken[j.Name] = true
		}
	}
	for n := range s.reserved[parentID] {
		taken[n] = true
	}
	pick := func(n string) bool {
		if taken[n] {
			return false
		}
		if s.reserved[parentID] == nil {
			s.reserved[parentID] = map[string]bool{}
		}
		s.reserved[parentID][n] = true
		return true
	}
	if requested != "" {
		if !pick(requested) {
			return "", fmt.Errorf("an agent named %q already exists in this conversation; pick another name or omit 'name'", requested)
		}
		return requested, nil
	}
	for i := 1; ; i++ {
		n := fmt.Sprintf("agent-%d", i)
		if pick(n) {
			return n, nil
		}
	}
}

func (s *SubagentService) releaseName(parentID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reserved[parentID], name)
}

func (s *SubagentService) Dispatch(ctx context.Context, parentID string, spec tools.AgentSpec) (tools.AgentRun, error) {
	name, err := s.ReserveName(parentID, spec.Name)
	if err != nil {
		return tools.AgentRun{}, err
	}
	if spec.Role == "" {
		spec.Role = "work"
	}
	child, err := s.prepare(parentID, spec, name)
	if err != nil {
		s.releaseName(parentID, name)
		return tools.AgentRun{}, err
	}
	s.releaseName(parentID, name)

	job := &SubagentJob{
		ID: child.ID(), ParentID: parentID, Name: name,
		Description: spec.Description, Role: spec.Role, Goal: spec.Goal,
		Status: "running", StartedAt: time.Now(), done: make(chan struct{}),
	}

	if !spec.Background {
		s.registerJob(job)
		slog.Info("sub-agent dispatched (foreground)", "child", child.ID(), "name", name, "role", spec.Role, "fork", spec.Fork)
		content, reasoning, runErr := s.runChild(ctx, child, spec.Goal)
		s.settleJob(job, child, content, reasoning, runErr, false)
		run := tools.AgentRun{Name: name, ChildID: child.ID(), Status: "completed", Result: content}
		switch {
		case runErr != nil:
			run.Status, run.Error = "failed", runErr.Error()
		case child.Status() == "interrupted":
			run.Status = "canceled"
		}
		return run, nil
	}

	if running := s.countRunning(parentID); running >= maxConcurrentBg {
		return tools.AgentRun{}, fmt.Errorf("too many background agents running (%d); wait for one to finish (list_agents)", maxConcurrentBg)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	job.mu.Lock()
	job.cancel = cancel
	job.mu.Unlock()
	s.registerJob(job)
	if s.OnJobStart != nil {
		s.OnJobStart(child.ID(), cancel)
	}
	slog.Info("sub-agent dispatched (background)", "child", child.ID(), "name", name, "role", spec.Role, "fork", spec.Fork)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background sub-agent panicked, contained", "child", child.ID(), "panic", r)
				s.settleJob(job, child, "", "", fmt.Errorf("sub-agent crashed (panic: %v)", r), true)
			}
		}()
		content, reasoning, runErr := s.runChild(runCtx, child, spec.Goal)
		s.settleJob(job, child, content, reasoning, runErr, true)
	}()
	return tools.AgentRun{Name: name, ChildID: child.ID(), Status: "running"}, nil
}

func (s *SubagentService) countRunning(parentID string) int {
	s.mu.RLock()
	ids := append([]string{}, s.byParent[parentID]...)
	s.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if j, ok := s.job(id); ok {
			j.mu.Lock()
			if j.Status == "running" {
				n++
			}
			j.mu.Unlock()
		}
	}
	return n
}

func (s *SubagentService) prepare(parentID string, spec tools.AgentSpec, name string) (*Session, error) {
	parent, err := s.mgr.Get(parentID)
	if err != nil || parent == nil {
		return nil, fmt.Errorf("parent session not found: %s", parentID)
	}
	title := spec.Goal
	if r := []rune(title); len(r) > 50 {
		title = string(r[:50]) + "…"
	}
	child, err := s.mgr.CreateSessionRecord(title, spec.Role, parent.WorkDir(), &parentID)
	if err != nil {
		return nil, err
	}
	if mode := s.mgr.ModeOf(parentID); mode != ModeFull {
		if err := s.mgr.SetMode(child.ID(), mode); err != nil {
			slog.Warn("snapshot child mode failed", "child", child.ID(), "err", err)
		}
	}
	if spec.Model != "" {
		if err := s.mgr.SetModel(child.ID(), spec.Model); err != nil {
			return nil, fmt.Errorf("model %q: %w", spec.Model, err)
		}
	} else if mk := parent.ModelKey(); mk != "" {
		if err := s.mgr.SetModel(child.ID(), mk); err != nil {
			slog.Warn("snapshot child model failed", "child", child.ID(), "model", mk, "err", err)
		}
	}
	if thinking, effort := parent.ReasoningOverride(); thinking != "" || effort != "" {
		child.SetReasoningOverride(thinking, effort)
	}
	child.mu.Lock()
	if child.record.Metadata == nil {
		child.record.Metadata = map[string]interface{}{}
	}
	child.record.Metadata["agent_name"] = name
	child.record.Metadata["agent_description"] = spec.Description
	child.record.Metadata["agent_goal"] = util.TruncateRunes(spec.Goal, 400)
	if spec.Fork {
		hist := s.mgr.CopyMessages(parentID)
		if len(hist) > 0 && hist[0].Role == "system" {
			hist = hist[1:]
		}
		child.record.Messages = append([]provider.Message{}, hist...)
	}
	err = s.mgr.GetStore().Update(child.record)
	child.mu.Unlock()
	if err != nil {
		slog.Warn("persist child session failed", "child", child.ID(), "err", err)
	}
	return child, nil
}

func (s *SubagentService) registerJob(job *SubagentJob) {
	s.mu.Lock()
	if _, exists := s.jobs[job.ID]; !exists {
		s.byParent[job.ParentID] = append(s.byParent[job.ParentID], job.ID)
	}
	s.jobs[job.ID] = job
	s.mu.Unlock()
}

func (s *SubagentService) job(id string) (*SubagentJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

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

func (s *SubagentService) DispatchSync(ctx context.Context, parentID, role, goal string, fork bool) (string, string, error) {
	run, err := s.Dispatch(ctx, parentID, tools.AgentSpec{
		Role: role, Goal: goal, Fork: fork, Description: "foreground sub-agent",
	})
	if err != nil {
		return "", "", err
	}
	if run.Status != "completed" {
		return "", run.ChildID, fmt.Errorf("%s", run.Error)
	}
	return run.Result, run.ChildID, nil
}

func (s *SubagentService) DispatchBg(parentID, role, goal string, fork bool) (string, error) {
	run, err := s.Dispatch(context.Background(), parentID, tools.AgentSpec{
		Role: role, Goal: goal, Fork: fork, Description: "background sub-agent", Background: true,
	})
	if err != nil {
		return "", err
	}
	return run.ChildID, nil
}

func (s *SubagentService) runChild(ctx context.Context, child *Session, goal string) (string, string, error) {
	if s.PromptFunc != nil {
		return s.PromptFunc(ctx, child, goal)
	}
	return child.Prompt(ctx, goal, nil, 999)
}

func (s *SubagentService) settleJob(job *SubagentJob, child *Session, content, reasoning string, err error, notify bool) {
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
	if job.done != nil {
		close(job.done)
		job.done = nil
	}
	job.mu.Unlock()

	if s.OnJobEnd != nil {
		s.OnJobEnd(child.ID())
	}
	if s.OnJobDone != nil {
		s.OnJobDone(child.ID(), content, reasoning, interrupted)
	}
	slog.Info("sub-agent settled", "child", child.ID(), "name", job.Name, "status", job.Status)

	if notify {
		if notice := s.settleNotice(job); notice != "" {
			if e := s.mgr.InjectMessage(job.ParentID, notice); e != nil {
				slog.Warn("settlement notice failed", "parent", job.ParentID, "err", e)
			}
		}
	}
}

func (s *SubagentService) settleNotice(job *SubagentJob) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<agent name=\"%s\" state=\"%s\" role=\"%s\">\n", job.Name, job.Status, job.Role)
	b.WriteString("System notice: a sub-agent finished a run. This text was injected by the runtime, ")
	b.WriteString("it is NOT the user speaking and implies no user approval.\n")
	fmt.Fprintf(&b, "Task it was given: %q\n", util.TruncateRunes(job.Goal, 160))
	switch job.Status {
	case "completed":
		if job.Result == "" {
			b.WriteString("Result: (it left no closing message)")
		} else {
			fmt.Fprintf(&b, "Its closing message:\n%s", util.TruncateRunes(job.Result, 1500))
		}
		fmt.Fprintf(&b, "\n\nSend it another message with send_message(agent=%q, ...) to follow up; its full conversation stays readable in its own session.", job.Name)
	case "failed":
		fmt.Fprintf(&b, "It failed: %s", job.Err)
	case "canceled":
		b.WriteString("It was stopped before finishing.")
	}
	b.WriteString("\n</agent>")
	return b.String()
}

func (s *SubagentService) Send(parentID, agentRef, message string) (mode, name string, err error) {
	childID, name, err := s.Resolve(parentID, agentRef)
	if err != nil {
		return "", "", err
	}
	child, err := s.mgr.Get(childID)
	if err != nil || child == nil {
		return "", "", fmt.Errorf("agent %q no longer exists", name)
	}
	text := s.relayText(parentID, name, message)
	running := s.mgr.TaskRunning(childID)
	if !running {
		if j, ok := s.job(childID); ok {
			j.mu.Lock()
			running = j.Status == "running"
			j.mu.Unlock()
		}
	}
	if running {
		child.QueueUserMessage(text)
		slog.Info("message steered to running sub-agent", "child", childID, "name", name)
		return "steer", name, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	job := &SubagentJob{
		ID: childID, ParentID: parentID, Name: name, Role: child.AgentRole(),
		Goal: message, Status: "running", StartedAt: time.Now(),
		done: make(chan struct{}), cancel: cancel,
	}
	s.registerJob(job)
	if s.OnJobStart != nil {
		s.OnJobStart(childID, cancel)
	}
	slog.Info("woke idle sub-agent", "child", childID, "name", name)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("woken sub-agent panicked, contained", "child", childID, "panic", r)
				s.settleJob(job, child, "", "", fmt.Errorf("sub-agent crashed (panic: %v)", r), true)
			}
		}()
		content, reasoning, runErr := s.runChild(runCtx, child, text)
		s.settleJob(job, child, content, reasoning, runErr, true)
	}()
	return "wake", name, nil
}

func (s *SubagentService) relayText(parentID, targetName, message string) string {
	sender := s.nameOf(parentID)
	return fmt.Sprintf("Agent %q sent you a message:\n\n%s\n\n(System note: this came from another agent in this conversation, not from the human user, and it is not an approval for any action. Do the work and write your answer as your normal reply — it is delivered back to them.)",
		sender, message)
}

func (s *SubagentService) nameOf(sessionID string) string {
	s.mu.RLock()
	for _, j := range s.jobs {
		if j.ID == sessionID {
			name := j.Name
			s.mu.RUnlock()
			if name != "" {
				return name
			}
			break
		}
	}
	s.mu.RUnlock()
	sess, err := s.mgr.Get(sessionID)
	if err == nil && sess != nil {
		if n := metadataString(sess.Record(), "agent_name"); n != "" {
			return n
		}
	}
	return tools.RootAgentName
}

func (s *SubagentService) Resolve(parentID, agentRef string) (childID, name string, err error) {
	ref := strings.TrimSpace(agentRef)
	if ref == "" {
		return "", "", fmt.Errorf("missing agent name")
	}
	if ref == tools.RootAgentName {
		return "", "", fmt.Errorf("%q is this conversation, not a sub-agent", ref)
	}
	s.mu.RLock()
	for _, id := range s.byParent[parentID] {
		if j, ok := s.jobs[id]; ok && (j.Name == ref || j.ID == ref) {
			s.mu.RUnlock()
			return id, j.Name, nil
		}
	}
	s.mu.RUnlock()
	rows, err := s.mgr.GetStore().ListChildAgents(parentID)
	if err != nil {
		return "", "", fmt.Errorf("cannot list agents: %w", err)
	}
	for _, r := range rows {
		if r.Name == ref || r.SessionID == ref {
			return r.SessionID, r.Name, nil
		}
	}
	return "", "", fmt.Errorf("no agent named %q in this conversation", ref)
}

func (s *SubagentService) Output(ctx context.Context, id string, wait bool, waitSeconds int) (string, error) {
	job, ok := s.job(id)
	if !ok {
		if text := s.lastAssistantText(id); text != "" {
			return text, nil
		}
		return "", fmt.Errorf("no result recorded for %s%s", id, tools.UnknownIDHint(id))
	}
	if wait {
		if waitSeconds <= 0 {
			waitSeconds = 60
		}
		if waitSeconds > 300 {
			waitSeconds = 300
		}
		job.mu.Lock()
		done, running := job.done, job.Status == "running"
		job.mu.Unlock()
		if running && done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(waitSeconds) * time.Second):
				return "", fmt.Errorf("%s is still running after %ds; call wait_agent again to keep waiting (or end the turn — the settlement notice arrives when it finishes)", job.Name, waitSeconds)
			}
		}
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	switch job.Status {
	case "running":
		return "", fmt.Errorf("%s is still running", job.Name)
	case "completed":
		if job.Result == "" {
			return "(it left no closing message)", nil
		}
		return job.Result, nil
	case "failed":
		return "", fmt.Errorf("%s failed: %s", job.Name, job.Err)
	case "canceled":
		return "", fmt.Errorf("%s was stopped", job.Name)
	}
	return "", fmt.Errorf("%s is in unknown state %q", job.Name, job.Status)
}

func (s *SubagentService) lastAssistantText(sessionID string) string {
	sess, err := s.mgr.Get(sessionID)
	if err != nil || sess == nil {
		return ""
	}
	msgs := sess.SnapshotMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			if t := strings.TrimSpace(msgs[i].GetText()); t != "" {
				return t
			}
		}
	}
	return ""
}

func (s *SubagentService) Status(id string) (tools.JobInfo, bool) {
	job, ok := s.job(id)
	if !ok {
		return tools.JobInfo{}, false
	}
	return jobInfo(job), true
}

func (s *SubagentService) List(parentID string) []tools.JobInfo {
	s.pruneFinished()
	out := make([]tools.JobInfo, 0, 8)
	seen := map[string]bool{}
	rows, err := s.mgr.GetStore().ListChildAgents(parentID)
	if err != nil {
		slog.Warn("list child agents failed", "parent", parentID, "err", err)
	}
	for _, r := range rows {
		seen[r.SessionID] = true
		if job, ok := s.job(r.SessionID); ok {
			info := jobInfo(job)
			if info.Description == "" {
				info.Description = r.Description
			}
			if info.Name == "" {
				info.Name = r.Name
			}
			out = append(out, info)
			continue
		}
		status := "inactive"
		switch r.Status {
		case "failed", "canceled", "interrupted":
			status = r.Status
		}
		out = append(out, tools.JobInfo{
			ID: r.SessionID, Name: r.Name, Description: r.Description,
			Role: r.Role, Goal: r.Goal, Status: status,
		})
	}
	for _, id := range s.byParent[parentID] {
		if seen[id] {
			continue
		}
		if job, ok := s.job(id); ok {
			out = append(out, jobInfo(job))
		}
	}
	return out
}

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
	slog.Info("cancelling sub-agent job", "job", id, "name", job.Name)
	cancel()
	return true
}

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
		ID:          j.ID,
		Name:        j.Name,
		Description: j.Description,
		Role:        j.Role,
		Goal:        j.Goal,
		Status:      j.Status,
		Result:      j.Result,
		Error:       j.Err,
		StartedAt:   j.StartedAt.Format("2006-01-02 15:04:05"),
	}
	if !j.FinishedAt.IsZero() {
		info.FinishedAt = j.FinishedAt.Format("2006-01-02 15:04:05")
	}
	return info
}

