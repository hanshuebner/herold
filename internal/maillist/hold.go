package maillist

// hold.go — REQ-MLIST-80 held-post storage and lifecycle (the moderated
// posting policy and the moderation surface, v2 milestone, issue #189):
// holding a non-fanned-out post, and the owner/moderator approve/reject/
// discard decisions. A held post is a store row (store.MailingListHeldPost),
// not an in-memory queue, so it survives a restart; its raw message bytes
// live in the same content-addressed blob store every other message body
// does, kept alive by a caller-managed blob_refs reference
// (store.Metadata.IncRefBlob/DecRefBlob via Insert/DecideMailingListHeldPost,
// the same pattern per-identity avatar blobs use) until the post is
// decided.
//
// Approving a held post runs it through fanOut (expand.go) -- the SAME
// shape/ARC-seal/archive/enqueue tail Expand itself uses for an allowed
// post -- so List-* headers, VERP, ARC sealing, and archive filing are
// identical regardless of whether the post was ever held. Rejecting or
// discarding never calls fanOut: no queue row and no archive copy is
// ever created for a held post that is not approved.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// heldOutcome is the fan-out-metric outcome label recorded when a post
// is held rather than delivered, unsealed, or dropped (REQ-MLIST-80).
const heldOutcome = "held"

// holdPost persists in's raw bytes as a new held-post row (REQ-MLIST-80)
// awaiting an owner/moderator decision. The blob is IncRef'd atomically
// with the row insert (store.Metadata.InsertMailingListHeldPost's own
// contract), so it cannot become GC-eligible while held.
func (e *Expander) holdPost(ctx context.Context, ml store.MailingList, in ExpandInput, reason store.MailingListHeldPostReason) (store.MailingListHeldPost, error) {
	if e.Blobs == nil {
		return store.MailingListHeldPost{}, fmt.Errorf("maillist: no Blobs wired; cannot hold post")
	}
	ref, err := e.Blobs.Put(ctx, bytes.NewReader(in.Raw))
	if err != nil {
		return store.MailingListHeldPost{}, fmt.Errorf("maillist: persist held post blob: %w", err)
	}
	authJSON, err := json.Marshal(in.Auth)
	if err != nil {
		// mailauth.AuthResults is plain typed fields; marshalling cannot
		// fail in practice. Fall back to an empty object rather than
		// blocking the hold on an encode error -- approval degrades to
		// ARC-sealing with an empty "prior" hop, not to losing the post.
		authJSON = []byte("{}")
	}
	held, err := e.Meta.InsertMailingListHeldPost(ctx, store.MailingListHeldPost{
		ListID:          ml.ID,
		BlobHash:        ref.Hash,
		BlobSize:        ref.Size,
		FromAddress:     posterAddress(in.Parsed),
		Subject:         in.Parsed.Envelope.Subject,
		MessageID:       in.Parsed.Envelope.MessageID,
		AuthResultsJSON: string(authJSON),
		Reason:          reason,
	})
	if err != nil {
		return store.MailingListHeldPost{}, fmt.Errorf("maillist: insert held post row: %w", err)
	}
	return held, nil
}

