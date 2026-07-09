package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the background worker that drains EmailBulkJob
// rows created by Email/setByQuery (issue #149/#161, REQ-PROTO-40..48).
// It follows the same shape as the other per-principal sweep workers in
// this codebase (internal/trashretention, internal/chatretention): a
// polling Run(ctx) loop the composition root starts under the lifecycle
// errgroup, ticking a bounded batch of work and sleeping when idle.
//
// Unlike the outbound SMTP queue (internal/queue), this worker does not
// need a claim/lease mechanism: v1 runs a single worker process per
// deployment (herold is a single binary), so ListRunningEmailBulkJobs
// plus the store's own transactional row updates are sufficient —
// there is no concurrent second worker to race against. A crash mid-job
// is safe to resume: the job row's Total/Processed cursor and the
// resolved target-id list are the only state, and every read-then-write
// step is idempotent (ResolveEmailBulkJobTargets no-ops once resolved;
// NextEmailBulkJobBatch always starts at the persisted Processed cursor).

const (
	// DefaultBulkJobPollInterval is how often Run checks for new/pending
	// jobs when idle.
	DefaultBulkJobPollInterval = 2 * time.Second
	// DefaultBulkJobBatchSize is how many messages one Tick patches per
	// job before yielding back to the poll loop. Bounded so one huge job
	// cannot starve other principals' jobs for long, and so each batch's
	// commit stays small.
	DefaultBulkJobBatchSize = 200
	// bulkJobResolvePageSize is the paging size resolveAllMatchingIDs
	// uses against the SQL fast path.
	bulkJobMaxJobsPerTick = 20
)

// BulkJobWorkerOptions configures BulkJobWorker. Zero fields fall back
// to defaults.
type BulkJobWorkerOptions struct {
	// Store is the metadata source. Required.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock; nil falls back to clock.NewReal.
	Clock clock.Clock
	// PollInterval is the cadence between idle polls. 0 falls back to
	// DefaultBulkJobPollInterval.
	PollInterval time.Duration
	// BatchSize is the per-job, per-tick message cap. 0 falls back to
	// DefaultBulkJobBatchSize.
	BatchSize int
}

// BulkJobWorker drains EmailBulkJob rows: resolving each job's target
// set once (off any request path), then applying the job's patch to
// the matched messages in batches, committing progress after each
// batch so a partial per-batch failure never loses the rest of the job
// (REQ-PROTO-40..48 acceptance: "a partial failure on one batch must
// not abort the remaining batches").
type BulkJobWorker struct {
	store    store.Store
	logger   *slog.Logger
	clk      clock.Clock
	interval time.Duration
	batch    int
	// h applies each message's patch via the exact same updateEmail
	// path Email/set uses (keywords, mailboxIds full/incremental patch,
	// snooze atomicity, …) so a bulk archive/mark-read/move behaves
	// identically to the equivalent per-message Email/set call.
	h *handlerSet
}

// NewBulkJobWorker constructs a BulkJobWorker. Call Run(ctx) in a
// managed goroutine (the lifecycle errgroup in internal/admin/server.go).
func NewBulkJobWorker(opts BulkJobWorkerOptions) *BulkJobWorker {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = DefaultBulkJobPollInterval
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = DefaultBulkJobBatchSize
	}
	return &BulkJobWorker{
		store:    opts.Store,
		logger:   logger,
		clk:      clk,
		interval: interval,
		batch:    batch,
		h: &handlerSet{
			store:   opts.Store,
			logger:  logger,
			clk:     clk,
			parseFn: defaultParseFn,
		},
	}
}

// PollInterval returns the resolved poll cadence. Exposed for tests.
func (w *BulkJobWorker) PollInterval() time.Duration { return w.interval }

// BatchSize returns the resolved per-tick batch cap. Exposed for tests.
func (w *BulkJobWorker) BatchSize() int { return w.batch }

