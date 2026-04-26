package storefts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Default WorkerOptions values. Derived from docs/design/notes/spike-fts-cadence.md
// — peak throughput at batch=2000, sub-second visibility at 500 ms commit
// ceiling. Exported so tests and operator diag can use the same defaults
// without a second source of truth.
const (
	DefaultBatchSize     = 2000
	DefaultFlushInterval = 500 * time.Millisecond
	DefaultCursorKey     = "fts"
)

// WorkerOptions tunes the single async indexing worker. Zero fields fall
// back to the spike-recommended defaults (2000 / 500 ms / "fts").
type WorkerOptions struct {
	// BatchSize is the document-count ceiling that triggers a commit.
	BatchSize int
	// FlushInterval is the wall-time ceiling, measured from the first
	// un-flushed change in the current batch, that triggers a commit.
	FlushInterval time.Duration
	// CursorKey names the worker's durable cursor slot. Kept in
	// WorkerOptions for forward compatibility with a future per-shard
	// worker layout; Phase 1 uses a single "fts" slot.
	CursorKey string
}

// Worker is the single async indexing goroutine. It reads the store's FTS
// change feed with a durable cursor, resolves each change to a message +
// extracted text, accumulates a Bleve batch, and flushes it on size OR
// time (whichever fires first). One worker covers the v1 100 msg/s target
// per the spike; sharding by principal is a future concern.
//
// Lifecycle: construct with NewWorker; call Run(ctx) in a goroutine owned
// by the server lifecycle manager; cancel ctx to drain and return.
type Worker struct {
	idx     *Index
	store   store.Store
	extract TextExtractor
	logger  *slog.Logger
	clock   clock.Clock
	opts    WorkerOptions

	// cursor is the last Seq successfully processed. It is hydrated on
	// Run() from Metadata.GetFTSCursor(opts.CursorKey) so crash
	// restarts resume from the last committed batch; IndexMessage is
	// idempotent, so a small replay window on cursor loss is safe but
	// undesirable at scale.
	cursor atomic.Uint64

	// lastProcessed is the ProducedAt of the most recent change the
	// worker indexed. Lag is the delta between clock.Now and that
	// instant; surfaces in Lag() for the metrics exporter.
	lastProcessed atomic.Int64

	// running guards against Run being called concurrently on the same
	// instance. A second caller must wait for the first to return.
	running atomic.Bool

	// awaitFlush is closed and replaced on every successful commit;
	// tests use WaitForFlush to synchronise deterministically against
	// the worker without sleeps.
	flushMu        sync.Mutex
	flushSeen      uint64
	flushBroadcast chan struct{}
}

// NewWorker constructs a worker. idx must be a *storefts.Index; st is the
// composed store.Store exposing the FTS change feed, metadata
// (GetMessage), and blob surfaces. extract turns a blob body into plain
// text for the index.
func NewWorker(
	idx *Index,
	st store.Store,
	extract TextExtractor,
	logger *slog.Logger,
	clk clock.Clock,
	opts WorkerOptions,
) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.CursorKey == "" {
		opts.CursorKey = DefaultCursorKey
	}
	w := &Worker{
		idx:            idx,
		store:          st,
		extract:        extract,
		logger:         logger,
		clock:          clk,
		opts:           opts,
		flushBroadcast: make(chan struct{}),
	}
	// Register the FTS collector set on Worker construction. The lag
	// gauge reads w.Lag() on every scrape; first registration wins, so
	// later workers replace the lag source via the helper inside
	// RegisterFTSMetrics. Idempotent across multiple workers in a
	// single process Registry.
	observe.RegisterFTSMetrics(func() float64 { return w.Lag().Seconds() })
	return w
}

// Cursor returns the worker's current durable cursor. Exposed for tests
// and the operator diag surface; the worker mutates it under the hood.
func (w *Worker) Cursor() uint64 {
	return w.cursor.Load()
}

// Lag returns the wall-time delta between now and the ProducedAt of the
// most recent change indexed. Zero if the worker has never processed a
// change. Used by the metrics exporter to expose
// herold_fts_indexing_lag_seconds.
func (w *Worker) Lag() time.Duration {
	last := w.lastProcessed.Load()
	if last == 0 {
		return 0
	}
	now := w.clock.Now().UnixNano()
	if now <= last {
		return 0
	}
	return time.Duration(now - last)
}

