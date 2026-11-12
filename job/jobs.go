// Package job is the session-scoped background-work runtime.
//
// Why it exists: before it, one exec call was all-or-nothing — the loop held
// the turn until the command finished or the deadline killed it (along with
// its work). On the C-round measurement that cost mc 64% of wall clock, with a
// single command waiting 1,031s while the model sat idle. The job runtime
// splits "start a command" from "collect its result": a call registers a Job,
// the loop waits on the job with a deadline, and a deadline EXPIRY PROMOTES the
// job (the process keeps running) instead of killing it. Completion then
// reaches the owning session as an in-session notice, so the model never has to
// poll and never has to block on work that is not blocking it.
//
// Three invariants the rest of the harness depends on:
//
//   - A job's settlement never rewrites history. The tool call that started it
//     already returned its own ("moved to background") result; the completion
//     arrives as a NEW message. Rewriting an emitted tool result would break
//     the provider's prefix cache and, as the arg-aging episode showed, the
//     model's grounding.
//   - The registry is the ledger's peer, not its replacement: every
//     registration, promotion and settlement is also emitted as a session
//     event, so "how long was the model waiting on tools" stays a query, not a
//     guess.
//   - Expiring a wait never kills work. Promote changes who is watching, not
//     what happens to the process: only an explicit Cancel, the process's own
//     exit, or the owner's teardown ends a job.
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

// Status is a job's lifecycle state.
type Status string

const (
	// StatusRunning: started, not settled yet.
	StatusRunning Status = "running"
	// StatusCompleted: settled by its producer with a normal outcome.
	StatusCompleted Status = "completed"
	// StatusFailed: settled by its producer with an error outcome.
	StatusFailed Status = "failed"
	// StatusKilled: canceled (model asked, or the owner went away).
	StatusKilled Status = "killed"
	// StatusLost: the producer died without settling (process crashed, shell
	// tore down). Distinct from failed so a lost job is never read as an exit
	// code the model should reason about.
	StatusLost Status = "lost"
)

// maxRingBytes bounds the in-memory output tail (the model-facing view). Past
// it the oldest bytes are dropped and the read says so; the full stream stays
// available to whatever producer wanted to spill it.
const maxRingBytes = 64 << 10

// Event kinds passed to the registry listener. They mirror the session ledger
// events (job/registered, job/promoted, job/settled) so the hook that writes
// the ledger and the hook that notifies the model are the same code path.
const (
	EventStart   = "start"
	EventPromote = "promote"
	EventSettle  = "settle"
)

