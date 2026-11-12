package tools

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The shell pool: who may hold a cmd.exe, and what a shell must look like when
// it is handed out.
//
// Why this exists. The persistent shell used to be one object with one mutex:
// every exec call queued behind every other, and a command that ran long held
// the whole session hostage. Once a long command can be LEFT ALONE (promoted to
// a background job) instead of killed, that single slot becomes the bottleneck
// in a new way — the abandoned command would still be sitting on the only
// shell, so the model's next command would wait behind work it has already
// walked away from. Concurrency is what makes promotion worth having, and a pool
// is how you get concurrency out of stateful shells without letting two commands
// scribble on the same cmd.exe.
//
// The rules:
//
//   - A shell is leased, not shared. One holder at a time; the holder may keep
//     it while its job runs in the background.
//   - The number of shells is capped, including the ones held by background
//     jobs. Past the ceiling, callers queue, and the queue is served in arrival
//     order so a promoted job cannot be starved by a stream of short commands.
//   - The session's shell state — the working directory and the environment
//     variables the model set — belongs to the POOL, not to a shell. A recycled
//     or freshly spawned cmd.exe is brought up to that state when it is leased,
//     so "the session is persistent" stays true even though several processes
//     stand behind it, and even across the restart that follows a timeout, a
//     kill, or a shell that was never warm.
//
// The environment sync is deliberately narrow: it carries only the variables
// that differ from the environment this process started with, and it reads a
// shell's environment back only after a command that spelled `set`. Values
// containing a double quote are skipped (cmd.exe has no quoting that survives
// them), and an oversized diff is dropped rather than prepended to every
// command.

const (
	// poolMaxShells bounds the processes, including those held by promoted
	// jobs. Six cmd.exe is small next to a compiler, and the ceiling is what
	// stops a fan-out of background jobs from turning into a fork bomb.
	poolMaxShells = 6
	// poolMaxIdle is how many warm shells to keep for the next call; beyond
	// that a returned shell is torn down instead of parked.
	poolMaxIdle = 2
	// poolEnvMaxLines / poolEnvMaxBytes cap the state replayed into a shell. A
	// script that exports a thousand variables is not what this exists for:
	// carrying its environment would cost more than it saves.
	poolEnvMaxLines = 64
	poolEnvMaxBytes = 4096
)

// shellSlot is one cmd.exe plus what the pool remembers about it.
type shellSlot struct {
	ps      *persistentShell
	envSnap map[string]string // last environment seen in THIS shell (nil = process default)
	envSig  string            // signature of envSnap, to spot a needed replay
}

// shellPool owns a session's shells and its shell state.
type shellPool struct {
	spawnDir string

	mu      sync.Mutex
	shells  []*shellSlot // everything we own, leased or not
	idle    []*shellSlot
	leased  map[*shellSlot]int
	waiters []chan struct{}

	base    map[string]string // the environment a fresh cmd.exe starts with
	env     map[string]string // the session's authoritative environment
	origKey map[string]string // lowercased name -> name as last spelled
	envSig  string
	cwd     string // the session's directory ("" = wherever a shell happens to be)
}

func newShellPool(spawnDir string) *shellPool {
	base := environMap()
	p := &shellPool{
		spawnDir: spawnDir,
		leased:   make(map[*shellSlot]int),
		base:     base,
		env:      copyEnv(base),
		origKey:  make(map[string]string, len(base)),
	}
	for k := range base {
		p.origKey[k] = k
	}
	p.envSig = envSignature(p.env)
	return p
}

// shellLease is a holder's ticket on a shell. run happens at most once per
// lease; release is idempotent so a deferred cleanup cannot double-return it.
type shellLease struct {
	pool   *shellPool
	slot   *shellSlot
	lines  []string // environment replay, applied before the first real command
	once   sync.Once
	cwdOut string // where the shell is expected to end up (published on release)
}