// Run consumes the FTS change feed until ctx is cancelled. It is a
// long-running call; callers run it in a single goroutine managed by the
// server lifecycle. Returns nil on ctx cancellation; non-nil on a fatal
// error from the store or index.
func (w *Worker) Run(ctx context.Context) error {
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("storefts: worker already running")
	}
	defer w.running.Store(false)

	// Hydrate the durable cursor. A missing row returns (0, nil) from
	// the store, which starts the feed from the beginning; that is the
	// correct behaviour for a fresh deployment or a backend that has
	// forgotten the cursor.
	if seq, err := w.store.Meta().GetFTSCursor(ctx, w.opts.CursorKey); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("storefts: load cursor: %w", err)
		}
		return nil
	} else {
		w.cursor.Store(seq)
	}

	for {
		if err := ctx.Err(); err != nil {
			// Final flush on shutdown so in-flight changes land in the
			// index. Use a fresh context so the flush itself is not
			// cancelled the moment it starts.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = w.idx.Commit(flushCtx)
			cancel()
			return nil
		}
		changes, err := w.store.FTS().ReadChangeFeedForFTS(ctx, w.cursor.Load(), w.opts.BatchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("storefts: read change feed: %w", err)
		}
		if len(changes) == 0 {
			// Backpressure: no new work. Wait a flush interval or until
			// ctx cancels. We intentionally do not wake on new changes
			// directly — the spike recommended a fixed-interval poll
			// over a notification channel to keep the store interface
			// simple. 500 ms ceiling is already inside the visibility
			// target.
			select {
			case <-ctx.Done():
			case <-w.clock.After(w.opts.FlushInterval):
			}
			continue
		}
		if err := w.processBatch(ctx, changes); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

// processBatch indexes every change in the batch and commits the pending
// Bleve batch at the end. The cursor is advanced to the last Seq on
// successful commit; a failure aborts before the advance so the worker
// re-reads the same range on the next tick.
func (w *Worker) processBatch(ctx context.Context, changes []store.FTSChange) error {
	deadline := w.clock.Now().Add(w.opts.FlushInterval)
	var maxSeq uint64
	var lastProduced time.Time
	indexed := 0
	for _, c := range changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.handleChange(ctx, c); err != nil {
			// Log and continue: one bad message should not block the
			// feed. The cursor still advances past this change because
			// re-reading will hit the same failure.
			w.logger.Warn("storefts: index message",
				"seq", c.Seq,
				"entity_kind", string(c.Kind),
				"entity_id", c.EntityID,
				"op", c.Op,
				"err", err.Error(),
			)
		} else {
			indexed++
		}
		if c.Seq > maxSeq {
			maxSeq = c.Seq
		}
		if c.ProducedAt.After(lastProduced) {
			lastProduced = c.ProducedAt
		}
		// Flush on size OR time ceiling. Size is authoritative via the
		// pending batch; time is the flush-interval wall clock.
		if w.idx.PendingSize() >= w.opts.BatchSize {
			break
		}
		if !w.clock.Now().Before(deadline) {
			break
		}
	}
	if err := w.idx.Commit(ctx); err != nil {
		return fmt.Errorf("storefts: commit: %w", err)
	}
	if maxSeq > 0 {
		w.cursor.Store(maxSeq)
		// Persist the advance so a crash restart resumes from this
		// batch rather than replaying the whole feed. Failure here
		// does not abort the batch: the Bleve commit already
		// succeeded; we log and press on, and the worst case is a
		// replay after restart (IndexMessage is idempotent).
		if err := w.store.Meta().SetFTSCursor(ctx, w.opts.CursorKey, maxSeq); err != nil {
			w.logger.Warn("storefts: persist cursor",
				"key", w.opts.CursorKey,
				"seq", maxSeq,
				"err", err.Error())
		}
	}
	if !lastProduced.IsZero() {
		w.lastProcessed.Store(lastProduced.UnixNano())
	}
	if indexed > 0 && observe.FTSIndexedMessagesTotal != nil {
		observe.FTSIndexedMessagesTotal.Add(float64(indexed))
	}
	w.signalFlush(maxSeq)
	return nil
}

// handleChange dispatches a single FTSChange: an email-create/update
// fetches the message, extracts text, and writes to the pending batch;
// an email-destroy issues a delete. Mailbox- and other-kind events are
// ignored — their messages arrive as individual email-destroyed
// entries. Datatype dispatch is intentionally a string-match on
// EntityKind (not a typed column), so future kinds added per
// docs/design/architecture/05-sync-and-state.md §Forward-compatibility flow
// past this worker untouched.
//
// Wave 2.9.6 Track D adds an EntityKindChatMessage path: chat messages
// are indexed alongside emails in the same Bleve index with a
// kind="chat_message" discriminator, so SearchChatMessages stays
// disjoint from the mail Query path while the worker keeps a single
// cursor.
func (w *Worker) handleChange(ctx context.Context, c store.FTSChange) error {
	switch c.Kind {
	case store.EntityKindEmail:
		return w.handleEmailChange(ctx, c)
	case store.EntityKindChatMessage:
		return w.handleChatMessageChange(ctx, c)
	default:
		// Mailbox- and other-kind events do not map to FTS docs.
		return nil
	}
}

