package cursors

import (
	"context"
	"log/slog"
	"time"
)

const defaultTimeout = 5 * time.Second

// ShutdownFlusher flushes an in-memory cursor to a durable store on
// worker shutdown using a fresh, short-deadline context so the flush
// itself is not cancelled by the same signal that ended the loop.
type ShutdownFlusher struct {
	// Get returns the latest in-memory cursor sequence number.
	Get func() uint64
	// Put persists seq to the durable store.
	Put func(ctx context.Context, seq uint64) error
	// Logger receives a warn-level record on Put failure.
	Logger *slog.Logger
	// Subsystem is included in the log message (e.g. "webpush").
	Subsystem string
	// Timeout is the deadline for the flush context. Zero uses 5s.
	Timeout time.Duration
}

// Flush writes the current in-memory cursor to the store using a fresh
// context. Safe for use in a defer in Run. Logs and absorbs errors so
// the worker's shutdown path is never interrupted.
func (f ShutdownFlusher) Flush() {
	seq := f.Get()
	if seq == 0 {
		return
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := f.Put(ctx, seq); err != nil {
		f.Logger.LogAttrs(ctx, slog.LevelWarn, f.Subsystem+": persist cursor on shutdown",
			slog.Uint64("seq", seq),
			slog.String("err", err.Error()),
		)
	}
}
