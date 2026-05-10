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
	// ProgressLogInterval controls the period of the sustained-progress
	// beacon (REQ-EXTIMG-BG-INTERNAL-52). Default 60 s. The goroutine
	// emits one INFO line per tick carrying pending_count_total,
	// processed_total, and processed_since_start_per_min so the operator
	// sees forward motion even when individual batch lines have rolled
	// out of the log window.
	ProgressLogInterval time.Duration
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
	if opts.ProgressLogInterval <= 0 {
		opts.ProgressLogInterval = 60 * time.Second
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
	// REQ-EXTIMG-BG-INTERNAL-52: report the initial backlog magnitude
	// at boot so the operator sees how much work is queued. A failure
	// is logged and ignored; the worker still starts.
	startedAt := w.clock.Now()
	pendingTotal, err := w.store.Meta().CountInternalizePendingTotal(ctx)
	if err != nil {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: count pending at start failed",
			slog.String("err", err.Error()))
		pendingTotal = 0
	}
	w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: started",
		slog.Int("concurrency", w.opts.Concurrency),
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Duration("idle_poll", w.opts.IdlePollInterval),
		slog.Duration("progress_log_interval", w.opts.ProgressLogInterval),
		slog.Uint64("pending_count_total", pendingTotal),
	)

	// REQ-EXTIMG-BG-INTERNAL-52: sustained-progress beacon. The
	// goroutine exits on ctx cancellation; runProgressLogger blocks
	// until then so the parent waits for it via the WaitGroup before
	// returning -- no goroutine leak past Run's return.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.runProgressLogger(ctx, startedAt)
	}()

	// First pass: drain whatever is pending at startup.
	w.resetCursor()
	for {
		empty := w.runBatch(ctx)
		if ctx.Err() != nil {
			wg.Wait()
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
		// REQ-EXTIMG-BG-INTERNAL-51: the batch was empty; the worker
		// is about to park on the notify channel or the safety-net
		// tick. Emit one INFO line so the operator can distinguish
		// "idle and waiting" from "stalled".
		w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: idle, parking",
			slog.Uint64("processed_total", w.snapshotProcessed()))
		// Batch was empty: park on notify or the safety-net tick.
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-w.notifyCh:
			w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: woke",
				slog.String("wake_source", "notify"))
			w.resetCursor()
		case <-w.clock.After(w.opts.IdlePollInterval):
			w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: woke",
				slog.String("wake_source", "idle_poll"))
			w.resetCursor()
		}
	}
}

// runProgressLogger ticks every ProgressLogInterval and emits a
// pending_count_total / processed_total / processed_since_start_per_min
// beacon (REQ-EXTIMG-BG-INTERNAL-52). Exits on ctx cancellation.
func (w *Worker) runProgressLogger(ctx context.Context, startedAt time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.clock.After(w.opts.ProgressLogInterval):
		}
		if ctx.Err() != nil {
			return
		}
		processed := w.snapshotProcessed()
		// Use a fresh, short-lived context for the count query so a
		// long-running outer ctx cancellation does not block the
		// log line. The outer select already aborted before reaching
		// this point on ctx.Done().
		pending, err := w.store.Meta().CountInternalizePendingTotal(ctx)
		if err != nil {
			w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: count pending in progress beacon failed",
				slog.String("err", err.Error()))
			pending = 0
		}
		elapsed := w.clock.Now().Sub(startedAt)
		var perMin float64
		if elapsed > 0 {
			perMin = float64(processed) / elapsed.Minutes()
		}
		w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: progress",
			slog.Uint64("pending_count_total", pending),
			slog.Uint64("processed_total", processed),
			slog.Float64("processed_since_start_per_min", perMin),
		)
	}
}