// Run drives the drain loop until ctx is cancelled. Returns nil on
// cancellation; non-nil only on an unrecoverable store failure (the
// lifecycle errgroup logs and triggers shutdown). A tick that processed
// a full batch worth of work loops immediately rather than sleeping, so
// a large backlog drains promptly.
func (w *BulkJobWorker) Run(ctx context.Context) error {
	if w.store == nil {
		return errors.New("emailbulkjob: nil Store")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		worked, err := w.Tick(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			w.logger.ErrorContext(ctx, "emailbulkjob: tick failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("err", err.Error()))
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.clk.After(w.interval):
		}
	}
}

// Tick processes one batch of work across every currently-running job
// (bounded to bulkJobMaxJobsPerTick jobs per call so one principal's
// huge backlog cannot starve others indefinitely). Returns worked=true
// when any job advanced, so Run loops immediately instead of sleeping.
// Exported so tests can drain a job deterministically without a
// goroutine + real clock.
func (w *BulkJobWorker) Tick(ctx context.Context) (bool, error) {
	jobs, err := w.store.Meta().ListRunningEmailBulkJobs(ctx, bulkJobMaxJobsPerTick)
	if err != nil {
		return false, err
	}
	if len(jobs) == 0 {
		return false, nil
	}
	worked := false
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return worked, nil
		}
		did, jerr := w.processOne(ctx, job)
		if jerr != nil {
			w.logger.ErrorContext(ctx, "emailbulkjob: job failed",
				slog.String("activity", observe.ActivitySystem),
				slog.Uint64("job_id", uint64(job.ID)),
				slog.String("err", jerr.Error()))
			if ferr := w.store.Meta().FinishEmailBulkJob(ctx, job.ID,
				store.EmailBulkJobStatusFailed, jerr.Error()); ferr != nil {
				w.logger.ErrorContext(ctx, "emailbulkjob: finish-failed write failed",
					slog.String("activity", observe.ActivitySystem),
					slog.Uint64("job_id", uint64(job.ID)),
					slog.String("err", ferr.Error()))
			}
			continue
		}
		if did {
			worked = true
		}
	}
	return worked, nil
}

// processOne advances job by exactly one step: resolving its target set
// if unresolved, otherwise draining one batch of pending targets. A
// non-nil error is a job-level failure (target resolution errored, or a
// store write failed); the caller marks the job EmailBulkJobStatusFailed
// and moves on. A per-message patch failure is NOT a job-level error —
// it is recorded in the job's failure list and the batch continues
// (REQ-PROTO-40..48 acceptance: partial batch failures do not abort the
// rest of the job).
func (w *BulkJobWorker) processOne(ctx context.Context, job store.EmailBulkJob) (bool, error) {
	if job.Total < 0 {
		return w.resolveTargets(ctx, job)
	}
	return w.drainBatch(ctx, job)
}

