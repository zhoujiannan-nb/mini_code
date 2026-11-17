package util

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/user/mini_code/config"
)

var Log *slog.Logger

const (
	// maxLogSize caps the active log file. When a write would cross it, the
	// chain shifts (agent.log → agent.log.1 → agent.log.2, oldest dropped),
	// so the folder holds at most (keep+1) log files of ~10MB each.
	maxLogSize = 10 * 1024 * 1024 // 10MB
	// logKeep is how many numbered backups survive a rotation.
	logKeep = 2
)

func SetupLogger() {
	Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	// Also set as default logger
	slog.SetDefault(Log)
}

// rotatingWriter is an io.Writer that keeps the log file under max size: the
// write that would cross the cap first shifts the backup chain — path →
// path.1 → … → path.keep, anything older deleted — and opens a fresh active
// file. Dependency-free on purpose; the mutex doubles as the serializer for
// slog's concurrent Emit calls. A rotation error (Windows rename race with a
// second instance) is ignored: the worst case is one lost backup generation,
// the active log keeps working.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	size int64
	file *os.File
}

func newRotatingWriter(path string, max int64, keep int) *rotatingWriter {
	w := &rotatingWriter{path: path, max: max, keep: keep}
	if info, err := os.Stat(path); err == nil {
		w.size = info.Size()
	}
	if w.size > max {
		// An over-cap leftover (e.g. written before the cap existed) is
		// demoted now, not on the next write.
		w.rotateLocked()
	}
	w.dropStaleLocked()
	return w
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.max {
		w.rotateLocked()
	}
	if w.file == nil {
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return 0, err
		}
		w.file = f
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotateLocked shifts the chain and resets the active file. Callers hold mu.
func (w *rotatingWriter) rotateLocked() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	// Shift high to low: .1 → .2 (overwriting the old .2), … , base → .1.
	// os.Rename replaces an existing destination on Windows and POSIX alike.
	for i := w.keep - 1; i >= 1; i-- {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", w.path, i)); err == nil {
			os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
	}
	os.Rename(w.path, w.path+".1") // the active file may not exist yet
	w.size = 0
}

// dropStaleLocked deletes chain files past the keep count. Backups larger
// than the cap (written before the cap existed, or demoted from an over-cap
// base) are NOT deleted here — deleting a user's history on startup is too
// aggressive; they age out of the chain within at most keep more rotations.
// A bounded scan covers chains with holes without looping on missing entries.
func (w *rotatingWriter) dropStaleLocked() {
	for i := 1; i <= w.keep+8; i++ {
		name := fmt.Sprintf("%s.%d", w.path, i)
		if _, err := os.Stat(name); err == nil && i > w.keep {
			os.Remove(name)
		}
	}
}

// SetupFileLogger sets up a logger that writes to a file only (no stderr).
// The active file is capped at maxLogSize with logKeep backup generations
// kept beside it (path.1 newest, path.keep oldest).
func SetupFileLogger(path string) {
	w := newRotatingWriter(path, maxLogSize, logKeep)
	Log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(Log)
}

// SetupDefaultFileLogger sets up a logger that writes to ~/.mini_code/agent.log
// with size-based rotation: 10MB per file, agent.log.1/.2 kept as history.
func SetupDefaultFileLogger() {
	dir, err := config.GetConfigDir()
	if err != nil {
		// Fallback to stderr if can't get config dir
		SetupLogger()
		return
	}
	SetupFileLogger(filepath.Join(dir, "agent.log"))
}
