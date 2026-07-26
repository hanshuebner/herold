package email

// Email/retryImages (issue #162, 17-external-images.md
// REQ-EXTIMG-71/73, capability https://netzhansa.com/jmap/email-
// image-retry). REQ-EXTIMG-BG-14 permanently placeholders any
// external image herold could not fetch: the origin URL is never
// written into the browser-visible/stored delivered body. This file
// implements the recovery half of that trade-off -- a server-side
// retry that re-attempts exactly the retained (server-only) failed
// URLs, without ever handing them to the client.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// retryImagesRequest is the wire-form Email/retryImages request.
type retryImagesRequest struct {
	AccountID jmapID `json:"accountId"`
	ID        jmapID `json:"id"`
}

// retryImagesResponse is the wire-form Email/retryImages response.
type retryImagesResponse struct {
	AccountID jmapID `json:"accountId"`
	ID        jmapID `json:"id"`
	// RetriedCount is the number of previously-failed images that
	// were successfully re-fetched by this call. 0 means nothing
	// improved (still failing, or there was nothing retained to
	// retry).
	RetriedCount int `json:"retriedCount"`
	// FailedImageCount is the Email.failedImageCount value after this
	// call -- the client re-renders its badge from this without a
	// separate Email/get round-trip.
	FailedImageCount int `json:"failedImageCount"`
	// NewState is the Email state after this call, matching the value
	// a subsequent Email/get would report. Unchanged from the prior
	// state when RetriedCount is 0.
	NewState string `json:"newState"`
}

// retryImagesHandler implements Email/retryImages.
type retryImagesHandler struct{ h *handlerSet }

func (r retryImagesHandler) Method() string { return "Email/retryImages" }