// lease takes a shell for exclusive use, waiting for a free slot. The wait ends
// with ctx, so a blocked lease is bounded by the caller's patience (and, for a
// promoted job, by the background limit) rather than by an unbounded queue.
func (p *shellPool) lease(ctx context.Context) (*shellLease, error) {
	for {
		p.mu.Lock()
		var slot *shellSlot
		if n := len(p.idle); n > 0 {
			slot = p.idle[n-1]
			p.idle = p.idle[:n-1]
		} else if len(p.shells) < poolMaxShells {
			slot = &shellSlot{ps: &persistentShell{spawnDir: p.spawnDir}}
			p.shells = append(p.shells, slot)
			trackShell(slot.ps)
		}
		if slot != nil {
			// A shell whose process is gone has no memory: whatever we recorded
			// about its environment is worth nothing, so it is treated exactly
			// like a brand-new one. Checked under ps.mu because the idle reaper
			// can retire this shell at any moment.
			slot.ps.mu.Lock()
			dead := !slot.ps.started
			slot.ps.mu.Unlock()
			if dead {
				slot.envSnap, slot.envSig = nil, ""
			}
			p.leased[slot]++
			lines := p.stateLinesLocked(slot)
			cwd := p.cwd
			p.mu.Unlock()
			return &shellLease{pool: p, slot: slot, lines: lines, cwdOut: cwd}, nil
		}
		ch := make(chan struct{}, 1)
		p.waiters = append(p.waiters, ch)
		p.mu.Unlock()

		select {
		case <-ch:
			// A shell came back; try again (arrival order is preserved because
			// waiters are woken from the front).
		case <-ctx.Done():
			p.dropWaiter(ch)
			return nil, ctx.Err()
		}
	}
}

