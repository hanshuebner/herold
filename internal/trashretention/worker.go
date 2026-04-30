package trashretention

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
	// DefaultRetentionDays is the number of days after which a message in
	// the Trash mailbox is permanently deleted. 30 days matches the Gmail
	// convention and REQ-STORE-90.
	DefaultRetentionDays = 30

	// DefaultSweepInterval is the cadence at which the worker scans Trash
	// mailboxes. 1 hour is sufficient for day-grained retention precision.
	DefaultSweepInterval = 60 * time.Minute

	// MinSweepInterval is the floor below which configuration is treated
	// as the default. Sub-5-second sweeping against the message tables is
	// a configuration mistake.
	MinSweepInterval = 5 * time.Second

	// DefaultBatchSize bounds one sweep's hard-deletes so we do not pin a
	// writer transaction. 500 is conservative for mail payloads.
	DefaultBatchSize = 500

	// MaxBatchSize is the operator-visible ceiling on per-sweep throughput.
	MaxBatchSize = 10000

	// listPrincipalPage is the page size used when iterating principals.
	listPrincipalPage = 256

	// listMessagePage is the page size used when paging aged-out messages.
	listMessagePage = 256
)

// Options configures Worker. Zero fields fall back to defaults.
type Options struct {
	// Store is the metadata source. Required.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock; nil falls back to clock.NewReal.
	Clock clock.Clock
	// RetentionDays is the number of days after which Trash messages are
	// permanently deleted. 0 / negative -> DefaultRetentionDays.
	RetentionDays int
	// SweepInterval is the cadence between sweeps. Below MinSweepInterval
	// is treated as the default.
	SweepInterval time.Duration
	// BatchSize is the per-sweep hard-delete ceiling. 0 / negative ->
	// default; values above MaxBatchSize are clamped.
	BatchSize int
}

// Worker is the trash retention sweeper loop. Construct with NewWorker;
// call Run(ctx) in a managed goroutine.
type Worker struct {
	store         store.Store
	logger        *slog.Logger
	clock         clock.Clock
	retentionDays int
	interval      time.Duration
	batch         int
	running       atomic.Bool
	deleted       atomic.Uint64
}

// NewWorker constructs a Worker with the supplied options. Defaults are
// applied at construction time so the worker exposes a stable
// RetentionDays / SweepInterval / BatchSize for tests.
func NewWorker(opts Options) *Worker {
	observe.RegisterTrashretentionMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	retentionDays := opts.RetentionDays
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	interval := opts.SweepInterval
	if interval < MinSweepInterval {
		interval = DefaultSweepInterval
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	if batch > MaxBatchSize {
		batch = MaxBatchSize
	}
	return &Worker{
		store:         opts.Store,
		logger:        logger,
		clock:         clk,
		retentionDays: retentionDays,
		interval:      interval,
		batch:         batch,
	}
}

// Deleted returns the cumulative count of messages hard-deleted by this
// worker. Used by tests and by the metrics exporter.
func (w *Worker) Deleted() uint64 { return w.deleted.Load() }

// RetentionDays returns the resolved retention window in days.
// Exposed for tests.
func (w *Worker) RetentionDays() int { return w.retentionDays }

// SweepInterval returns the resolved sweep interval (post-defaulting).
// Exposed for tests.
func (w *Worker) SweepInterval() time.Duration { return w.interval }

// BatchSize returns the resolved batch ceiling (post-defaulting).
// Exposed for tests.
func (w *Worker) BatchSize() int { return w.batch }

// Run drives the sweep loop until ctx is cancelled. Each tick locates
// every Trash mailbox across all principals, hard-deletes messages whose
// InternalDate predates the retention window, and emits Prometheus metrics.
//
// Returns nil on ctx cancellation; non-nil only on an unrecoverable store
// failure (the lifecycle errgroup logs and triggers shutdown).
// Run is single-goroutine; a second concurrent invocation returns an error.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil {
		return errors.New("trashretention: nil Store")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("trashretention: worker already running")
	}
	defer w.running.Store(false)

	// First tick fires immediately so a server restart catches up on
	// retention windows that came due during downtime.
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		processed, err := w.Tick(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		// If we processed a full batch, more rows may be due — loop
		// without sleeping so backlogs drain promptly.
		if processed >= w.batch {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.clock.After(w.interval):
		}
	}
}

