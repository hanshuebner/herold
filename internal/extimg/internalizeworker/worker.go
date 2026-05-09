// Package internalizeworker drains the importer-flagged
// internalize_pending backlog out of the JMAP request path
// (REQ-EXTIMG-BG-01..09).
//
// The worker runs newest-first via the descending sweep over rows
// flagged with internalize_pending = 1. New rows wake the loop on a
// buffered notify channel; an idle-poll safety net catches the case
// where the notification was dropped. Each Run goroutine processes at
// most Concurrency messages in parallel so external image origins
// are not hammered.
package internalizeworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/store"
)

// Options configures a Worker. Zero-value defaults are documented per
// field and make the worker usable in tests without ceremony.
type Options struct {
	// Concurrency caps the number of in-flight messages per batch.
	// Default 4. Each in-flight rewrite is bounded by the
	// extimg.Config.PerMessageTimeout already enforced inside
	// extimg.Internalize.
	Concurrency int
	// BatchSize is the upper bound on messages the worker pulls per
	// store round-trip. Default 64. A small batch keeps the
	// notify-driven cursor reset responsive on a fresh import wave.
	BatchSize int
	// IdlePollInterval is the safety-net interval for waking the
	// loop when no notification has fired (REQ-EXTIMG-BG-09).
	// Default 5 minutes.
	IdlePollInterval time.Duration
}

// Worker drains the internalize_pending backlog. Construct via New;
// drive via Run. Notify is safe to call from any goroutine and is
// idempotent across rapid bursts (the buffered channel collapses).
type Worker struct {
	store    store.Store
	cfg      extimg.Config
	logger   *slog.Logger
	clock    clock.Clock
	opts     Options
	notifyCh chan struct{}

	mu             sync.Mutex
	cursor         store.MessageID
	processedTotal uint64
}

// New constructs a Worker. logger and clk fall back to slog.Default()
// and clock.NewReal() when nil; opts default fields when zero.
func New(st store.Store, cfg extimg.Config, logger *slog.Logger, clk clock.Clock, opts Options) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	if opts.IdlePollInterval <= 0 {
		opts.IdlePollInterval = 5 * time.Minute
	}
	return &Worker{
		store:    st,
		cfg:      cfg,
		logger:   logger.With("subsystem", "extimg-worker"),
		clock:    clk,
		opts:     opts,
		notifyCh: make(chan struct{}, 1),
	}
}

// Notify wakes the worker so the next batch resets to "newest" and
// picks up freshly-arrived pending rows immediately. Non-blocking;
// duplicates are collapsed by the buffered channel.
func (w *Worker) Notify() {
	select {
	case w.notifyCh <- struct{}{}:
	default:
		// A wake is already pending; the worker will see this row
		// when it processes the queued notification.
	}
}

// Run drives the worker. Returns when ctx is cancelled. Idempotent
// across multiple invocations would race the cursor; only one Run
// goroutine should be live per Worker instance.
func (w *Worker) Run(ctx context.Context) {
	w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: started",
		slog.Int("concurrency", w.opts.Concurrency),
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Duration("idle_poll", w.opts.IdlePollInterval),
	)
	// First pass: drain whatever is pending at startup.
	w.resetCursor()
	for {
		empty := w.runBatch(ctx)
		if ctx.Err() != nil {
			w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: stopped",
				slog.Uint64("processed_total", w.snapshotProcessed()))
			return
		}
		if !empty {
			// Fall through and grab the next batch. Honor any
			// pending notification so a fresh InsertMessage that
			// arrived mid-batch resets the cursor before the next
			// pass.
			select {
			case <-w.notifyCh:
				w.resetCursor()
			default:
			}
			continue
		}
		// Batch was empty: park on notify or the safety-net tick.
		select {
		case <-ctx.Done():
			return
		case <-w.notifyCh:
			w.resetCursor()
		case <-w.clock.After(w.opts.IdlePollInterval):
			w.resetCursor()
		}
	}
}