// processOutcome records the per-message result aggregated by runBatch
// (REQ-EXTIMG-BG-INTERNAL-50) and reported on the batch-summary log
// line. The PrincipalID is the row's owner when the worker did *some*
// work (whether the rewrite succeeded, no-changed out, or failed with
// the flag cleared); 0 when the row was already gone or already
// cleared by a competing path -- those are no-ops the count surface
// should not credit the worker for.
type processOutcome struct {
	PrincipalID store.PrincipalID
	Result      processResult
}

type processResult int

const (
	// resultSkipped: row gone (expunged) or already cleared by a
	// competing path before the worker reached it. Not counted in any
	// of ok / no_change / failed.
	resultSkipped processResult = iota
	// resultOK: extimg.Internalize returned Modified=true and the
	// rewritten body was committed via ReplaceMessageBody.
	resultOK
	// resultNoChange: extimg.Internalize returned Modified=false; the
	// flag was cleared without writing a new body.
	resultNoChange
	// resultFailed: rewrite returned an error; the flag was cleared so
	// the message does not requeue forever (REQ-EXTIMG-BG-01).
	resultFailed
)

// runBatch pulls one batch from the store, fans the work out to
// `Concurrency` goroutines, waits for completion, bumps the per-principal
// InternalizeStatus state once per affected principal
// (REQ-EXTIMG-BG-INTERNAL-21), and advances the cursor. Returns true
// when the batch was empty (caller should park).
func (w *Worker) runBatch(ctx context.Context) bool {
	start := w.clock.Now()
	cursorBefore := w.snapshotCursor()
	ids, err := w.store.Meta().ListMessagesWithInternalizePending(ctx, cursorBefore, w.opts.BatchSize)
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
	// Each goroutine reports its outcome back via outcomesCh so the
	// post-batch bump can fire once per principal -- and the batch
	// summary log line (REQ-EXTIMG-BG-INTERNAL-50) can roll up the
	// per-result counts -- without holding any per-message lock
	// during the fan-out.
	sem := make(chan struct{}, w.opts.Concurrency)
	outcomesCh := make(chan processOutcome, len(ids))
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
			outcomesCh <- w.processOne(ctx, id)
		}(id)
	}
	wg.Wait()
	close(outcomesCh)
	// Collect per-result counts and distinct affected principals.
	// The InternalizeStatus counter advances once per principal per
	// batch (REQ-EXTIMG-BG-INTERNAL-21) regardless of whether
	// individual rewrites succeeded -- the count surface only
	// reflects "the worker did *some* work" on this principal's
	// mailbox.
	var okCount, noChangeCount, failedCount int
	seen := make(map[store.PrincipalID]struct{}, 4)
	for o := range outcomesCh {
		switch o.Result {
		case resultOK:
			okCount++
		case resultNoChange:
			noChangeCount++
		case resultFailed:
			failedCount++
		}
		if o.PrincipalID == 0 {
			continue
		}
		seen[o.PrincipalID] = struct{}{}
	}
	for pid := range seen {
		// Bump the per-principal state counter and emit a paired
		// EntityKindInternalizeStatus user-cause state_changes row.
		// The row is what arms the EventSource flush timer; the
		// counter is what the SPA reads via collectStateMap.
		// REQ-EXTIMG-BG-INTERNAL-22.
		if _, err := w.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindInternalizeStatus); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: bump internalize-status failed",
				slog.Uint64("principal_id", uint64(pid)),
				slog.String("err", err.Error()))
			continue
		}
		change := store.StateChange{
			PrincipalID: pid,
			Kind:        store.EntityKindInternalizeStatus,
			Op:          store.ChangeOpUpdated,
			Cause:       store.ChangeCauseUser,
		}
		if err := w.store.Meta().AppendStateChange(ctx, change); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: append internalize-status change failed",
				slog.Uint64("principal_id", uint64(pid)),
				slog.String("err", err.Error()))
		}
	}
	// Advance the cursor to the lowest id of the batch (the rows are
	// returned descending, so ids[len-1] is the lowest).
	cursorAfter := ids[len(ids)-1]
	w.advanceCursor(cursorAfter)

	// REQ-EXTIMG-BG-INTERNAL-50: emit one INFO line per non-empty
	// processed batch so the operator has a steady signal that
	// progress is occurring. Use the injected clock for elapsed
	// rather than time.Since so fakes deliver a predictable value.
	elapsed := w.clock.Now().Sub(start)
	w.logger.LogAttrs(ctx, slog.LevelInfo, "extimg-worker: batch summary",
		slog.Int("batch_size", len(ids)),
		slog.Int("principals", len(seen)),
		slog.Int("ok", okCount),
		slog.Int("no_change", noChangeCount),
		slog.Int("failed", failedCount),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.Uint64("cursor_before", uint64(cursorBefore)),
		slog.Uint64("cursor_after", uint64(cursorAfter)),
	)
	return false
}