func (w *Worker) handleEmailChange(ctx context.Context, c store.FTSChange) error {
	messageID := store.MessageID(c.EntityID)
	switch c.Op {
	case store.ChangeOpCreated, store.ChangeOpUpdated:
		msg, err := w.store.Meta().GetMessage(ctx, messageID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// The message was deleted between the change's
				// append and this read. Treat as a delete so a stale
				// doc does not linger.
				return w.idx.RemoveMessage(ctx, messageID)
			}
			return fmt.Errorf("get message %d: %w", messageID, err)
		}
		text, err := w.extractText(ctx, msg)
		if err != nil {
			return fmt.Errorf("extract text for message %d: %w", messageID, err)
		}
		return w.idx.IndexMessageFull(ctx, c.PrincipalID, msg, text)
	case store.ChangeOpDestroyed:
		return w.idx.RemoveMessage(ctx, messageID)
	default:
		// Unknown op — leave the index unchanged.
		return nil
	}
}

// handleChatMessageChange routes a chat-message FTS change into the
// chat-document side of the index (REQ-CHAT-80..82). On created/updated
// the worker reads the chat row and indexes its plain-text body; on
// destroyed (hard-delete via chat-retention sweeper) and on
// soft-deleted-state-change-as-update the doc is removed. System
// messages (IsSystem == true) are removed-not-indexed by IndexChatMessage
// itself so audit metadata never enters the search corpus.
//
// Hard-delete: the per-backend HardDeleteChatMessage appends an
// (EntityKindChatMessage, ChangeOpDestroyed) state-change row in the
// same tx (see internal/storesqlite/metadata_chat.go and the pg
// counterpart), which the FTS feed surfaces via state_changes; this
// worker then removes the doc here. No follow-up reconciliation sweep
// is required.
func (w *Worker) handleChatMessageChange(ctx context.Context, c store.FTSChange) error {
	id := store.ChatMessageID(c.EntityID)
	switch c.Op {
	case store.ChangeOpCreated, store.ChangeOpUpdated:
		msg, err := w.store.Meta().GetChatMessage(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Hard-deleted between the change append and this
				// read; remove the doc to keep the index in sync.
				return w.idx.RemoveChatMessage(ctx, id)
			}
			return fmt.Errorf("get chat message %d: %w", id, err)
		}
		// IndexChatMessage is responsible for routing IsSystem and
		// soft-deleted rows to RemoveChatMessage, so the worker does
		// not duplicate that policy here.
		return w.idx.IndexChatMessage(ctx, msg)
	case store.ChangeOpDestroyed:
		return w.idx.RemoveChatMessage(ctx, id)
	default:
		return nil
	}
}

// extractText reads the message body from the blob store and runs it
// through the extractor. An empty blob hash is treated as "no body" so
// tests that seed messages without a real blob still index the envelope.
func (w *Worker) extractText(ctx context.Context, msg store.Message) (string, error) {
	if msg.Blob.Hash == "" {
		return "", nil
	}
	r, err := w.store.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("blob get: %w", err)
	}
	defer r.Close()
	return w.extract.Extract(ctx, msg, r)
}

// signalFlush notifies waiters (tests, operator diag) that a commit just
// completed. The broadcast pattern is close-then-replace so every waiter
// observes the current flush exactly once.
func (w *Worker) signalFlush(seq uint64) {
	w.flushMu.Lock()
	if seq > w.flushSeen {
		w.flushSeen = seq
	}
	ch := w.flushBroadcast
	w.flushBroadcast = make(chan struct{})
	w.flushMu.Unlock()
	close(ch)
}

// WaitForFlush blocks until the worker has committed at least one batch
// after the call. Tests use it to synchronise deterministically with the
// worker's single goroutine; it returns ctx.Err() if the context is
// cancelled first. Not part of the store.FTS surface.
func (w *Worker) WaitForFlush(ctx context.Context) error {
	w.flushMu.Lock()
	ch := w.flushBroadcast
	w.flushMu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