// Job is one piece of background work: an id, ownership, an output tail, and a
// settlement that wakes whoever waits for it.
type Job struct {
	// ID is unique per registry ("exec-1a2b3c4d"); Owner is the session that
	// started it, which is also the only session allowed to see it.
	ID    string
	Owner string
	Kind  string // the tool that started it, e.g. "exec"
	// Label is a short, model-facing description (the command's first line).
	Label string
	// Background is fixed at birth: true when the caller is walking away from this
	// work immediately (exec run_in_background). Listeners use it to decide what is
	// worth a ledger row — a foreground command that finished while the model
	// watched is already fully described by its tool call and tool result, and
	// registering every one of those as a job buries the interesting rows. A wait
	// that gives up announces the job through EventPromote instead, so the
	// promotion path does not need this flag to be reported.
	Background bool

	mu         sync.Mutex
	status     Status
	detail     string // exit code / kill reason / producer note, one line
	ring       []byte // output tail, newest at the end
	dropped    int64  // bytes dropped from the front of the ring
	total      int64  // bytes ever written
	unread     int64  // bytes written but not yet read by the model
	started    time.Time
	ended      time.Time
	promotedAt time.Time     // when a foreground wait handed this job over
	cancel     func() Status // producer-supplied kill; reports the final status
	settled    chan struct{}
	awhield    bool   // a live wait collected this job: skip the notice
	waiters    int    // waits currently blocked on this job (a settle while >0 is awaited)
	promoted   bool   // a wait gave up on it; the process keeps running
	final      string // producer's model-visible final text, when it has one
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

// SetCancel hands the job its kill switch. Called by the producer right after
// Start; cancel must be idempotent and return the status the job should carry.
func (j *Job) SetCancel(cancel func() Status) {
	j.mu.Lock()
	j.cancel = cancel
	j.mu.Unlock()
}

// SetFinal records the producer's model-visible final text — the same text the
// caller would have gotten had it waited. Output returns it verbatim, so a
// command collected in the foreground reads exactly like one collected in the
// background, byte for byte: no duplicated output, no format drift between the
// two paths. Producers that have no such text (a killed job, a lost process)
// leave it empty and Output falls back to the output tail.
func (j *Job) SetFinal(text string) {
	j.mu.Lock()
	j.final = text
	j.mu.Unlock()
}

// Write appends producer output. It satisfies io.Writer, so a shell or process
// can stream straight into the job while a live UI feed is also being served.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status != StatusRunning {
		// Late bytes after settlement (a grandchild that outlived the
		// command) are counted but not kept: the model already has a
		// terminal answer and a second one would be a contradiction.
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

// Settle ends the job exactly once. status Completed/Failed come from the
// producer; detail is one line the model sees ("exit code: 1").
func (j *Job) Settle(status Status, detail string) {
	j.mu.Lock()
	if j.status != StatusRunning {
		j.mu.Unlock()
		return
	}
	j.status, j.detail, j.ended = status, detail, time.Now()
	// A wait blocked on this job IS the answer's destination: whoever asked is
	// about to receive the result, so the listener must not also announce it.
	// Deciding here, under the lock that closes the channel, is what keeps the
	// settlement notice from beating the waiter that was already awake — a race
	// that otherwise announces a result the model is holding in its hand.
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

// Promote hands the job over to the background: a foreground wait has run out
// of patience but the work goes on. It reports whether this call was the one
// that handed it over (so the caller knows to say "moved to background"), and
// it never touches the running work.
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

// Promoted reports whether a wait handed this job to the background, and for
// how long the harness waited before doing so.
func (j *Job) Promoted() (bool, time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.promoted {
		return false, 0
	}
	return true, j.promotedAt.Sub(j.started)
}

// Cancel asks the producer to stop. It reports whether a running job was
// told to stop; the job settles when the work actually stops.
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

// Wait blocks until the job settles or the timeout expires (a timeout leaves
// the job running). Passing wait <= 0 waits only for an already-settled job.
// It reports whether the job settled, and yields early on ctx cancellation.
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
	// Being blocked here is what "someone is waiting" means, and the settlement
	// checks it before deciding whether to announce the job instead.
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

// markAwaited records that a live wait collected the job, so the settlement
// listener does not also deliver a notice for the same completion.
func (j *Job) markAwaited() {
	j.mu.Lock()
	j.awhield = true
	j.mu.Unlock()
}

// Awaited reports whether a live wait already collected this completion.
func (j *Job) Awaited() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.awhield
}

// Status reports the current lifecycle state.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// Output renders the model-facing view: the output tail since the previous
// read, a note when bytes were dropped, and the settled detail. consume=true
// clears the unread counter, so the settlement notice does not repeat what the
// model has already been shown.
func (j *Job) Output(consume bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if consume {
		j.unread = 0
	}
	// The producer's own final text wins, verbatim: the foreground and the
	// background then hand the model the same bytes for the same command.
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

// Snapshot is the JSON/UI view (no ownership internals, no ring).
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
	// Promoted marks a job whose foreground wait gave up on it; WaitedMS is how
	// long that wait was, which is the number the wall-clock analysis wants.
	Promoted bool  `json:"promoted,omitempty"`
	WaitedMS int64 `json:"waited_ms,omitempty"`
}

// Snapshot returns the current public view.
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

// Registry holds one session's jobs. It is owned by the session (through its
// prompt builder) so jobs survive across turns but never cross sessions.
type Registry struct {
	mu     sync.Mutex
	byID   map[string]*Job
	order  []string
	seq    uint64
	listen func(*Job, string)
}

// New returns an empty registry.
func New() *Registry { return &Registry{byID: make(map[string]*Job)} }

// SetListener installs the lifecycle hook (the session uses it to write the
// ledger and to inject the completion notice). Set it before starting jobs: a
// job created earlier keeps whatever hook the registry had at that moment.
func (r *Registry) SetListener(fn func(*Job, string)) {
	r.mu.Lock()
	r.listen = fn
	r.mu.Unlock()
}

// Start registers a running job. label is the model-facing one-liner; the
// caller must SetCancel so the job can be killed. bg says whether the caller is
// walking away immediately (see Job.Background).
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
	// Announce outside the lock: the hook writes session events, and a hook
	// that ever looked back at the registry must not meet a held lock.
	if l != nil {
		l(j, EventStart)
	}
	return j
}

// Get returns the job or nil.
func (r *Registry) Get(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// List returns every job owned by owner, oldest first.
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

// Running returns the ids of the owner's still-running jobs.
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

// CancelAll settles every running job of owner (the session is going away):
// the kill switch runs, and the settlement is marked as teardown so no notice
// is delivered to a session nobody is left to read.
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
			j.awhield = true // teardown settlement is nobody's news
			j.mu.Unlock()
			j.Settle(st, "session closed: "+reason)
		}(j)
		n++
	}
	return n
}

// Prune drops settled jobs older than ttl so a long-lived session's registry
// cannot grow without bound. Returns how many were dropped.
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
