package cursors_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/cursors"
)

// logBuffer is a slog handler that captures log records as strings.
type logBuffer struct {
	buf bytes.Buffer
}

func (l *logBuffer) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (l *logBuffer) Handle(_ context.Context, r slog.Record) error {
	l.buf.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		l.buf.WriteByte(' ')
		l.buf.WriteString(a.Key)
		l.buf.WriteByte('=')
		l.buf.WriteString(a.Value.String())
		return true
	})
	l.buf.WriteByte('\n')
	return nil
}

func (l *logBuffer) WithAttrs(attrs []slog.Attr) slog.Handler { return l }
func (l *logBuffer) WithGroup(name string) slog.Handler       { return l }

func newLogger() (*slog.Logger, *logBuffer) {
	lb := &logBuffer{}
	return slog.New(lb), lb
}

// TestShutdownFlusher_ZeroCursorSkip verifies that Flush is a no-op when
// Get returns 0 — i.e. the worker never advanced its cursor.
func TestShutdownFlusher_ZeroCursorSkip(t *testing.T) {
	putCalled := false
	logger, _ := newLogger()
	f := cursors.ShutdownFlusher{
		Get:       func() uint64 { return 0 },
		Put:       func(_ context.Context, _ uint64) error { putCalled = true; return nil },
		Logger:    logger,
		Subsystem: "test",
	}
	f.Flush()
	if putCalled {
		t.Fatal("Put should not be called when cursor is 0")
	}
}

// TestShutdownFlusher_SuccessfulFlush verifies that Flush calls Put with
// the sequence number returned by Get and does not log anything on success.
func TestShutdownFlusher_SuccessfulFlush(t *testing.T) {
	var written uint64
	logger, lb := newLogger()
	f := cursors.ShutdownFlusher{
		Get:       func() uint64 { return 42 },
		Put:       func(_ context.Context, seq uint64) error { written = seq; return nil },
		Logger:    logger,
		Subsystem: "test",
	}
	f.Flush()
	if written != 42 {
		t.Fatalf("Put called with seq %d, want 42", written)
	}
	if lb.buf.Len() != 0 {
		t.Fatalf("unexpected log output: %s", lb.buf.String())
	}
}

// TestShutdownFlusher_StoreErrorLoggedAndAbsorbed verifies that a Put
// failure is logged at warn level and does not propagate (the method has
// no return value, and Flush must return normally).
func TestShutdownFlusher_StoreErrorLoggedAndAbsorbed(t *testing.T) {
	logger, lb := newLogger()
	f := cursors.ShutdownFlusher{
		Get:       func() uint64 { return 7 },
		Put:       func(_ context.Context, _ uint64) error { return errors.New("db gone") },
		Logger:    logger,
		Subsystem: "myworker",
	}
	f.Flush() // must not panic or return an error
	out := lb.buf.String()
	if !strings.Contains(out, "myworker: persist cursor on shutdown") {
		t.Fatalf("expected log message not found; got: %s", out)
	}
	if !strings.Contains(out, "db gone") {
		t.Fatalf("expected error text in log; got: %s", out)
	}
}

// TestShutdownFlusher_FreshContext verifies that Flush does not inherit a
// cancelled parent context — Put receives a live context even when the
// caller's context is already cancelled. We check that the Put context has
// a non-zero deadline (not context.Background, which has no deadline but
// is never cancelled) so we know it was derived from context.Background
// with a timeout rather than from any cancelled ancestor.
func TestShutdownFlusher_FreshContext(t *testing.T) {
	var hadDeadline bool
	var deadlineInFuture bool
	logger, _ := newLogger()
	f := cursors.ShutdownFlusher{
		Get: func() uint64 { return 1 },
		Put: func(ctx context.Context, _ uint64) error {
			// Check the deadline inside Put, before defer cancel() fires.
			dl, ok := ctx.Deadline()
			hadDeadline = ok
			deadlineInFuture = ok && dl.After(time.Now())
			return nil
		},
		Logger:    logger,
		Subsystem: "test",
	}
	f.Flush()

	if !hadDeadline {
		t.Fatal("Put context had no deadline; expected a fresh context with default timeout")
	}
	if !deadlineInFuture {
		t.Fatal("Put context deadline was in the past; expected a live fresh context")
	}
}

// TestShutdownFlusher_DefaultTimeout verifies that when Timeout is zero
// the flush context has a deadline set (i.e. the 5s default is applied).
func TestShutdownFlusher_DefaultTimeout(t *testing.T) {
	var deadlineSet bool
	logger, _ := newLogger()
	f := cursors.ShutdownFlusher{
		Get: func() uint64 { return 1 },
		Put: func(ctx context.Context, _ uint64) error {
			dl, ok := ctx.Deadline()
			deadlineSet = ok && !dl.IsZero()
			return nil
		},
		Logger:    logger,
		Subsystem: "test",
		Timeout:   0, // use default
	}
	f.Flush()
	if !deadlineSet {
		t.Fatal("expected a deadline on the flush context when Timeout is 0")
	}
}

// TestShutdownFlusher_CustomTimeout verifies that an explicit Timeout is
// used rather than the 5s default.
func TestShutdownFlusher_CustomTimeout(t *testing.T) {
	const want = 100 * time.Millisecond
	logger, _ := newLogger()
	var got time.Duration
	f := cursors.ShutdownFlusher{
		Get: func() uint64 { return 1 },
		Put: func(ctx context.Context, _ uint64) error {
			dl, ok := ctx.Deadline()
			if ok {
				got = time.Until(dl)
			}
			return nil
		},
		Logger:    logger,
		Subsystem: "test",
		Timeout:   want,
	}
	before := time.Now()
	f.Flush()
	_ = before
	// The deadline should be <= want from now (it was set before the Put call).
	if got > want {
		t.Fatalf("flush deadline %v exceeds custom timeout %v", got, want)
	}
	if got <= 0 {
		t.Fatal("flush context had no positive remaining time")
	}
}

// TestShutdownFlusher_AtomicCursor exercises the common pattern where Get
// wraps an atomic.Uint64, confirming the flusher reads the latest value.
func TestShutdownFlusher_AtomicCursor(t *testing.T) {
	var cur atomic.Uint64
	cur.Store(99)
	var written uint64
	logger, _ := newLogger()
	f := cursors.ShutdownFlusher{
		Get:       cur.Load,
		Put:       func(_ context.Context, seq uint64) error { written = seq; return nil },
		Logger:    logger,
		Subsystem: "test",
	}
	f.Flush()
	if written != 99 {
		t.Fatalf("got %d, want 99", written)
	}
}
