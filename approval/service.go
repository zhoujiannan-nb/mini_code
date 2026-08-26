// Package approval implements the cautious-mode approval flow: a
// destructive tool call is held by the runtime gate, this service pushes
// the request to the UI over the message hub and blocks until the user
// approves, denies, the task is canceled, or the request times out
// (auto-deny).
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

// DefaultTimeout is how long an approval request waits for a decision
// before it is auto-denied.
const DefaultTimeout = 5 * time.Minute

type pending struct {
	id       string
	deadline time.Time
	payload  channels.ApprovalRequestPayload
	done     chan channels.ApprovalResolvedPayload
}

// Service holds the pending approval requests and emits their lifecycle
// events into the message hub. It implements session.Approver.
type Service struct {
	hub     *channels.Hub
	mgr     *session.SessionManager
	timeout time.Duration

	mu   sync.Mutex
	reqs map[string]*pending
}

// NewService creates the approval service bound to the hub and session
// manager (the manager resolves parent-session titles for sub-agent
// requests).
func NewService(hub *channels.Hub, mgr *session.SessionManager) *Service {
	return &Service{
		hub:     hub,
		mgr:     mgr,
		timeout: DefaultTimeout,
		reqs:    make(map[string]*pending),
	}
}

// SetTimeout overrides the approval timeout (tests).
func (s *Service) SetTimeout(d time.Duration) {
	if d > 0 {
		s.timeout = d
	}
}

// RequestApproval pushes the request to the UI and blocks until it is
// decided, the task context is canceled, or the timeout elapses (auto-deny).
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

// settle records a decision that did not come from the user (timeout /
// cancel) so a late UI click is ignored.
func (s *Service) settle(p *pending, d channels.ApprovalResolvedPayload) {
	select {
	case p.done <- d:
	default:
	}
}

// Resolve applies a user decision to a pending request. It reports whether
// the request existed and was still open.
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
		return channels.ApprovalResolvedPayload{}, false // already settled (timeout/cancel)
	}
}

// PendingInfo is the JSON view of one open request (GET /api/approvals).
type PendingInfo = channels.ApprovalRequestPayload

// Pending lists the open requests (UI sync after a reload).
func (s *Service) Pending() []PendingInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingInfo, 0, len(s.reqs))
	for _, p := range s.reqs {
		out = append(out, p.payload)
	}
	return out
}