// dropWaiter removes an abandoned ticket (the holder gave up).
func (p *shellPool) dropWaiter(ch chan struct{}) {
	p.mu.Lock()
	for i, w := range p.waiters {
		if w == ch {
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
}

// release returns the shell. poison means "do not hand this one to anyone": the
// process was killed or wedged, so it is torn down and forgotten instead of
// parked for the next command.
func (l *shellLease) release(poison bool) {
	l.once.Do(func() { l.pool.returnSlot(l, poison) })
}

func (p *shellPool) returnSlot(l *shellLease, poison bool) {
	p.mu.Lock()
	slot := l.slot
	if p.leased[slot] > 1 {
		p.leased[slot]--
		p.mu.Unlock()
		return
	}
	delete(p.leased, slot)

	if poison {
		slot.ps.mu.Lock()
		slot.ps.teardownLocked()
		slot.ps.mu.Unlock()
		slot.envSnap, slot.envSig = nil, ""
		p.dropShellLocked(slot)
	} else {
		if slot.envSnap != nil {
			// This shell is now the authority on the session's environment: what
			// it holds is what the next command should see.
			p.env = copyEnv(slot.envSnap)
			p.envSig = envSignature(p.env)
		}
		if l.cwdOut != "" {
			p.cwd = l.cwdOut
		}
		if len(p.idle) < poolMaxIdle {
			p.idle = append(p.idle, slot)
		} else {
			slot.ps.mu.Lock()
			slot.ps.teardownLocked()
			slot.ps.mu.Unlock()
			p.dropShellLocked(slot)
		}
	}
	// One wake per returned shell: the head of the queue gets it.
	if n := len(p.waiters); n > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		select {
		case w <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}

// dropShellLocked forgets a shell entirely, so it stops counting toward the
// ceiling. Caller holds p.mu.
func (p *shellPool) dropShellLocked(slot *shellSlot) {
	for i, s := range p.shells {
		if s == slot {
			p.shells = append(p.shells[:i], p.shells[i+1:]...)
			break
		}
	}
	for i, s := range p.idle {
		if s == slot {
			p.idle = append(p.idle[:i], p.idle[i+1:]...)
			break
		}
	}
}

// close tears down every shell the pool owns. Anything still running was
// canceled by the caller first; this only frees processes.
func (p *shellPool) close() {
	p.mu.Lock()
	all := append([]*shellSlot(nil), p.shells...)
	p.shells, p.idle = nil, nil
	p.leased = make(map[*shellSlot]int)
	p.mu.Unlock()
	for _, s := range all {
		s.ps.mu.Lock()
		s.ps.teardownLocked()
		s.ps.mu.Unlock()
		shellTracker.mu.Lock()
		delete(shellTracker.live, s.ps)
		shellTracker.mu.Unlock()
	}
}

// ------------------------------------------------------------- state replay

// stateLinesLocked returns the `set` lines that bring slot's environment up to
// the session's. Empty means "nothing to replay". Caller holds p.mu.
//
// Only variables that differ from the process default are carried. That keeps
// the replay tiny in the common case (a build sets one or two variables) and
// makes the direction of every change explicit: a variable the session deleted
// gets `set NAME=` in the new shell, because a fresh cmd.exe would still have
// it.
func (p *shellPool) stateLinesLocked(slot *shellSlot) []string {
	if slot.envSig == p.envSig {
		return nil
	}
	names := make([]string, 0, 16)
	for k := range p.env {
		names = append(names, k)
	}
	for k := range p.base {
		if _, ok := p.env[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	var lines []string
	size := 0
	for _, k := range names {
		want, inEnv := p.env[k]
		got, inBase := p.base[k]
		if inEnv == inBase && (!inEnv || want == got) {
			continue // unchanged, or absent on both sides
		}
		name := p.origKey[k]
		if name == "" {
			name = k
		}
		if !inEnv {
			lines = append(lines, `set "`+name+`="`)
			size += len(name) + 8
			continue
		}
		if strings.ContainsAny(want, "\"\r\n") {
			continue // cmd.exe cannot set this value through any quoting
		}
		line := `set "` + name + "=" + want + `"`
		size += len(line)
		if size > poolEnvMaxBytes || len(lines) >= poolEnvMaxLines {
			return nil // too much state to carry: leave the shell as it is
		}
		lines = append(lines, line)
	}
	return lines
}

// reSetUse matches a command that touches the environment — at the start of a
// line or of a && / || clause, which is how the model actually spells it
// (`cd build && set X=1`). Only then is it worth a round trip to read the
// shell's environment back.
var reSetUse = regexp.MustCompile(`(?i)(^|[&|;(])\s*set(\s|/|$)`)

// noteEnv records the environment the shell now holds, read back after a command
// that spelled `set`. A nil or unparsable snapshot is ignored: the pool keeps
// whatever it already believed rather than forgetting the session's state.
func (l *shellLease) noteEnv(raw []byte) {
	env := parseEnv(raw)
	if env == nil {
		return
	}
	p := l.pool
	p.mu.Lock()
	l.slot.envSnap = env
	l.slot.envSig = envSignature(env)
	for _, k := range sortedKeys(env) {
		p.origKey[k] = envKeyOf(raw, k)
	}
	p.mu.Unlock()
}

// setCwd publishes where the command left the shell.
func (l *shellLease) setCwd(dir string) {
	if dir == "" {
		return
	}
	l.pool.mu.Lock()
	l.cwdOut = dir
	l.pool.mu.Unlock()
}

// targetCwd is the directory this lease should run in when the caller named
// none: the session's directory, so a second or a restarted shell lands where
// the model thinks it is.
func (l *shellLease) targetCwd(cwd string) string {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	if cwd != "" {
		l.cwdOut = cwd
		return cwd
	}
	return l.pool.cwd
}

// -------------------------------------------------------------- env helpers

// environMap reads the current process environment into a lower-keyed map.
func environMap() map[string]string {
	m := make(map[string]string, 64)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[strings.ToLower(kv[:i])] = kv[i+1:]
		}
	}
	return m
}

// parseEnv reads cmd.exe's `set` output ("NAME=VALUE" per line) into a
// lower-keyed map. Lines without an '=' are boot noise and are skipped.
func parseEnv(raw []byte) map[string]string {
	m := make(map[string]string, 64)
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimRight(ln, "\r")
		i := strings.IndexByte(ln, '=')
		if i <= 0 {
			continue
		}
		m[strings.ToLower(ln[:i])] = ln[i+1:]
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// envKeyOf recovers the original spelling of a lowercased variable name from the
// raw `set` output.
func envKeyOf(raw []byte, lowerKey string) string {
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if i := strings.IndexByte(ln, '='); i > 0 && strings.ToLower(ln[:i]) == lowerKey {
			return ln[:i]
		}
	}
	return lowerKey
}

func copyEnv(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// envSignature is a cheap identity for an environment map: two environments with
// the same signature need no replay between them.
func envSignature(m map[string]string) string {
	var b strings.Builder
	for _, k := range sortedKeys(m) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(m[k])
		b.WriteByte(0)
	}
	return b.String()
}
