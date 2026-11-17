package tools

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)


const (
	poolMaxShells = 6
	poolMaxIdle = 2
	poolEnvMaxLines = 64
	poolEnvMaxBytes = 4096
)

type shellSlot struct {
	ps      *persistentShell
	envSnap map[string]string
	envSig  string
}

type shellPool struct {
	spawnDir string

	mu      sync.Mutex
	shells  []*shellSlot
	idle    []*shellSlot
	leased  map[*shellSlot]int
	waiters []chan struct{}

	base    map[string]string
	env     map[string]string
	origKey map[string]string
	envSig  string
	cwd     string
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

type shellLease struct {
	pool   *shellPool
	slot   *shellSlot
	lines  []string
	once   sync.Once
	cwdOut string
}

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
		case <-ctx.Done():
			p.dropWaiter(ch)
			return nil, ctx.Err()
		}
	}
}

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
			continue
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
			continue
		}
		line := `set "` + name + "=" + want + `"`
		size += len(line)
		if size > poolEnvMaxBytes || len(lines) >= poolEnvMaxLines {
			return nil
		}
		lines = append(lines, line)
	}
	return lines
}

var reSetUse = regexp.MustCompile(`(?i)(^|[&|;(])\s*set(\s|/|$)`)

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

func (l *shellLease) setCwd(dir string) {
	if dir == "" {
		return
	}
	l.pool.mu.Lock()
	l.cwdOut = dir
	l.pool.mu.Unlock()
}

func (l *shellLease) targetCwd(cwd string) string {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	if cwd != "" {
		l.cwdOut = cwd
		return cwd
	}
	return l.pool.cwd
}


func environMap() map[string]string {
	m := make(map[string]string, 64)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[strings.ToLower(kv[:i])] = kv[i+1:]
		}
	}
	return m
}

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
