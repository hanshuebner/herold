package archiveretention

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

// Default option values applied when callers leave fields zero. Mirrors
// internal/trashretention's own defaults.
const (
	// DefaultSweepInterval is the cadence at which the worker scans
	// mailing lists with an archive configured. 1 hour matches
	// trashretention's own default; retention bounds here are day/count
	// grained, so sub-hour precision is not needed.
	DefaultSweepInterval = 60 * time.Minute

	// MinSweepInterval is the floor below which configuration is treated
	// as the default.
	MinSweepInterval = 5 * time.Second

	// DefaultBatchSize bounds one sweep's hard-deletes so we do not pin a
	// writer transaction.
	DefaultBatchSize = 500

	// MaxBatchSize is the operator-visible ceiling on per-sweep throughput.
	MaxBatchSize = 10000

	// listListPage is the page size used when iterating mailing lists.
	listListPage = 256

	// listMessagePage is the page size used when paging aged-out or
	// excess-count archive messages.
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
	// SweepInterval is the cadence between sweeps. Below MinSweepInterval
	// is treated as the default.
	SweepInterval time.Duration
	// BatchSize is the per-sweep hard-delete ceiling. 0 / negative ->
	// default; values above MaxBatchSize are clamped.
	BatchSize int
}

// Worker is the archive retention sweeper loop (REQ-MLIST-74). Construct
// with NewWorker; call Run(ctx) in a managed goroutine.
type Worker struct {
	store    store.Store
	logger   *slog.Logger
	clock    clock.Clock
	interval time.Duration
	batch    int
	running  atomic.Bool
	deleted  atomic.Uint64
}

// NewWorker constructs a Worker with the supplied options.
func NewWorker(opts Options) *Worker {
	observe.RegisterArchiveretentionMetrics()
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
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
		store:    opts.Store,
		logger:   logger,
		clock:    clk,
		interval: interval,
		batch:    batch,
	}
}

// Deleted returns the cumulative count of archive messages hard-deleted
// by this worker. Used by tests and the metrics exporter.
func (w *Worker) Deleted() uint64 { return w.deleted.Load() }

// SweepInterval returns the resolved sweep interval (post-defaulting).
func (w *Worker) SweepInterval() time.Duration { return w.interval }

// BatchSize returns the resolved batch ceiling (post-defaulting).
func (w *Worker) BatchSize() int { return w.batch }

// Run drives the sweep loop until ctx is cancelled. Each tick locates
// every list with an archive mailbox and enforces its configured
// age/count retention bound. Returns nil on ctx cancellation; non-nil
// only on an unrecoverable store failure. Run is single-goroutine; a
// second concurrent invocation returns an error.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil {
		return errors.New("archiveretention: nil Store")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("archiveretention: worker already running")
	}
	defer w.running.Store(false)

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

// Tick performs one sweep across every mailing list with an archive
// configured. Returns the number of archive messages hard-deleted.
// Exported so tests can drive the sweeper deterministically.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	start := w.clock.Now()
	defer func() {
		observe.ArchiveretentionSweepsTotal.Inc()
		observe.ArchiveretentionSweepDurationSeconds.Observe(w.clock.Now().Sub(start).Seconds())
	}()
	now := start.UTC()
	deleted := 0
	if err := w.sweepAllLists(ctx, now, &deleted); err != nil {
		return deleted, err
	}
	if deleted > 0 {
		w.logger.LogAttrs(ctx, slog.LevelInfo, "archiveretention: swept",
			slog.Int("deleted", deleted),
			slog.String("activity", "system"),
		)
	}
	return deleted, nil
}

// sweepAllLists iterates every mailing_list row and sweeps each one that
// has an archive mailbox configured.
func (w *Worker) sweepAllLists(ctx context.Context, now time.Time, deleted *int) error {
	var afterID store.MailingListID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lists, err := w.store.Meta().ListMailingLists(ctx, store.MailingListFilter{
			AfterID: afterID, Limit: listListPage,
		})
		if err != nil {
			return fmt.Errorf("archiveretention: list mailing lists: %w", err)
		}
		if len(lists) == 0 {
			return nil
		}
		for _, ml := range lists {
			afterID = ml.ID
			if ml.ArchiveMailboxID == nil {
				continue
			}
			if err := w.sweepListArchive(ctx, ml, now, deleted); err != nil {
				return err
			}
			if *deleted >= w.batch {
				return nil
			}
		}
		if len(lists) < listListPage {
			return nil
		}
	}
}

