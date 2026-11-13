package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Observation is a session's memory of which files it has looked at.
//
// It has two jobs, and they were deliberately separated after measurement:
//
//   - A gate (NeedsRead): write_file / edit_file refuse to modify an existing
//     file this session never read, or one that changed on disk after it was
//     read. The earlier version only recorded blind mutations because the
//     comparison round measured 0 blind edits out of 78 and concluded the gate
//     had nothing to refuse; that held for a careful model on short tasks. It
//     does not hold for a local model on long ones, where the failure is not
//     "edits without reading" but "edits a file from a stale memory of it" —
//     which the same fingerprint check catches.
//   - A ledger (Blind + Count): the mutation-without-read ratio is still worth
//     recording, because a gate that never fires and a gate that keeps firing
//     are both information.
//
// What is NOT observed, stated plainly so the number is not over-read: changes
// made through exec (a script that rewrites a file, a formatter, `git checkout`)
// are invisible here — the harness cannot know what a command touched without
// diffing the tree. The fingerprint check catches those the next time the tool
// is used (the file's size/mtime no longer match what was read), which is why a
// stale read is a hard error rather than a warning.
type Observation struct {
	mu   sync.Mutex
	seen map[string]fileRecord
}

// fileRecord is what the session knows about one path.
type fileRecord struct {
	readAt time.Time
	// content is true when the file's CONTENT was read (read_file/readimg, or
	// written by this session). A listing (list_dir/glob/search_files) only
	// proves the name exists: it is enough for the blind-mutation ratio, not
	// enough to authorise overwriting the file.
	content bool
	// size/mtime are the file's state at the moment it was read. They are the
	// cheap stale check: an external edit (formatter, git checkout, another
	// agent) almost always changes one of them.
	size  int64
	mtime time.Time
}

// NewObservation returns an empty observation for one session.
func NewObservation() *Observation {
	return &Observation{seen: make(map[string]fileRecord, 64)}
}

// NoteRead records that the session has looked at a path: its listing, a
// search hit in it, or its contents. It does not authorise a write (see
// NoteContentRead).
func (o *Observation) NoteRead(path string) {
	key := obsKey(path)
	if key == "" {
		return
	}
	o.mu.Lock()
	if _, ok := o.seen[key]; !ok {
		o.seen[key] = fileRecord{readAt: time.Now()}
	}
	o.mu.Unlock()
}

// NoteContentRead records that the session has read the file's contents, with
// the size/mtime it had at that moment.
func (o *Observation) NoteContentRead(path string) {
	key := obsKey(path)
	if key == "" {
		return
	}
	rec := fileRecord{readAt: time.Now(), content: true}
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		rec.size = fi.Size()
		rec.mtime = fi.ModTime()
	}
	o.mu.Lock()
	o.seen[key] = rec
	o.mu.Unlock()
}

// NoteWrite records that this session wrote the file, which makes its current
// contents known: the model produced them. Without this every second edit of
// the same file would demand a redundant re-read.
func (o *Observation) NoteWrite(path string) {
	o.NoteContentRead(path)
}

// Seen reports whether this session has looked at path at all (listing counts).
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

// NeedsRead reports whether modifying path must be refused: the file exists,
// its content was never read in this session (or only its name was seen), or it
// changed on disk since it was read. The reason is the sentence the model gets
// back, so it has to say what to do next, not just that something is wrong.
func (o *Observation) NeedsRead(path string) (bool, string) {
	key := obsKey(path)
	if key == "" {
		return false, ""
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false, "" // creating a new file is not a blind overwrite
	}
	o.mu.Lock()
	rec, ok := o.seen[key]
	o.mu.Unlock()
	switch {
	case !ok:
		return true, fmt.Sprintf("%s has not been read in this session, so overwriting it could destroy content you have never seen", path)
	case !rec.content:
		return true, fmt.Sprintf("%s has only been listed or searched, not read", path)
	case rec.mtime.IsZero() || rec.size != fi.Size() || !rec.mtime.Equal(fi.ModTime()):
		return true, fmt.Sprintf("%s changed on disk after this session read it (size/mtime differ), so the copy in context is stale", path)
	}
	return false, ""
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
