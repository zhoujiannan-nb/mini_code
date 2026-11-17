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
	maxLogSize = 10 * 1024 * 1024
	logKeep = 2
)

func SetupLogger() {
	Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(Log)
}

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

func (w *rotatingWriter) rotateLocked() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	for i := w.keep - 1; i >= 1; i-- {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", w.path, i)); err == nil {
			os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
	}
	os.Rename(w.path, w.path+".1")
	w.size = 0
}

func (w *rotatingWriter) dropStaleLocked() {
	for i := 1; i <= w.keep+8; i++ {
		name := fmt.Sprintf("%s.%d", w.path, i)
		if _, err := os.Stat(name); err == nil && i > w.keep {
			os.Remove(name)
		}
	}
}

func SetupFileLogger(path string) {
	w := newRotatingWriter(path, maxLogSize, logKeep)
	Log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(Log)
}

func SetupDefaultFileLogger() {
	dir, err := config.GetConfigDir()
	if err != nil {
		SetupLogger()
		return
	}
	SetupFileLogger(filepath.Join(dir, "agent.log"))
}