func (r retryImagesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	callerPID, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req retryImagesRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	ownerPID, merr := resolveAccount(ctx, r.h.store.Meta(), callerPID, req.AccountID)
	if merr != nil {
		return nil, merr
	}
	mid, ok := emailIDFromJMAP(req.ID)
	if !ok {
		return nil, protojmap.NewMethodError("invalidArguments", "id is not a valid Email id")
	}

	m, err := loadMessageForPrincipal(ctx, r.h.store.Meta(), callerPID, mid)
	if err != nil {
		if errors.Is(err, errMessageMissing) {
			return nil, protojmap.NewMethodError("notFound", "no such Email")
		}
		return nil, serverFail(fmt.Errorf("email: retryImages: load message: %w", err))
	}
	mb, err := r.h.store.Meta().GetMailboxByID(ctx, m.MailboxID)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: retryImages: get mailbox: %w", err))
	}
	if mb.PrincipalID != ownerPID {
		// The message is visible to the caller but does not belong to
		// the requested account -- treat it as absent for this
		// account per REQ-PROTO-33.
		return nil, protojmap.NewMethodError("notFound", "no such Email")
	}
	if callerPID != ownerPID {
		rights, rerr := aclRightsForCaller(ctx, r.h.store.Meta(), callerPID, mb)
		if rerr != nil {
			return nil, serverFail(rerr)
		}
		if !rights.CanWrite() {
			return nil, protojmap.NewMethodError("forbidden", "caller lacks write right on the mailbox")
		}
	}

	resp := retryImagesResponse{AccountID: req.AccountID, ID: req.ID}

	if m.FailedImageCount == 0 || m.FailedImageState == "" {
		state, serr := currentState(ctx, r.h.store.Meta(), ownerPID)
		if serr != nil {
			return nil, serverFail(serr)
		}
		resp.FailedImageCount = 0
		resp.NewState = state
		return resp, nil
	}

	retained, derr := extimg.DecodeRetainedState(m.FailedImageState)
	if derr != nil {
		// Corrupt/unreadable retained state: clear it defensively so
		// the badge does not wedge forever; report nothing retried.
		_ = r.h.store.Meta().SetMessageFailedImages(ctx, mid, 0, "", 0, "")
		state, serr := currentState(ctx, r.h.store.Meta(), ownerPID)
		if serr != nil {
			return nil, serverFail(serr)
		}
		resp.NewState = state
		return resp, nil
	}

	rc, err := r.h.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: retryImages: blob get: %w", err))
	}
	raw, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		return nil, serverFail(fmt.Errorf("email: retryImages: read blob: %w", rerr))
	}

	rewritten, result, rerr2 := extimg.RetryFailedImages(ctx, raw, retained.Template, retained.URLs, r.h.extImg, retained.DKIM)
	if rerr2 != nil {
		return nil, serverFail(fmt.Errorf("email: retryImages: retry: %w", rerr2))
	}
	r.logRetryOutcome(ctx, mid, len(retained.URLs), result)
	resp.RetriedCount = result.RetriedOK
	resp.FailedImageCount = len(result.StillFailedURLs)

	if !result.Modified || result.RetriedOK == 0 {
		// Nothing improved: leave the retained state exactly as it
		// was (still resolvable by a future retry) and report the
		// unchanged count.
		resp.FailedImageCount = m.FailedImageCount
		state, serr := currentState(ctx, r.h.store.Meta(), ownerPID)
		if serr != nil {
			return nil, serverFail(serr)
		}
		resp.NewState = state
		return resp, nil
	}

	ref, perr := r.h.store.Blobs().Put(ctx, bytes.NewReader(rewritten))
	if perr != nil {
		return nil, serverFail(fmt.Errorf("email: retryImages: blob put: %w", perr))
	}
	if err := r.h.store.Meta().ReplaceMessageBody(ctx, mid, ref, ref.Size, m.Envelope); err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			return nil, protojmap.NewMethodError("overQuota", "principal is over quota")
		}
		return nil, serverFail(fmt.Errorf("email: retryImages: replace body: %w", err))
	}
	// ReplaceMessageBody reset failed_image_count/state to zero/empty.
	// Re-populate when some URLs are still failing after this attempt.
	if len(result.StillFailedURLs) > 0 {
		encoded, eerr := extimg.EncodeRetainedState(extimg.RetainedState{
			URLs:     result.StillFailedURLs,
			Template: result.NewTemplate,
			DKIM:     retained.DKIM,
		})
		if eerr == nil {
			retryable, reason := extimg.DeriveFailureSignal(result.FailureCounts)
			_ = r.h.store.Meta().SetMessageFailedImages(ctx, mid, len(result.StillFailedURLs), encoded, retryable, string(reason))
		}
	}

	// ReplaceMessageBody's own change-feed row is tagged cause =
	// 'background' (REQ-EXTIMG-BG-INTERNAL-15: it is the worker's
	// convention, reused here since the rebuild logic is shared). A
	// retry is user-initiated, so append an explicit cause = 'user'
	// row per mailbox membership -- this is what makes Email/changes
	// and the EventSource push loop observe the change (REQ-EXTIMG-
	// RETRY: "advances Email state so the client re-renders via the
	// existing Email/changes / StateChange mechanism").
	for _, mm := range m.Mailboxes {
		if _, _, serr := r.h.store.Meta().UpdateMailboxModseqAndAppendChange(ctx, mm.MailboxID, store.StateChange{
			PrincipalID: m.PrincipalID,
			Kind:        store.EntityKindEmail,
			EntityID:    uint64(mid),
			Op:          store.ChangeOpUpdated,
			Cause:       store.ChangeCauseUser,
		}); serr != nil {
			r.h.logger.WarnContext(ctx, "email: retryImages: append user-cause state change failed",
				"message_id", uint64(mid), "mailbox_id", uint64(mm.MailboxID), "err", serr.Error())
		}
	}

	state, serr := currentState(ctx, r.h.store.Meta(), ownerPID)
	if serr != nil {
		return nil, serverFail(serr)
	}
	resp.NewState = state
	return resp, nil
}

// logRetryOutcome emits one INFO log line per Email/retryImages call,
// aggregating the re-fetch outcome the same way logExtImgOutcome does
// for the original ingest-time Internalize call (internal/protosmtp/
// extimg.go). Before this, a retry attempt left no server-side signal
// at all beyond the still-failing badge count the client already saw
// (issue #267).
func (r retryImagesHandler) logRetryOutcome(ctx context.Context, mid store.MessageID, attempted int, result extimg.RetryResult) {
	attrs := []slog.Attr{
		slog.String("activity", observe.ActivityUser),
		slog.String("subsystem", "protojmap"),
		slog.Uint64("message_id", uint64(mid)),
		slog.Int("attempted", attempted),
		slog.Int("retried_ok", result.RetriedOK),
		slog.Int("still_failed", len(result.StillFailedURLs)),
	}
	for outcome, n := range result.FailureCounts {
		if n > 0 {
			attrs = append(attrs, slog.Int("fail_"+string(outcome), n))
		}
	}
	r.h.logger.LogAttrs(ctx, slog.LevelInfo, "email: retryImages outcome", attrs...)
}
