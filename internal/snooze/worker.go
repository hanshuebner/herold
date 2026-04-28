package snooze

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Default option values applied when callers leave fields zero.
const (
	// DefaultPollInterval is the cadence at which the worker scans for
	// due-snoozed messages. 60 s matches the JMAP draft's
	// recommendation: snooze precision is minute-grained and a slower
	// poll lets the partial index do its job without piling work.
	DefaultPollInterval = 60 * time.Second

	// MinPollInterval is the floor below which configuration is
	// rejected; sub-5-second polling against the messages table is a
	// configuration mistake, not a feature.
	MinPollInterval = 5 * time.Second

	// DefaultBatchSize is the per-tick batch ceiling. A snooze release
	// is one SetSnooze tx per message; 256 keeps each tick bounded and
	// observable in the logger.
	DefaultBatchSize = 256

	// MaxBatchSize is the operator-visible ceiling enforced by the
	// store and re-asserted here for clarity.
	MaxBatchSize = 10000
)

// Options configures Worker. Zero fields fall back to defaults.
type Options struct {
	// Store is the metadata source. Required.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock; nil falls back to clock.NewReal.
	Clock clock.Clock
	// PollInterval is the cadence between sweeps. Below MinPollInterval
	// is treated as the default; this matches sysconfig validation
	// which rejects sub-5-second values at parse time.
	PollInterval time.Duration
	// BatchSize is the per-tick ceiling. 0 / negative -> default;
	// values above MaxBatchSize are clamped.
	BatchSize int
}

// Worker is the snooze wake-up loop. Construct with NewWorker; call
// Run(ctx) in a managed goroutine.
type Worker struct {
	store    store.Store
	logger   *slog.Logger
	clock    clock.Clock
	poll     time.Duration
	batch    int
	running  atomic.Bool
	released atomic.Uint64
}

// NewWorker constructs a Worker with the supplied options. Defaults
// are applied at construction time so the worker exposes a stable
// PollInterval / BatchSize for tests.
func NewWorker(opts Options) *Worker {
	observe.RegisterSnoozeMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	poll := opts.PollInterval
	if poll < MinPollInterval {
		poll = DefaultPollInterval
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	if batch > MaxBatchSize {
		batch = MaxBatchSize
	}
	return &Worker{
		store:  opts.Store,
		logger: logger,
		clock:  clk,
		poll:   poll,
		batch:  batch,
	}
}

// Released returns the cumulative count of messages released by this
// worker. Used by tests and by the metrics exporter.
func (w *Worker) Released() uint64 { return w.released.Load() }

// PollInterval returns the resolved poll interval (post-defaulting).
// Exposed for tests.
func (w *Worker) PollInterval() time.Duration { return w.poll }

// BatchSize returns the resolved batch ceiling (post-defaulting).
// Exposed for tests.
func (w *Worker) BatchSize() int { return w.batch }

// Run drives the wake-up loop until ctx is cancelled. Each tick
// calls ListDueSnoozedMessages and then SetSnooze(nil) for every
// returned row; when a tick releases a full batch the next tick
// fires immediately so backlogs drain without waiting a full poll
// interval.
//
// Returns nil on ctx cancellation; non-nil only on an unrecoverable
// store failure (the lifecycle errgroup logs and triggers shutdown).
// Run is single-goroutine; a second concurrent invocation returns an
// error.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil {
		return errors.New("snooze: nil Store")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("snooze: worker already running")
	}
	defer w.running.Store(false)

	// First tick fires immediately so a server restart does not wait
	// PollInterval before catching up on snoozes that came due during
	// downtime.
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		processed, err := w.tick(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		// If we processed a full batch, more rows may be due — loop
		// without sleeping. Otherwise wait one poll interval.
		if processed >= w.batch {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.clock.After(w.poll):
		}
	}
}

// tick performs one sweep. Returns the number of messages released.
func (w *Worker) tick(ctx context.Context) (int, error) {
	start := w.clock.Now()
	defer func() {
		observe.SnoozeSweepsTotal.Inc()
		observe.SnoozeSweepDurationSeconds.Observe(w.clock.Now().Sub(start).Seconds())
	}()
	now := start.UTC()
	due, err := w.store.Meta().ListDueSnoozedMessages(ctx, now, w.batch)
	if err != nil {
		return 0, fmt.Errorf("snooze: list due: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}
	for _, msg := range due {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, err := w.store.Meta().SetSnooze(ctx, msg.ID, msg.MailboxID, nil); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Message was expunged between list and set; skip.
				continue
			}
			return 0, fmt.Errorf("snooze: clear %d: %w", msg.ID, err)
		}
		w.released.Add(1)
		observe.SnoozeMessagesWokenTotal.Inc()
	}
	w.logger.LogAttrs(ctx, slog.LevelInfo, "snooze: released batch",
		slog.Int("count", len(due)),
		slog.Time("now", now),
	)
	return len(due), nil
}
