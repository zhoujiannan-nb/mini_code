// fsindex.go — in-memory directory index backing the web UI's "@" folder
// picker (GET /api/fs/search).
//
// The index is a flat list of directory paths built by walking every fixed
// drive (Windows) or a few common roots (other OSes), pruning dependency
// caches and OS internals. It is rebuilt periodically in the background, so
// searches are a pure in-memory scan over a few hundred thousand short
// strings (a few milliseconds, no disk I/O on the hot path).

package web

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fsIndexMaxDirs = 250000       // hard cap on indexed directories
	fsIndexRefresh = 15 * time.Minute
	fsSearchWait   = 8 * time.Second // how long a search waits for the first build
)

// skipDirNames are pruned during the walk: dependency caches, VCS metadata
// and OS internals that are huge and almost never referenced in a prompt.
var skipDirNames = map[string]struct{}{
	"node_modules": {}, ".git": {}, ".svn": {}, ".hg": {},
	"__pycache__": {}, ".tox": {}, ".mypy_cache": {}, ".pytest_cache": {},
	".venv": {}, "venv": {}, ".idea": {}, ".vs": {},
	".gradle": {}, ".cargo": {}, ".rustup": {}, ".npm": {}, ".cache": {},
	"appdata": {}, "windows": {}, "$recycle.bin": {},
	"system volume information": {}, "recovery": {}, "perflogs": {},
	"library": {},
}

// DirIndex is a snapshot of the machine's directory tree for fast search.
type DirIndex struct {
	mu      sync.RWMutex
	dirs    []string
	roots   map[string]struct{}
	ready   bool
	builtAt time.Time
	first   chan struct{} // closed when the first build finishes
}

// NewDirIndex creates an empty index. Call Run to build it.
func NewDirIndex() *DirIndex {
	return &DirIndex{first: make(chan struct{})}
}

// Run builds the index and refreshes it every fsIndexRefresh until ctx ends.
func (ix *DirIndex) Run(ctx context.Context) {
	ix.build()
	ticker := time.NewTicker(fsIndexRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ix.build()
		}
	}
}

func (ix *DirIndex) build() {
	roots := driveRoots()
	rootSet := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		rootSet[r] = struct{}{}
	}
	// Walk each root in parallel (drives are independent).
	var wg sync.WaitGroup
	parts := make([][]string, len(roots))
	for i, root := range roots {
		wg.Add(1)
		go func(i int, root string) {
			defer wg.Done()
			parts[i] = walkDirs(root)
		}(i, root)
	}
	wg.Wait()
	dirs := make([]string, 0, 65536)
	for _, p := range parts {
		dirs = append(dirs, p...)
		if len(dirs) >= fsIndexMaxDirs {
			dirs = dirs[:fsIndexMaxDirs]
			break
		}
	}
	ix.mu.Lock()
	ix.dirs = dirs
	ix.roots = rootSet
	ix.ready = true
	ix.builtAt = time.Now()
	ix.mu.Unlock()
	select {
	case <-ix.first:
	default:
		close(ix.first)
	}
}

func walkDirs(root string) []string {
	dirs := make([]string, 0, 32768)
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // unreadable entry: skip
		}
		if len(dirs) >= fsIndexMaxDirs {
			return filepath.SkipAll
		}
		if path != root {
			if _, skip := skipDirNames[strings.ToLower(d.Name())]; skip {
				return filepath.SkipDir
			}
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs
}

// driveRoots returns the top-level roots to index: every fixed drive on
// Windows, a few common roots elsewhere.
func driveRoots() []string {
	var roots []string
	if runtime.GOOS == "windows" {
		for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			root := string(c) + ":\\"
			if fi, err := os.Stat(root); err == nil && fi.IsDir() {
				roots = append(roots, root)
			}
		}
		return roots
	}
	home, _ := os.UserHomeDir()
	for _, r := range append([]string{home}, "/opt", "/srv", "/data", "/mnt", "/media") {
		if r == "" {
			continue
		}
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			roots = append(roots, r)
		}
	}
	return roots
}

// DirHit is one folder match for the "@" picker.
type DirHit struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

// Search matches q (case-insensitive) against the indexed directories.
// An empty q returns the top-level folders of every drive root. The second
// return value is true while the first build is still running (results may
// then be partial).
func (ix *DirIndex) Search(q string, limit int) (hits []DirHit, indexing bool) {
	dirs, roots, ready := ix.snapshot()
	if !ready {
		select {
		case <-ix.first:
		case <-time.After(fsSearchWait):
		}
		dirs, roots, ready = ix.snapshot()
	}
	indexing = !ready
	q = strings.ToLower(strings.TrimSpace(q))

	type scored struct {
		hit   DirHit
		score int
	}
	var out []scored
	if q == "" {
		for _, d := range dirs {
			if _, top := roots[filepath.Dir(d)]; top {
				out = append(out, scored{makeHit(d), 0})
			}
		}
	} else {
		for _, d := range dirs {
			base := strings.ToLower(filepath.Base(d))
			var score int
			switch {
			case base == q:
				score = 10000
			case strings.HasPrefix(base, q):
				score = 9000
			case strings.Contains(base, q):
				score = 8000 - strings.Index(base, q)*100
			case strings.Contains(strings.ToLower(d), q):
				score = 1000
			default:
				continue
			}
			// Slightly demote deeper (longer) paths within a match class.
			out = append(out, scored{makeHit(d), score - len(d)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].hit.Path < out[j].hit.Path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	hits = make([]DirHit, 0, len(out))
	for _, s := range out {
		hits = append(hits, s.hit)
	}
	return hits, indexing
}

func (ix *DirIndex) snapshot() (dirs []string, roots map[string]struct{}, ready bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.dirs, ix.roots, ix.ready
}

func makeHit(d string) DirHit {
	return DirHit{Path: d, Name: filepath.Base(d), Parent: filepath.Dir(d)}
}