func (w *BulkJobWorker) resolveTargets(ctx context.Context, job store.EmailBulkJob) (bool, error) {
	filter, ferr := decodeFilter(rawFilterOrNil(job.FilterJSON))
	if ferr != nil {
		return false, fmt.Errorf("decode filter: %w", ferr)
	}
	ids, err := resolveAllMatchingIDs(ctx, w.store, job.PrincipalID, filter)
	if err != nil {
		return false, fmt.Errorf("resolve targets: %w", err)
	}
	if err := w.store.Meta().ResolveEmailBulkJobTargets(ctx, job.ID, ids); err != nil {
		return false, fmt.Errorf("persist targets: %w", err)
	}
	if len(ids) == 0 {
		// Nothing matched: terminal immediately, Processed == Total == 0.
		if err := w.finish(ctx, job.ID, job.PrincipalID, nil); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (w *BulkJobWorker) drainBatch(ctx context.Context, job store.EmailBulkJob) (bool, error) {
	ids, err := w.store.Meta().NextEmailBulkJobBatch(ctx, job.ID, w.batch)
	if err != nil {
		return false, fmt.Errorf("next batch: %w", err)
	}
	if len(ids) == 0 {
		// Every target already attempted (a previous tick's commit
		// landed but the terminal-status write did not, e.g. a crash in
		// between) -- finalize now.
		return w.finalizeIfDrained(ctx, job)
	}
	outcomes := make([]store.EmailBulkJobOutcome, 0, len(ids))
	for _, mid := range ids {
		outcomes = append(outcomes, store.EmailBulkJobOutcome{
			MessageID: mid,
			Err:       w.applyPatch(ctx, job.PrincipalID, mid, job.PatchJSON),
		})
	}
	if err := w.store.Meta().RecordEmailBulkJobBatch(ctx, job.ID, outcomes); err != nil {
		return false, fmt.Errorf("record batch: %w", err)
	}
	// EmailBulkJob push signal: one bump per batch, not per message, so
	// EventSource push volume stays proportional to batches rather than
	// individual messages (mirrors the extimg internalize-worker's
	// InternalizeStatus counter). Non-fatal: the client can still poll
	// EmailBulkJob/get even if this increment is lost.
	if _, err := w.store.Meta().IncrementJMAPState(ctx, job.PrincipalID, store.JMAPStateKindEmailBulkJob); err != nil {
		w.logger.WarnContext(ctx, "emailbulkjob: state bump failed",
			slog.String("activity", observe.ActivitySystem),
			slog.Uint64("job_id", uint64(job.ID)),
			slog.String("err", err.Error()))
	}
	return w.finalizeIfDrained(ctx, job)
}

// finalizeIfDrained re-reads the job row and finalizes it once
// Processed has caught up with Total.
func (w *BulkJobWorker) finalizeIfDrained(ctx context.Context, job store.EmailBulkJob) (bool, error) {
	updated, err := w.store.Meta().GetEmailBulkJob(ctx, job.PrincipalID, job.ID)
	if err != nil {
		// Non-fatal: the batch itself already committed. Report worked so
		// Run keeps looping; the next tick will re-read successfully.
		return true, nil
	}
	if updated.Total >= 0 && updated.Processed >= updated.Total {
		if err := w.finish(ctx, job.ID, job.PrincipalID, updated.Failures); err != nil {
			return false, err
		}
	}
	return true, nil
}

// finish sets the job's terminal status (done when failures is empty,
// partial otherwise) and bumps the push counter once more so the
// client's final EventSource notification reflects the terminal state.
func (w *BulkJobWorker) finish(
	ctx context.Context,
	id store.EmailBulkJobID,
	pid store.PrincipalID,
	failures []store.EmailBulkJobFailure,
) error {
	status := store.EmailBulkJobStatusDone
	if len(failures) > 0 {
		status = store.EmailBulkJobStatusPartial
	}
	if err := w.store.Meta().FinishEmailBulkJob(ctx, id, status, ""); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	if _, err := w.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindEmailBulkJob); err != nil {
		w.logger.WarnContext(ctx, "emailbulkjob: final state bump failed",
			slog.String("activity", observe.ActivitySystem),
			slog.Uint64("job_id", uint64(id)),
			slog.String("err", err.Error()))
	}
	return nil
}

// applyPatch applies patchJSON to message mid via the same updateEmail
// path Email/set uses, with the job's owning principal as both caller
// and account owner (v1 scope: bulk jobs never cross accounts). Returns
// "" on success, or a description string recorded as this message's
// failure otherwise. A patch-application error here is a per-message
// outcome, never a job-level failure — the caller always proceeds to
// the next message in the batch.
func (w *BulkJobWorker) applyPatch(ctx context.Context, pid store.PrincipalID, mid store.MessageID, patchJSON string) string {
	serr, err := w.h.updateEmail(ctx, pid, pid, mid, json.RawMessage(patchJSON))
	if err != nil {
		return err.Error()
	}
	if serr != nil {
		if serr.Description != "" {
			return serr.Type + ": " + serr.Description
		}
		return serr.Type
	}
	return ""
}