// runBatch pulls one batch from the store, fans the work out to
// `Concurrency` goroutines, waits for completion, and advances the
// cursor. Returns true when the batch was empty (caller should park).
func (w *Worker) runBatch(ctx context.Context) bool {
	cursor := w.snapshotCursor()
	ids, err := w.store.Meta().ListMessagesWithInternalizePending(ctx, cursor, w.opts.BatchSize)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return true
		}
		w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: list pending failed",
			slog.String("err", err.Error()))
		return true
	}
	if len(ids) == 0 {
		return true
	}
	// Fan out. Use a small semaphore so we never exceed Concurrency
	// in flight; a per-message cancellation propagates from ctx.
	sem := make(chan struct{}, w.opts.Concurrency)
	var wg sync.WaitGroup
fanout:
	for _, id := range ids {
		select {
		case <-ctx.Done():
			break fanout
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(id store.MessageID) {
			defer wg.Done()
			defer func() { <-sem }()
			w.processOne(ctx, id)
		}(id)
	}
	wg.Wait()
	// Advance the cursor to the lowest id of the batch (the rows are
	// returned descending, so ids[len-1] is the lowest).
	w.advanceCursor(ids[len(ids)-1])
	return false
}

// processOne runs the rewrite for a single message. Errors are
// swallowed to the log; the cursor advances regardless so a poison
// blob never wedges the worker.
func (w *Worker) processOne(ctx context.Context, id store.MessageID) {
	w.bumpProcessed()
	m, err := w.store.Meta().GetMessage(ctx, id)
	if err != nil {
		// Message gone (expunged) — clear the flag if it still
		// exists, otherwise just move on.
		_ = w.store.Meta().ClearMessageInternalizePending(ctx, id)
		return
	}
	if !m.InternalizePending {
		// Already cleared by another path.
		return
	}
	if err := w.internalize(ctx, m); err != nil {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: rewrite failed",
			slog.Uint64("message_id", uint64(m.ID)),
			slog.String("err", err.Error()))
		// Clear the flag so the message does not re-queue forever
		// (REQ-EXTIMG-BG-01). The user sees the original body; the
		// failure is on the existing extimg metric.
		_ = w.store.Meta().ClearMessageInternalizePending(ctx, m.ID)
	}
}

// internalize loads the blob, runs extimg.Internalize, and replaces
// the body. Modelled on the legacy maybeInternalizeOnDemand path
// (REQ-EXTIMG-93..98) without the Email/get glue.
func (w *Worker) internalize(ctx context.Context, m store.Message) error {
	rc, err := w.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return err
	}
	raw, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		return rerr
	}
	rewritten, sum, ierr := extimg.Internalize(ctx, raw, w.cfg, extimg.DKIMVerdict{})
	if ierr != nil {
		return ierr
	}
	if !sum.Modified {
		// Nothing to do — clear the flag and move on.
		return w.store.Meta().ClearMessageInternalizePending(ctx, m.ID)
	}
	ref, err := w.store.Blobs().Put(ctx, bytesReader(rewritten))
	if err != nil {
		return err
	}
	if err := w.store.Meta().ReplaceMessageBody(ctx, m.ID, ref, int64(len(rewritten)), m.Envelope); err != nil {
		return err
	}
	w.logger.LogAttrs(ctx, slog.LevelDebug, "extimg-worker: rewrite ok",
		slog.Uint64("message_id", uint64(m.ID)),
		slog.Int("internalized", sum.Internalized),
	)
	return nil
}

func (w *Worker) snapshotCursor() store.MessageID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cursor
}

func (w *Worker) advanceCursor(to store.MessageID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cursor = to
}

func (w *Worker) resetCursor() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cursor = 0
}

func (w *Worker) bumpProcessed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.processedTotal++
}

func (w *Worker) snapshotProcessed() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.processedTotal
}
