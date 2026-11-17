package approval

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/user/mini_code/channels"
	"github.com/user/mini_code/session"
	"github.com/user/mini_code/util"
)

const DefaultTimeout = 5 * time.Minute

type pending struct {
	id       string
	deadline time.Time
	payload  channels.ApprovalRequestPayload
	done     chan channels.ApprovalResolvedPayload
}

type Service struct {
	hub     *channels.Hub
	mgr     *session.SessionManager
	timeout time.Duration

	mu   sync.Mutex
	reqs map[string]*pending
}

func NewService(hub *channels.Hub, mgr *session.SessionManager) *Service {
	return &Service{
		hub:     hub,
		mgr:     mgr,
		timeout: DefaultTimeout,
		reqs:    make(map[string]*pending),
	}
}

func (s *Service) SetTimeout(d time.Duration) {
	if d > 0 {
		s.timeout = d
	}
}

func (s *Service) RequestApproval(ctx context.Context, req session.ApprovalRequest) session.ApprovalDecision {
	id := util.RandomHex(12)
	deadline := time.Now().Add(s.timeout)
	parentTitle := ""
	if req.ParentID != "" {
		if ps, err := s.mgr.Get(req.ParentID); err == nil && ps != nil {
			parentTitle = ps.Title()
		}
	}
	payload := channels.ApprovalRequestPayload{
		ID:          id,
		SessionID:   req.SessionID,
		ParentID:    req.ParentID,
		Title:       req.SessionTitle,
		ParentTitle: parentTitle,
		ToolName:    req.ToolName,
		Args:        req.ArgsJSON,
		Deadline:    deadline,
	}
	p := &pending{
		id:      id,
		deadline: deadline,
		payload: payload,
		done:    make(chan channels.ApprovalResolvedPayload, 1),
	}
	s.mu.Lock()
	s.reqs[id] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.reqs, id)
		s.mu.Unlock()
	}()

	s.hub.Submit(channels.NewApprovalRequestMessage(payload))
	slog.Info("approval requested", "id", id, "session", req.SessionID, "tool", req.ToolName)

	var resolved channels.ApprovalResolvedPayload
	select {
	case resolved = <-p.done:
	case <-ctx.Done():
		resolved = channels.ApprovalResolvedPayload{ID: id, Approved: false, Reason: "task canceled"}
		s.settle(p, resolved)
	case <-time.After(time.Until(deadline)):
		resolved = channels.ApprovalResolvedPayload{ID: id, Approved: false, Reason: "approval timed out"}
		s.settle(p, resolved)
	}
	s.hub.Submit(channels.NewApprovalResolvedMessage(resolved))
	slog.Info("approval resolved", "id", id, "approved", resolved.Approved, "reason", resolved.Reason)
	return session.ApprovalDecision{Approved: resolved.Approved, Reason: resolved.Reason}
}

func (s *Service) settle(p *pending, d channels.ApprovalResolvedPayload) {
	select {
	case p.done <- d:
	default:
	}
}

func (s *Service) Resolve(id string, approved bool) (channels.ApprovalResolvedPayload, bool) {
	s.mu.Lock()
	p, ok := s.reqs[id]
	s.mu.Unlock()
	if !ok {
		return channels.ApprovalResolvedPayload{}, false
	}
	reason := "user approved"
	if !approved {
		reason = "user denied"
	}
	d := channels.ApprovalResolvedPayload{ID: id, Approved: approved, Reason: reason}
	select {
	case p.done <- d:
		return d, true
	default:
		return channels.ApprovalResolvedPayload{}, false
	}
}

type PendingInfo = channels.ApprovalRequestPayload

func (s *Service) Pending() []PendingInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingInfo, 0, len(s.reqs))
	for _, p := range s.reqs {
		out = append(out, p.payload)
	}
	return out
}
