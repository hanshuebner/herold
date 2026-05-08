package email

// On-demand external-image internalization for importer-flagged
// messages. Called from the Email/get render path when m has
// InternalizePending = true (17-external-images.md REQ-EXTIMG-93).
// On any non-trivial outcome — successful rewrite, no-op rewrite,
// fetch failures, quota exceeded — the pending flag is cleared so
// subsequent reads do not retry indefinitely (REQ-EXTIMG-94).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/store"
)

// maybeInternalizeOnDemand runs the rewriter for one message when its
// importer-set flag is on. The returned Message reflects the new
// blob hash + size when a rewrite landed; the original is returned
// otherwise. Errors are not propagated — the read path always
// degrades to "serve the original body" rather than failing the
// Email/get response.
func (h *handlerSet) maybeInternalizeOnDemand(ctx context.Context, m store.Message) store.Message {
	if !m.InternalizePending {
		return m
	}
	// Operator switched to passthrough after the importer flagged
	// the message: clear the flag and serve the original. The
	// rewriter package is happy to be called in passthrough mode
	// (it's a no-op there), but we save the work entirely.
	if h.extImg.Mode != extimg.ModeInternalize {
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		return m
	}

	rc, err := h.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		h.logOnDemand(ctx, m.ID, slog.LevelWarn, "blob_get_failed", err, extimg.AuditSummary{})
		return m
	}
	raw, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		h.logOnDemand(ctx, m.ID, slog.LevelWarn, "blob_read_failed", rerr, extimg.AuditSummary{})
		return m
	}

	rewritten, sum, ierr := extimg.Internalize(ctx, raw, h.extImg, extimg.DKIMVerdict{})
	if ierr != nil || !sum.Modified {
		// Not modified: parse failed, no candidates, or every fetch
		// failed. Clear the flag to prevent retry storms; subsequent
		// reads serve the original. The audit summary reaches the log
		// so operators can debug why a specific message did not get
		// rewritten.
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		h.logOnDemand(ctx, m.ID, slog.LevelInfo, "no_change", ierr, sum)
		return m
	}

	// Persist the rewritten body. Blobs().Put dedupes on content hash,
	// so two recipients of the same imported newsletter will share
	// the rewritten blob just like any live-delivered message. The
	// envelope is unchanged across rewrite — ReplaceMessageBody
	// reuses the cached envelope verbatim.
	ref, err := h.store.Blobs().Put(ctx, bytes.NewReader(rewritten))
	if err != nil {
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		h.logOnDemand(ctx, m.ID, slog.LevelWarn, "blob_put_failed", err, sum)
		return m
	}
	if err := h.store.Meta().ReplaceMessageBody(ctx, m.ID, ref, int64(len(rewritten)), m.Envelope); err != nil {
		// Quota-exceeded is the most likely outcome here for very
		// image-heavy messages. Either way, clear the flag so the
		// user can read the message — without rewrite — and a future
		// retry doesn't re-do the fetch budget on every open
		// (REQ-EXTIMG-95). ReplaceMessageBody clears the flag in
		// success path; we clear it explicitly on failure.
		h.clearPendingBestEffort(ctx, m.ID)
		m.InternalizePending = false
		level := slog.LevelWarn
		if errors.Is(err, store.ErrQuotaExceeded) {
			level = slog.LevelInfo
		}
		h.logOnDemand(ctx, m.ID, level, "replace_body_failed", err, sum)
		return m
	}
	m.Blob = ref
	m.Size = int64(len(rewritten))
	m.InternalizePending = false
	h.logOnDemand(ctx, m.ID, slog.LevelInfo, "internalized", nil, sum)
	return m
}

// clearPendingBestEffort issues a flag-clear and ignores ErrNotFound
// (race: the message was concurrently expunged).
func (h *handlerSet) clearPendingBestEffort(ctx context.Context, id store.MessageID) {
	if err := h.store.Meta().ClearMessageInternalizePending(ctx, id); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		// Log but don't surface — the read path must keep working.
		h.logger.LogAttrs(ctx, slog.LevelWarn, "extimg on-demand: clear flag failed",
			slog.Uint64("message_id", uint64(id)),
			slog.String("err", err.Error()),
		)
	}
}

// logOnDemand emits a single structured log line per on-demand
// invocation so operators can correlate user-visible latency with
// the rewriter's per-message decisions (REQ-EXTIMG-98).
func (h *handlerSet) logOnDemand(
	ctx context.Context,
	id store.MessageID,
	level slog.Level,
	outcome string,
	err error,
	sum extimg.AuditSummary,
) {
	if h.logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("subsystem", "protojmap-mail-email"),
		slog.String("phase", "on_demand"),
		slog.String("outcome", outcome),
		slog.Uint64("message_id", uint64(id)),
		slog.Bool("modified", sum.Modified),
		slog.Int("candidates", sum.Candidates),
		slog.Int("internalized", sum.Internalized),
		slog.Int("failed", sum.Failed),
		slog.Duration("wall", sum.WallClock),
	}
	if sum.NotEligibleReason != "" {
		attrs = append(attrs, slog.String("not_eligible", sum.NotEligibleReason))
	}
	for k, v := range sum.FailureCounts {
		if v > 0 {
			attrs = append(attrs, slog.Int("fail_"+string(k), v))
		}
	}
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	h.logger.LogAttrs(ctx, level, "extimg on-demand rewrite", attrs...)
}
