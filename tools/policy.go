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

type Observation struct {
	mu   sync.Mutex
	seen map[string]fileRecord
}

type fileRecord struct {
	readAt time.Time
	content bool
	size  int64
	mtime time.Time
}

func NewObservation() *Observation {
	return &Observation{seen: make(map[string]fileRecord, 64)}
}

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

func (o *Observation) NoteWrite(path string) {
	o.NoteContentRead(path)
}

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

func (o *Observation) NeedsRead(path string) (bool, string) {
	key := obsKey(path)
	if key == "" {
		return false, ""
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false, ""
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

func (o *Observation) Count() int {
	o.mu.Lock()
	n := len(o.seen)
	o.mu.Unlock()
	return n
}

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