// processOne runs the rewrite for a single message. Errors are
// swallowed to the log; the cursor advances regardless so a poison
// blob never wedges the worker. Returns the per-message outcome so
// runBatch can aggregate the distinct affected principals for the
// per-batch InternalizeStatus bump (REQ-EXTIMG-BG-INTERNAL-21) and
// the per-result counts for the batch-summary log line
// (REQ-EXTIMG-BG-INTERNAL-50).
func (w *Worker) processOne(ctx context.Context, id store.MessageID) processOutcome {
	w.bumpProcessed()
	m, err := w.store.Meta().GetMessage(ctx, id)
	if err != nil {
		// Message gone (expunged) — clear the flag if it still
		// exists, otherwise just move on.
		_ = w.store.Meta().ClearMessageInternalizePending(ctx, id)
		return processOutcome{Result: resultSkipped}
	}
	if !m.InternalizePending {
		// Already cleared by another path.
		return processOutcome{PrincipalID: m.PrincipalID, Result: resultSkipped}
	}
	res, ierr := w.internalize(ctx, m)
	if ierr != nil {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "extimg-worker: rewrite failed",
			slog.Uint64("message_id", uint64(m.ID)),
			slog.String("err", ierr.Error()))
		// Clear the flag so the message does not re-queue forever
		// (REQ-EXTIMG-BG-01). The user sees the original body; the
		// failure is on the existing extimg metric.
		_ = w.store.Meta().ClearMessageInternalizePending(ctx, m.ID)
		return processOutcome{PrincipalID: m.PrincipalID, Result: resultFailed}
	}
	return processOutcome{PrincipalID: m.PrincipalID, Result: res}
}

// internalize loads the blob, runs extimg.Internalize, and replaces
// the body. Modelled on the legacy maybeInternalizeOnDemand path
// (REQ-EXTIMG-93..98) without the Email/get glue. Returns the
// per-message result so processOne can roll it up into the batch
// summary (REQ-EXTIMG-BG-INTERNAL-50).
func (w *Worker) internalize(ctx context.Context, m store.Message) (processResult, error) {
	rc, err := w.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return resultFailed, err
	}
	raw, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		return resultFailed, rerr
	}
	rewritten, sum, ierr := extimg.Internalize(ctx, raw, w.cfg, extimg.DKIMVerdict{})
	if ierr != nil {
		return resultFailed, ierr
	}
	if !sum.Modified {
		// Nothing to do — clear the flag and move on.
		if err := w.store.Meta().ClearMessageInternalizePending(ctx, m.ID); err != nil {
			return resultFailed, err
		}
		return resultNoChange, nil
	}
	ref, err := w.store.Blobs().Put(ctx, bytesReader(rewritten))
	if err != nil {
		return resultFailed, err
	}
	if err := w.store.Meta().ReplaceMessageBody(ctx, m.ID, ref, int64(len(rewritten)), m.Envelope); err != nil {
		return resultFailed, err
	}
	w.logger.LogAttrs(ctx, slog.LevelDebug, "extimg-worker: rewrite ok",
		slog.Uint64("message_id", uint64(m.ID)),
		slog.Int("internalized", sum.Internalized),
	)
	return resultOK, nil
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
