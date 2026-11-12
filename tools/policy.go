package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Observation is a session's memory of which files it has looked at.
//
// Why it exists, and why it stops there. The comparison round measured how often
// each harness modified a file that already existed without having read it: 0 of
// 78 mutations on this side, 1 of 119 on the other. Neither harness has a blind
// editing problem, so the mechanism the other side ships for it (a filesystem
// observation policy that refuses an edit until the file has been observed) has
// nothing to refuse — on the measured runs it never once fired.
//
// So this records and does not gate. The number is still worth having, because
// the change being tested here moves work around in time: commands get promoted
// to the background, several shells can be in flight at once, and a model that no
// longer waits for a build may spend its next calls editing files it reached past.
// If that shows up as blind mutations, the ledger will say so, and the decision
// to gate can then be made on evidence instead of on taste.
//
// What is NOT observed, stated plainly so the number is not over-read: changes
// made through exec (a script that rewrites a file, a formatter, `git checkout`)
// are invisible here — the harness cannot know what a command touched without
// diffing the tree. The ratio is therefore a floor on "did the model look before
// it leapt", not a complete account.
type Observation struct {
	mu   sync.Mutex
	seen map[string]time.Time // normalized path -> when this session first saw it
}

// NewObservation returns an empty observation for one session.
func NewObservation() *Observation {
	return &Observation{seen: make(map[string]time.Time, 64)}
}

// NoteRead records that the session has looked at a path: its contents, or its
// listing, which is also "knowing" in the sense that matters — a directory
// listing that includes a file tells the model the file is there.
func (o *Observation) NoteRead(path string) {
	key := obsKey(path)
	if key == "" {
		return
	}
	o.mu.Lock()
	if _, ok := o.seen[key]; !ok {
		o.seen[key] = time.Now()
	}
	o.mu.Unlock()
}

// Seen reports whether this session has looked at path.
func (o *Observation) Seen(path string) bool {
	key := obsKey(path)
	if key == "" {
		return false
	}
	o.mu.Lock()
	_, ok := o.seen[key]
	o.mu.Unlock()
	return ok
}

// Blind reports whether modifying path would be modifying a file this session
// never looked at. A path that does not exist is not blind: creating something is
// a different mistake from overwriting something, and only the second one is what
// this measures.
func (o *Observation) Blind(path string) bool {
	key := obsKey(path)
	if key == "" || o.Seen(path) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return true
}

// Count is how many paths the session has observed (a cheap health check for the
// ledger and for tests).
func (o *Observation) Count() int {
	o.mu.Lock()
	n := len(o.seen)
	o.mu.Unlock()
	return n
}

// obsKey normalizes a path for comparison: separators unified, trailing ones cut,
// case folded where the OS folds it. The model writes "src\\a.go", "src/a.go" and
// "D:\\proj\\src\\a.go" for the same file within one session, and a memory keyed
// by spelling would remember nothing.
func obsKey(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	p = filepath.Clean(filepath.FromSlash(p))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}
