package chatretention

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
	// DefaultSweepInterval is the cadence at which the worker scans
	// for retention-expired chat messages. 60 s matches the snooze
	// worker's default — retention precision is minute-grained and a
	// slower sweep lets the indexed conversation/account scans run
	// without piling work.
	DefaultSweepInterval = 60 * time.Second

	// MinSweepInterval is the floor below which configuration is
	// treated as the default; sub-5-second sweeping against the chat
	// tables is a configuration mistake, not a feature.
	MinSweepInterval = 5 * time.Second

	// DefaultBatchSize bounds one sweep's hard-deletes so we do not
	// pin a writer transaction. 1000 matches the retention spec note
	// in the Track C scope.
	DefaultBatchSize = 1000

	// MaxBatchSize is the operator-visible ceiling on per-sweep
	// throughput.
	MaxBatchSize = 10000

	// listPageSize is the page size used when paging through
	// chat_conversations / chat_account_settings rows. Independent of
	// the per-sweep delete budget.
	listPageSize = 256
)

// Options configures Worker. Zero fields fall back to defaults.
type Options struct {
	// Store is the metadata source. Required.
	Store store.Store
	// Logger is the structured logger; nil falls back to slog.Default.
	Logger *slog.Logger
	// Clock is the injected clock; nil falls back to clock.NewReal.
	Clock clock.Clock
	// SweepInterval is the cadence between sweeps. Below
	// MinSweepInterval is treated as the default; this matches
	// sysconfig validation which rejects sub-5-second values.
	SweepInterval time.Duration
	// BatchSize is the per-sweep hard-delete ceiling. 0 / negative ->
	// default; values above MaxBatchSize are clamped.
	BatchSize int
}

// Worker is the chat retention sweeper loop. Construct with
// NewWorker; call Run(ctx) in a managed goroutine.
type Worker struct {
	store    store.Store
	logger   *slog.Logger
	clock    clock.Clock
	interval time.Duration
	batch    int
	running  atomic.Bool
	deleted  atomic.Uint64
}

// NewWorker constructs a Worker with the supplied options. Defaults
// are applied at construction time so the worker exposes a stable
// SweepInterval / BatchSize for tests.
func NewWorker(opts Options) *Worker {
	observe.RegisterChatretentionMetrics()
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

// Deleted returns the cumulative count of messages hard-deleted by
// this worker. Used by tests and by the metrics exporter.
func (w *Worker) Deleted() uint64 { return w.deleted.Load() }

// SweepInterval returns the resolved sweep interval (post-defaulting).
// Exposed for tests.
func (w *Worker) SweepInterval() time.Duration { return w.interval }

// BatchSize returns the resolved batch ceiling (post-defaulting).
// Exposed for tests.
func (w *Worker) BatchSize() int { return w.batch }

// Run drives the sweep loop until ctx is cancelled. Each tick scans
// every conversation with a positive RetentionSeconds plus every
// account whose ChatAccountSettings.DefaultRetentionSeconds > 0,
// hard-deletes messages older than the resolved window, and
// recomputes the conversation's denormalised counters via
// Metadata.HardDeleteChatMessage.
//
// Returns nil on ctx cancellation; non-nil only on an unrecoverable
// store failure (the lifecycle errgroup logs and triggers shutdown).
// Run is single-goroutine; a second concurrent invocation returns an
// error.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil {
		return errors.New("chatretention: nil Store")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("chatretention: worker already running")
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

// Tick performs one sweep. Returns the number of messages
// hard-deleted. Exported so tests can drive the sweeper deterministically
// without spinning Run in a goroutine.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	start := w.clock.Now()
	defer func() {
		observe.ChatretentionSweepsTotal.Inc()
		observe.ChatretentionSweepDurationSeconds.Observe(w.clock.Now().Sub(start).Seconds())
	}()
	now := start.UTC()
	deleted := 0
	// Phase 1: per-conversation retention overrides.
	if err := w.sweepPerConversation(ctx, now, &deleted); err != nil {
		return deleted, err
	}
	// Phase 2: per-account default retention. Skip conversations whose
	// retention_seconds is non-nil — those are owned by phase 1 and
	// must not double-count.
	if err := w.sweepPerAccount(ctx, now, &deleted); err != nil {
		return deleted, err
	}
	if deleted > 0 {
		w.logger.LogAttrs(ctx, slog.LevelInfo, "chatretention: swept",
			slog.Int("deleted", deleted),
			slog.Time("now", now),
		)
	}
	return deleted, nil
}

