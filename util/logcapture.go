package util

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LogEntry represents a single captured log entry
type LogEntry struct {
	Time    time.Time
	Level   string
	Message string
	Attrs   string
}

func (e LogEntry) String() string {
	ts := e.Time.Format("15:04:05")
	if e.Attrs != "" {
		return fmt.Sprintf("[%s] %s %s %s", ts, e.Level, e.Message, e.Attrs)
	}
	return fmt.Sprintf("[%s] %s %s", ts, e.Level, e.Message)
}

// LogCapture captures slog output to a channel for TUI display
type LogCapture struct {
	ch     chan LogEntry
	cancel context.CancelFunc
	logger *slog.Logger
}

// NewLogCapture creates a new log capture instance and sets it as the default slog handler
func NewLogCapture(bufferSize int) *LogCapture {
	lc := &LogCapture{
		ch: make(chan LogEntry, bufferSize),
	}
	return lc
}

// Start activates log capture by setting a custom slog handler
func (lc *LogCapture) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	lc.cancel = cancel

	handler := &channelHandler{
		ch:     lc.ch,
		ctx:    ctx,
		level:  slog.LevelInfo,
		attrs:  make([]slog.Attr, 0),
		group:  "",
		parent: nil,
	}
	lc.logger = slog.New(handler)
	slog.SetDefault(lc.logger)
}

// Stop deactivates log capture and restores default logging
func (lc *LogCapture) Stop() {
	if lc.cancel != nil {
		lc.cancel()
	}
	close(lc.ch)
}

// Ch returns the channel to read captured log entries from
func (lc *LogCapture) Ch() <-chan LogEntry {
	return lc.ch
}

// channelHandler implements slog.Handler to send logs to a channel
type channelHandler struct {
	ch     chan LogEntry
	ctx    context.Context
	level  slog.Level
	attrs  []slog.Attr
	group  string
	parent *channelHandler
}

func (h *channelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *channelHandler) Handle(_ context.Context, record slog.Record) error {
	if h.ctx.Err() != nil {
		return nil
	}

	var attrs strings.Builder
	// Write group prefix
	if h.group != "" {
		attrs.WriteString(h.group)
		attrs.WriteString(".")
	}
	// Write handler attrs
	for i, a := range h.attrs {
		if i > 0 {
			attrs.WriteString(" ")
		}
		attrs.WriteString(fmt.Sprintf("%s=%v", a.Key, a.Value))
	}
	// Write record attrs
	record.Attrs(func(a slog.Attr) bool {
		if attrs.Len() > 0 {
			attrs.WriteString(" ")
		}
		attrs.WriteString(fmt.Sprintf("%s=%v", a.Key, a.Value))
		return true
	})

	entry := LogEntry{
		Time:    record.Time,
		Level:   strings.ToUpper(record.Level.String()),
		Message: record.Message,
		Attrs:   attrs.String(),
	}

	// Non-blocking send
	select {
	case h.ch <- entry:
	default:
		// Drop if buffer is full
	}

	return nil
}

func (h *channelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Copy current attrs
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &channelHandler{
		ch:     h.ch,
		ctx:    h.ctx,
		level:  h.level,
		attrs:  newAttrs,
		group:  h.group,
		parent: h.parent,
	}
}

func (h *channelHandler) WithGroup(name string) slog.Handler {
	return &channelHandler{
		ch:     h.ch,
		ctx:    h.ctx,
		level:  h.level,
		attrs:  h.attrs,
		group:  name,
		parent: h,
	}
}
