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
	fsIndexMaxDirs = 250000
	fsIndexRefresh = 15 * time.Minute
	fsSearchWait   = 8 * time.Second
)

var skipDirNames = map[string]struct{}{
	"node_modules": {}, ".git": {}, ".svn": {}, ".hg": {},
	"__pycache__": {}, ".tox": {}, ".mypy_cache": {}, ".pytest_cache": {},
	".venv": {}, "venv": {}, ".idea": {}, ".vs": {},
	".gradle": {}, ".cargo": {}, ".rustup": {}, ".npm": {}, ".cache": {},
	"appdata": {}, "windows": {}, "$recycle.bin": {},
	"system volume information": {}, "recovery": {}, "perflogs": {},
	"library": {},
}

type DirIndex struct {
	mu      sync.RWMutex
	dirs    []string
	roots   map[string]struct{}
	ready   bool
	builtAt time.Time
	first   chan struct{}
}

func NewDirIndex() *DirIndex {
	return &DirIndex{first: make(chan struct{})}
}

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
			return nil
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

type DirHit struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

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