// sweepListArchive enforces ml's age bound (ArchiveRetentionDays) and
// count bound (ArchiveRetentionMaxMessages) against its archive mailbox.
// Either, both, or neither may be set (0 means unbounded); a list with
// neither set is visited (the ListMailingLists page read is unavoidable)
// but does no work.
func (w *Worker) sweepListArchive(ctx context.Context, ml store.MailingList, now time.Time, deleted *int) error {
	mbID := *ml.ArchiveMailboxID
	if ml.ArchiveRetentionDays > 0 {
		cutoff := now.Add(-time.Duration(ml.ArchiveRetentionDays) * 24 * time.Hour)
		if err := w.sweepByAge(ctx, mbID, cutoff, deleted); err != nil {
			return err
		}
		if *deleted >= w.batch {
			return nil
		}
	}
	if ml.ArchiveRetentionMaxMessages > 0 {
		if err := w.sweepByCount(ctx, mbID, ml.ArchiveRetentionMaxMessages, deleted); err != nil {
			return err
		}
	}
	return nil
}

// sweepByAge pages through mbID's archive and expunges messages whose
// InternalDate is strictly before cutoff, mirroring
// trashretention.Worker.sweepTrashMailbox.
func (w *Worker) sweepByAge(ctx context.Context, mbID store.MailboxID, cutoff time.Time, deleted *int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msgs, err := w.store.Meta().ListMessages(ctx, mbID, store.MessageFilter{
			Limit:          listMessagePage,
			ReceivedBefore: &cutoff,
		})
		if err != nil {
			return fmt.Errorf("archiveretention: list aged-out messages in mailbox %d: %w", mbID, err)
		}
		if len(msgs) == 0 {
			return nil
		}
		if err := w.expunge(ctx, mbID, msgs, deleted); err != nil {
			return err
		}
		if *deleted >= w.batch || len(msgs) < listMessagePage {
			return nil
		}
	}
}

// sweepByCount uses the O(1) CountMessages aggregate (never a full-
// mailbox scan, REQ-STORE-06/07: large-mailbox target applies equally to
// large archives) to find how many messages exceed maxMessages, then
// expunges exactly that many of the OLDEST messages -- ListMessages
// without AfterUID returns ascending-UID order, and every archive
// message is appended (never reordered), so ascending UID is ascending
// post age.
func (w *Worker) sweepByCount(ctx context.Context, mbID store.MailboxID, maxMessages int64, deleted *int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		total, _, err := w.store.Meta().CountMessages(ctx, mbID)
		if err != nil {
			return fmt.Errorf("archiveretention: count messages in mailbox %d: %w", mbID, err)
		}
		excess := total - maxMessages
		if excess <= 0 {
			return nil
		}
		page := int64(listMessagePage)
		if excess < page {
			page = excess
		}
		if remaining := int64(w.batch - *deleted); remaining < page {
			page = remaining
		}
		if page <= 0 {
			return nil
		}
		msgs, err := w.store.Meta().ListMessages(ctx, mbID, store.MessageFilter{Limit: int(page)})
		if err != nil {
			return fmt.Errorf("archiveretention: list oldest messages in mailbox %d: %w", mbID, err)
		}
		if len(msgs) == 0 {
			return nil
		}
		if err := w.expunge(ctx, mbID, msgs, deleted); err != nil {
			return err
		}
		if *deleted >= w.batch {
			return nil
		}
	}
}

// expunge hard-deletes msgs from mbID and updates the deleted counters.
func (w *Worker) expunge(ctx context.Context, mbID store.MailboxID, msgs []store.Message, deleted *int) error {
	ids := make([]store.MessageID, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if err := w.store.Meta().ExpungeMessages(ctx, mbID, ids); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("archiveretention: expunge mailbox %d: %w", mbID, err)
	}
	n := len(ids)
	*deleted += n
	w.deleted.Add(uint64(n))
	observe.ArchiveretentionMessagesDeletedTotal.Add(float64(n))
	return nil
}
