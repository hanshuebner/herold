package bodymeta

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// defaultBatchSize is the number of messages fetched per store round-trip.
// Small batches keep the loop responsive without hammering the store.
const defaultBatchSize = 32

// defaultIdleSleep is the duration the worker parks between drain passes when
// the backlog is empty.
const defaultIdleSleep = 30 * time.Second

// Options configures a Worker. Zero-value fields apply the defaults
// documented on each field.
type Options struct {
	// BatchSize caps the number of messages the worker pulls per store
	// round-trip. Default 32.
	BatchSize int
	// IdleSleep is how long the worker waits between passes when the
	// backlog is empty. Default 30 s.
	IdleSleep time.Duration
}

// Worker sweeps messages whose body_meta_computed flag is false, parses
// each body, and calls SetMessageBodyMeta so future Email/get calls can
// skip the blob. Construct via New; drive via Run.
type Worker struct {
	store  store.Store
	logger *slog.Logger
	clk    clock.Clock
	opts   Options
}

// New constructs a Worker. logger and clk fall back to slog.Default() and
// clock.NewReal() when nil; zero-value option fields apply the defaults.
func New(st store.Store, logger *slog.Logger, clk clock.Clock, opts Options) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.IdleSleep <= 0 {
		opts.IdleSleep = defaultIdleSleep
	}
	return &Worker{
		store:  st,
		logger: logger.With("subsystem", "bodymeta-worker", "activity", "system"),
		clk:    clk,
		opts:   opts,
	}
}

// Run drives the worker until ctx is cancelled. It is safe to call Run in a
// goroutine registered on the lifecycle errgroup (like the internalize-worker).
func (w *Worker) Run(ctx context.Context) {
	w.logger.LogAttrs(ctx, slog.LevelInfo, "bodymeta-worker: started",
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Duration("idle_sleep", w.opts.IdleSleep),
	)
	var beforeID store.MessageID // 0 = "start from the newest"
	for {
		if ctx.Err() != nil {
			w.logger.LogAttrs(ctx, slog.LevelInfo, "bodymeta-worker: stopped")
			return
		}
		ids, err := w.store.Meta().ListMessagesNeedingBodyMeta(ctx, beforeID, w.opts.BatchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				w.logger.LogAttrs(ctx, slog.LevelInfo, "bodymeta-worker: stopped")
				return
			}
			w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: list failed",
				slog.String("err", err.Error()),
			)
			// Back off and retry; do not spin on a broken store.
			select {
			case <-ctx.Done():
				return
			case <-w.clk.After(w.opts.IdleSleep):
			}
			beforeID = 0
			continue
		}
		if len(ids) == 0 {
			// Backlog exhausted. Reset cursor and park.
			w.logger.LogAttrs(ctx, slog.LevelDebug, "bodymeta-worker: idle, parking")
			select {
			case <-ctx.Done():
				w.logger.LogAttrs(ctx, slog.LevelInfo, "bodymeta-worker: stopped")
				return
			case <-w.clk.After(w.opts.IdleSleep):
			}
			beforeID = 0
			continue
		}

		ok, failed := 0, 0
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			if w.processOne(ctx, id) {
				ok++
			} else {
				failed++
			}
			// Advance the cursor. ListMessagesNeedingBodyMeta returns IDs in
			// descending order (newest first); the last ID is the smallest and
			// becomes the beforeID for the next page.
			beforeID = id
		}
		w.logger.LogAttrs(ctx, slog.LevelInfo, "bodymeta-worker: batch",
			slog.Int("processed", len(ids)),
			slog.Int("ok", ok),
			slog.Int("failed", failed),
			slog.Uint64("cursor_after", uint64(beforeID)),
		)
		// If we got fewer than a full batch, we've reached the end for now.
		// Reset the cursor so the next pass starts from the newest again
		// (there may be new messages inserted while we were sweeping).
		if len(ids) < w.opts.BatchSize {
			beforeID = 0
		}
	}
}

// processOne fetches the blob for id, parses it, and calls SetMessageBodyMeta.
// Returns true on success, false on any error (errors are logged, not returned;
// the worker continues to the next message regardless).
func (w *Worker) processOne(ctx context.Context, id store.MessageID) bool {
	m, err := w.store.Meta().GetMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Message deleted between the list call and now — skip silently.
			return true
		}
		w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: get message failed",
			slog.Uint64("message_id", uint64(id)),
			slog.String("err", err.Error()),
		)
		return false
	}
	if m.BodyMetaComputed {
		// Already handled by a concurrent caller (e.g. Email/get opportunistic
		// persist). Skip without error.
		return true
	}
	rc, err := w.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Blob gone — this can happen when the message was expunged after
			// the list call. Not an error.
			return true
		}
		w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: get blob failed",
			slog.Uint64("message_id", uint64(id)),
			slog.String("blob_hash", m.Blob.Hash),
			slog.String("err", err.Error()),
		)
		return false
	}
	raw, rerr := io.ReadAll(io.LimitReader(rc, 64<<20))
	_ = rc.Close()
	if rerr != nil {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: read blob failed",
			slog.Uint64("message_id", uint64(id)),
			slog.String("err", rerr.Error()),
		)
		return false
	}
	opts := mailparse.NewParseOptions()
	parsed, perr := mailparse.Parse(bytes.NewReader(raw), opts)
	if perr != nil {
		// Unparseable body: write zeroed meta so we don't retry forever.
		// The flag is set so the message is excluded from future sweeps.
		w.logger.LogAttrs(ctx, slog.LevelDebug, "bodymeta-worker: parse failed, writing zeroed meta",
			slog.Uint64("message_id", uint64(id)),
			slog.String("err", perr.Error()),
		)
		if serr := w.store.Meta().SetMessageBodyMeta(ctx, id, "", false); serr != nil {
			w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: set body meta (zeroed) failed",
				slog.Uint64("message_id", uint64(id)),
				slog.String("err", serr.Error()),
			)
			return false
		}
		return true
	}
	preview := mailparse.BodyPreview(parsed, 256)
	hasAtt := mailparse.HasAttachment(parsed)
	if serr := w.store.Meta().SetMessageBodyMeta(ctx, id, preview, hasAtt); serr != nil {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "bodymeta-worker: set body meta failed",
			slog.Uint64("message_id", uint64(id)),
			slog.String("err", serr.Error()),
		)
		return false
	}
	w.logger.LogAttrs(ctx, slog.LevelDebug, "bodymeta-worker: computed",
		slog.Uint64("message_id", uint64(id)),
		slog.Bool("has_attachment", hasAtt),
		slog.Int("preview_len", len(preview)),
	)
	return true
}