// readHeldBlob reads held's full raw message body back from the blob
// store. Held posts are decided individually, not fanned out N times,
// so reading the whole body into memory here (mirroring how Expand
// itself receives ExpandInput.Raw as a single []byte) is proportionate.
func (e *Expander) readHeldBlob(ctx context.Context, held store.MailingListHeldPost) ([]byte, error) {
	rc, err := e.Blobs.Get(ctx, held.BlobHash)
	if err != nil {
		return nil, fmt.Errorf("maillist: read held post blob %s: %w", held.BlobHash, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("maillist: read held post blob %s: %w", held.BlobHash, err)
	}
	return raw, nil
}

// ApproveHeldPost fans out held post id through the normal S1 path
// (REQ-MLIST-80: "an approved held post fans out normally"), then
// transitions the row to approved and releases the held post's own blob
// reference -- safe because fanOut's own queue submissions (and any
// archive filing) have already taken their own references by the time
// this call runs, so the blob never drops to zero refs in between.
// Returns store.ErrConflict if the post is not currently pending
// (already decided by a prior or concurrent call).
func (e *Expander) ApproveHeldPost(ctx context.Context, id store.MailingListHeldPostID, approver store.PrincipalID) (ExpandResult, error) {
	held, err := e.Meta.GetMailingListHeldPost(ctx, id)
	if err != nil {
		return ExpandResult{}, err
	}
	if held.Status != store.MailingListHeldPostPending {
		return ExpandResult{}, store.ErrConflict
	}
	ml, err := e.Meta.GetMailingList(ctx, held.ListID)
	if err != nil {
		return ExpandResult{}, err
	}
	if e.Blobs == nil {
		return ExpandResult{}, fmt.Errorf("maillist: no Blobs wired; cannot approve held post")
	}
	raw, err := e.readHeldBlob(ctx, held)
	if err != nil {
		return ExpandResult{}, err
	}
	parsed, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		return ExpandResult{}, fmt.Errorf("maillist: reparse held post %d: %w", id, err)
	}
	var auth mailauth.AuthResults
	if held.AuthResultsJSON != "" {
		// Best effort: a decode failure (should not occur -- holdPost
		// always writes valid JSON) degrades to an empty "prior" hop for
		// ARC-sealing rather than blocking the approval.
		_ = json.Unmarshal([]byte(held.AuthResultsJSON), &auth)
	}
	res, err := e.fanOut(ctx, ml, ExpandInput{List: ml, Parsed: parsed, Raw: raw, Auth: auth})
	if err != nil {
		return ExpandResult{}, err
	}
	if _, derr := e.Meta.DecideMailingListHeldPost(ctx, id, store.MailingListHeldPostApproved, approver, "", e.Clock.Now()); derr != nil {
		e.Logger.ErrorContext(ctx, "maillist: held post fanned out but the approve transition failed; its blob reference is not yet released",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.Uint64("held_post_id", uint64(id)),
			slog.String("err", derr.Error()))
		return res, derr
	}
	e.audit(ctx, ml, "maillist.held.approved", fmt.Sprintf("held_post_id=%d approver=%d", id, approver))
	return res, nil
}

// RejectHeldPost transitions held post id to rejected without ever
// running fanOut: no queue row and no archive copy is created. The held
// post's blob reference is released, making it GC-eligible once nothing
// else references it.
func (e *Expander) RejectHeldPost(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error) {
	return e.decideHeldPost(ctx, id, store.MailingListHeldPostRejected, actor, note, "maillist.held.rejected")
}

// DiscardHeldPost transitions held post id to discarded. Distinct from
// Reject only in the operator-visible action label; both disposals
// share the same never-fan-out, blob-release semantics.
func (e *Expander) DiscardHeldPost(ctx context.Context, id store.MailingListHeldPostID, actor store.PrincipalID, note string) (store.MailingListHeldPost, error) {
	return e.decideHeldPost(ctx, id, store.MailingListHeldPostDiscarded, actor, note, "maillist.held.discarded")
}

func (e *Expander) decideHeldPost(ctx context.Context, id store.MailingListHeldPostID, status store.MailingListHeldPostStatus, actor store.PrincipalID, note, auditAction string) (store.MailingListHeldPost, error) {
	held, err := e.Meta.GetMailingListHeldPost(ctx, id)
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	ml, err := e.Meta.GetMailingList(ctx, held.ListID)
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	decided, err := e.Meta.DecideMailingListHeldPost(ctx, id, status, actor, note, e.Clock.Now())
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	e.audit(ctx, ml, auditAction, fmt.Sprintf("held_post_id=%d actor=%d", id, actor))
	return decided, nil
}
