package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusRunning Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed Status = "failed"
	StatusKilled Status = "killed"
	StatusLost Status = "lost"
)

const maxRingBytes = 64 << 10

const (
	EventStart   = "start"
	EventPromote = "promote"
	EventSettle  = "settle"
)

type Job struct {
	ID    string
	Owner string
	Kind  string
	Label string
	Background bool

	mu         sync.Mutex
	status     Status
	detail     string
	ring       []byte
	dropped    int64
	total      int64
	unread     int64
	started    time.Time
	ended      time.Time
	promotedAt time.Time
	cancel     func() Status
	settled    chan struct{}
	awhield    bool
	waiters    int
	promoted   bool
	final      string
	listener   func(*Job, string)
}

func newJob(id, owner, kind, label string, listener func(*Job, string)) *Job {
	return &Job{
		ID: id, Owner: owner, Kind: kind, Label: label,
		status:   StatusRunning,
		started:  time.Now(),
		settled:  make(chan struct{}),
		listener: listener,
	}
}

func (j *Job) SetCancel(cancel func() Status) {
	j.mu.Lock()
	j.cancel = cancel
	j.mu.Unlock()
}

func (j *Job) SetFinal(text string) {
	j.mu.Lock()
	j.final = text
	j.mu.Unlock()
}

func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != StatusRunning {
		j.total += int64(len(p))
		return len(p), nil
	}
	j.total += int64(len(p))
	j.unread += int64(len(p))
	j.ring = append(j.ring, p...)
	if over := len(j.ring) - maxRingBytes; over > 0 {
		j.ring = append(j.ring[:0], j.ring[over:]...)
		j.dropped += int64(over)
	}
	return len(p), nil
}

func (j *Job) Settle(status Status, detail string) {
	j.mu.Lock()
	if j.status != StatusRunning {
		j.mu.Unlock()
		return
	}
	j.status, j.detail, j.ended = status, detail, time.Now()
	if j.waiters > 0 {
		j.awhield = true
	}
	l := j.listener
	close(j.settled)
	j.mu.Unlock()
	if l != nil {
		l(j, EventSettle)
	}
}

func (j *Job) Promote() bool {
	j.mu.Lock()
	if j.status != StatusRunning || j.promoted {
		j.mu.Unlock()
		return false
	}
	j.promoted = true
	j.promotedAt = time.Now()
	l := j.listener
	j.mu.Unlock()
	if l != nil {
		l(j, EventPromote)
	}
	return true
}

func (j *Job) Promoted() (bool, time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.promoted {
		return false, 0
	}
	return true, j.promotedAt.Sub(j.started)
}

func (j *Job) Cancel(reason string) bool {
	j.mu.Lock()
	cancel := j.cancel
	running := j.status == StatusRunning
	j.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	go func() {
		st := cancel()
		if st == "" {
			st = StatusKilled
		}
		d := "canceled"
		if reason != "" {
			d = "canceled: " + reason
		}
		j.Settle(st, d)
	}()
	return true
}

func (j *Job) Wait(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		select {
		case <-j.settled:
			j.markAwaited()
			return true
		default:
			return false
		}
	}
	j.mu.Lock()
	j.waiters++
	j.mu.Unlock()
	defer func() {
		j.mu.Lock()
		j.waiters--
		j.mu.Unlock()
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-j.settled:
		j.markAwaited()
		return true
	case <-ctx.Done():
		return j.Status() != StatusRunning
	case <-t.C:
		return false
	}
}

func (j *Job) markAwaited() {
	j.mu.Lock()
	j.awhield = true
	j.mu.Unlock()
}

func (j *Job) Awaited() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.awhield
}

func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

func (j *Job) Output(consume bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if consume {
		j.unread = 0
	}
	if j.final != "" {
		return j.final
	}
	var b strings.Builder
	if j.dropped > 0 {
		fmt.Fprintf(&b, "(%d bytes dropped from the front of the output)\n", j.dropped)
	}
	b.Write(j.ring)
	if j.status != StatusRunning {
		fmt.Fprintf(&b, "[status: %s", j.status)
		if j.detail != "" {
			b.WriteString(", " + j.detail)
		}
		b.WriteString("]\n")
	}
	return b.String()
}