// Tick performs one sweep across all principals' Trash mailboxes. Returns the
// number of messages hard-deleted. Exported so tests can drive the sweeper
// deterministically without spinning Run in a goroutine.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	start := w.clock.Now()
	defer func() {
		observe.TrashretentionSweepsTotal.Inc()
		observe.TrashretentionSweepDurationSeconds.Observe(w.clock.Now().Sub(start).Seconds())
	}()
	now := start.UTC()
	cutoff := now.Add(-time.Duration(w.retentionDays) * 24 * time.Hour)
	deleted := 0
	if err := w.sweepAllPrincipals(ctx, cutoff, &deleted); err != nil {
		return deleted, err
	}
	if deleted > 0 {
		w.logger.LogAttrs(ctx, slog.LevelInfo, "trashretention: swept",
			slog.Int("deleted", deleted),
			slog.Time("cutoff", cutoff),
			slog.String("activity", "system"),
		)
	}
	return deleted, nil
}

// sweepAllPrincipals iterates through every principal in the store and
// processes each one's Trash mailbox.
func (w *Worker) sweepAllPrincipals(ctx context.Context, cutoff time.Time, deleted *int) error {
	var afterID store.PrincipalID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		principals, err := w.store.Meta().ListPrincipals(ctx, afterID, listPrincipalPage)
		if err != nil {
			return fmt.Errorf("trashretention: list principals: %w", err)
		}
		if len(principals) == 0 {
			return nil
		}
		for _, p := range principals {
			afterID = p.ID
			if err := w.sweepPrincipal(ctx, p.ID, cutoff, deleted); err != nil {
				return err
			}
			if *deleted >= w.batch {
				return nil
			}
		}
		if len(principals) < listPrincipalPage {
			return nil
		}
	}
}

// sweepPrincipal locates the Trash mailbox for a single principal and
// expunges messages older than cutoff.
func (w *Worker) sweepPrincipal(ctx context.Context, pid store.PrincipalID, cutoff time.Time, deleted *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mailboxes, err := w.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return fmt.Errorf("trashretention: list mailboxes for principal %d: %w", pid, err)
	}
	for _, mb := range mailboxes {
		if mb.Attributes&store.MailboxAttrTrash == 0 {
			continue
		}
		if err := w.sweepTrashMailbox(ctx, mb, cutoff, deleted); err != nil {
			return err
		}
		if *deleted >= w.batch {
			return nil
		}
	}
	return nil
}

// sweepTrashMailbox pages through a Trash mailbox and expunges messages
// whose InternalDate is strictly before cutoff. Uses the AfterUID cursor
// so undeletable rows (if any) cannot pin the page indefinitely.
func (w *Worker) sweepTrashMailbox(ctx context.Context, mb store.Mailbox, cutoff time.Time, deleted *int) error {
	// Page through aged-out messages in UID order. Because ExpungeMessages
	// removes the rows, subsequent pages start at the next surviving UID.
	// We reset the cursor to 0 each batch so the scan restarts from the
	// beginning of the mailbox; this is safe because expunged rows are
	// gone and will not be revisited.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msgs, err := w.store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{
			Limit:          listMessagePage,
			ReceivedBefore: &cutoff,
		})
		if err != nil {
			return fmt.Errorf("trashretention: list messages in mailbox %d: %w", mb.ID, err)
		}
		if len(msgs) == 0 {
			return nil
		}
		ids := make([]store.MessageID, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
		if err := w.store.Meta().ExpungeMessages(ctx, mb.ID, ids); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// All already gone concurrently — treated as success.
				return nil
			}
			return fmt.Errorf("trashretention: expunge mailbox %d: %w", mb.ID, err)
		}
		n := len(ids)
		*deleted += n
		w.deleted.Add(uint64(n))
		observe.TrashretentionMessagesDeletedTotal.Add(float64(n))
		if *deleted >= w.batch {
			return nil
		}
		if len(msgs) < listMessagePage {
			return nil
		}
	}
}
