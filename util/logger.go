package util

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/user/uclaw/config"
)

var Log *slog.Logger

// defaultHandler is the original slog handler
var defaultHandler slog.Handler

const maxLogSize = 50 * 1024 * 1024 // 50MB

func SetupLogger() {
	// Save the default handler for later restoration
	defaultHandler = slog.Default().Handler()

	Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	// Also set as default logger
	slog.SetDefault(Log)
}

// SetupFileLogger sets up a logger that writes to a file only (no stderr)
func SetupFileLogger(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Can't log error since we're setting up logger
		return
	}
	Log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(Log)
}

// SetupDefaultFileLogger sets up a logger that writes to ~/.ucode/agent.log
// with automatic rotation when file exceeds 50MB
func SetupDefaultFileLogger() {
	dir, err := config.GetConfigDir()
	if err != nil {
		// Fallback to stderr if can't get config dir
		SetupLogger()
		return
	}
	logPath := filepath.Join(dir, "agent.log")
	rotateLogFile(logPath)
	SetupFileLogger(logPath)
}

// rotateLogFile checks if log file exceeds maxLogSize and deletes it if so
func rotateLogFile(path string) {
	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist or other error, no rotation needed
		return
	}
	if info.Size() > maxLogSize {
		os.Remove(path)
	}
}

// DisableLogger disables all log output (for TUI mode)
func DisableLogger() {
	discardHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	Log = slog.New(discardHandler)
	slog.SetDefault(Log)
}