// sweepPerConversation hard-deletes messages whose conversation
// carries an explicit RetentionSeconds > 0 and whose CreatedAt is
// older than now - retention.
func (w *Worker) sweepPerConversation(ctx context.Context, now time.Time, deleted *int) error {
	var afterID store.ConversationID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		convs, err := w.store.Meta().ListChatConversationsForRetention(ctx, afterID, listPageSize)
		if err != nil {
			return fmt.Errorf("chatretention: list conversations: %w", err)
		}
		if len(convs) == 0 {
			return nil
		}
		for _, c := range convs {
			afterID = c.ID
			if c.RetentionSeconds == nil || *c.RetentionSeconds <= 0 {
				continue
			}
			cutoff := now.Add(-time.Duration(*c.RetentionSeconds) * time.Second)
			if err := w.expireConversation(ctx, c.ID, cutoff, deleted); err != nil {
				return err
			}
			if *deleted >= w.batch {
				return nil
			}
		}
		if len(convs) < listPageSize {
			return nil
		}
	}
}

// sweepPerAccount hard-deletes messages whose owning conversation has
// no RetentionSeconds override but whose creator principal has set a
// positive ChatAccountSettings.DefaultRetentionSeconds.
func (w *Worker) sweepPerAccount(ctx context.Context, now time.Time, deleted *int) error {
	var afterID store.PrincipalID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		settings, err := w.store.Meta().ListChatAccountSettingsForRetention(ctx, afterID, listPageSize)
		if err != nil {
			return fmt.Errorf("chatretention: list account settings: %w", err)
		}
		if len(settings) == 0 {
			return nil
		}
		for _, s := range settings {
			afterID = s.PrincipalID
			if s.DefaultRetentionSeconds <= 0 {
				continue
			}
			cutoff := now.Add(-time.Duration(s.DefaultRetentionSeconds) * time.Second)
			// List conversations created by this principal, filter to
			// those whose RetentionSeconds is nil (use account default).
			kindOwner := s.PrincipalID
			filter := store.ChatConversationFilter{
				CreatedByPrincipalID: &kindOwner,
				IncludeArchived:      true,
			}
			convs, err := w.store.Meta().ListChatConversations(ctx, filter)
			if err != nil {
				return fmt.Errorf("chatretention: list account-owned conversations: %w", err)
			}
			for _, c := range convs {
				if c.RetentionSeconds != nil {
					continue
				}
				if err := w.expireConversation(ctx, c.ID, cutoff, deleted); err != nil {
					return err
				}
				if *deleted >= w.batch {
					return nil
				}
			}
		}
		if len(settings) < listPageSize {
			return nil
		}
	}
}

// expireConversation hard-deletes every non-system message in
// conversation cid whose CreatedAt is at or before cutoff. Bounded by
// the worker's batch ceiling so a single conversation cannot starve
// peers. Uses an AfterID cursor so undeletable system rows do not
// pin the page indefinitely.
func (w *Worker) expireConversation(ctx context.Context, cid store.ConversationID, cutoff time.Time, deleted *int) error {
	// CreatedBefore in the filter is strictly less-than; widen by one
	// microsecond so the boundary CreatedAt == cutoff is included
	// (REQ-CHAT-92's "older than" is inclusive at the cutoff).
	cutoffPlus := cutoff.Add(time.Microsecond)
	var afterID store.ChatMessageID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		filter := store.ChatMessageFilter{
			ConversationID: &cid,
			CreatedBefore:  &cutoffPlus,
			IncludeDeleted: true,
			AfterID:        afterID,
			Limit:          listPageSize,
		}
		msgs, err := w.store.Meta().ListChatMessages(ctx, filter)
		if err != nil {
			return fmt.Errorf("chatretention: list messages: %w", err)
		}
		if len(msgs) == 0 {
			return nil
		}
		for _, m := range msgs {
			afterID = m.ID
			if m.IsSystem {
				// REQ-CHAT-92: system messages are retained for the
				// conversation history (joins/leaves don't decay).
				continue
			}
			if m.CreatedAt.After(cutoff) {
				continue
			}
			if err := w.store.Meta().HardDeleteChatMessage(ctx, m.ID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					// Concurrently removed; advance.
					continue
				}
				return fmt.Errorf("chatretention: hard-delete %d: %w", m.ID, err)
			}
			*deleted++
			w.deleted.Add(1)
			observe.ChatretentionMessagesDeletedTotal.Inc()
			if *deleted >= w.batch {
				return nil
			}
		}
		if len(msgs) < listPageSize {
			return nil
		}
	}
}