type Snapshot struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Status    Status    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Unread    int64     `json:"unread"`
	Total     int64     `json:"total_bytes"`
	Promoted bool  `json:"promoted,omitempty"`
	WaitedMS int64 `json:"waited_ms,omitempty"`
}

func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{
		ID: j.ID, Kind: j.Kind, Label: j.Label, Status: j.status, Detail: j.detail,
		StartedAt: j.started, EndedAt: j.ended, Unread: j.unread, Total: j.total,
		Promoted: j.promoted,
	}
	if j.promoted {
		s.WaitedMS = j.promotedAt.Sub(j.started).Milliseconds()
	}
	return s
}

type Registry struct {
	mu     sync.Mutex
	byID   map[string]*Job
	order  []string
	seq    uint64
	listen func(*Job, string)
}

func New() *Registry { return &Registry{byID: make(map[string]*Job)} }

func (r *Registry) SetListener(fn func(*Job, string)) {
	r.mu.Lock()
	r.listen = fn
	r.mu.Unlock()
}

func (r *Registry) Start(owner, kind, label string, bg bool) *Job {
	r.mu.Lock()
	r.seq++
	if r.byID == nil {
		r.byID = make(map[string]*Job)
	}
	id := fmt.Sprintf("%s-%s", kind, randID())
	j := newJob(id, owner, kind, truncate(label, 120), r.listen)
	j.Background = bg
	r.byID[id] = j
	r.order = append(r.order, id)
	l := r.listen
	r.mu.Unlock()
	if l != nil {
		l(j, EventStart)
	}
	return j
}

func (r *Registry) Get(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

func (r *Registry) List(owner string) []Snapshot {
	r.mu.Lock()
	jobs := make([]*Job, 0, len(r.order))
	for _, id := range r.order {
		if j := r.byID[id]; j != nil && (owner == "" || j.Owner == owner) {
			jobs = append(jobs, j)
		}
	}
	r.mu.Unlock()
	out := make([]Snapshot, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Snapshot())
	}
	return out
}

func (r *Registry) Running(owner string) []string {
	r.mu.Lock()
	var ids []string
	for _, id := range r.order {
		if j := r.byID[id]; j != nil && j.Owner == owner && j.status == StatusRunning {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	return ids
}

func (r *Registry) CancelAll(owner, reason string) int {
	r.mu.Lock()
	var targets []*Job
	for _, id := range r.order {
		if j := r.byID[id]; j != nil && j.Owner == owner && j.status == StatusRunning {
			targets = append(targets, j)
		}
	}
	r.mu.Unlock()
	n := 0
	for _, j := range targets {
		j.mu.Lock()
		cancel := j.cancel
		j.mu.Unlock()
		if cancel == nil {
			j.Settle(StatusLost, "session closed")
			n++
			continue
		}
		go func(j *Job) {
			st := cancel()
			if st == "" {
				st = StatusKilled
			}
			j.mu.Lock()
			j.awhield = true
			j.mu.Unlock()
			j.Settle(st, "session closed: "+reason)
		}(j)
		n++
	}
	return n
}

func (r *Registry) Prune(ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := time.Now().Add(-ttl)
	kept := r.order[:0]
	dropped := 0
	for _, id := range r.order {
		j := r.byID[id]
		if j == nil {
			continue
		}
		s := j.Snapshot()
		if s.Status == StatusRunning || s.EndedAt.After(cut) {
			kept = append(kept, id)
			continue
		}
		delete(r.byID, id)
		dropped++
	}
	r.order = kept
	return dropped
}

func randID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

func truncate(s string, n int) string {
	r := []rune(strings.ReplaceAll(strings.TrimSpace(s), "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
