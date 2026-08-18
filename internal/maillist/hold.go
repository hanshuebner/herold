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
	"errors"
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

// heldPostIdempotencyPrefix computes the per-member queue.Submission
// IdempotencyKey prefix used for every fan-out this held post ever
// triggers (its first approval attempt and any later resume of a stuck
// MailingListHeldPostApproving row), so a retried or concurrently
// racing fan-out of the SAME held post can never mail a member twice
// (issue #189 verification fix).
func heldPostIdempotencyPrefix(id store.MailingListHeldPostID) string {
	return fmt.Sprintf("mlist-held-%d", id)
}

// ApproveHeldPost approves held post id (REQ-MLIST-80: "an approved
// held post fans out normally"), fanning it out through the normal S1
// path exactly once no matter how many concurrent OR sequential
// (crash-resumed) callers invoke this method for the same id (issue
// #189 verification fix, second hardening pass). Three steps, and
// concurrency is arbitrated in EXACTLY ONE of them:
//
//  1. Claim: ClaimMailingListHeldPostForApproval is a single atomic
//     UPDATE that transitions the row to 'approving' -- matching either
//     a fresh 'pending' row (the normal case) or a stale 'approving' row
//     whose claim has aged past store.MailingListHeldPostApprovalLeaseTTL
//     (a crash-resume case). This call is made UNCONDITIONALLY, with no
//     prior read of the row's status: there is no read-then-branch here
//     to race. Of any number of concurrent or sequential callers racing
//     this method for the same id, AT MOST ONE wins the claim; every
//     other caller's claim affects zero rows, returns store.ErrConflict
//     here (a genuinely concurrent peer still holds a fresh claim, or
//     the row is in a terminal state), and NEVER reaches step 2.
//  2. fanOut: the winning caller reads the blob, reparses it, and runs
//     the SAME shape/ARC-seal/archive/enqueue tail Expand uses for a
//     never-held post, with in.HeldPostID set so both the per-member
//     queue idempotency key and fileArchive's own claim make re-running
//     this step for the SAME held post (a crash-resume) safe.
//  3. Finalize: FinalizeMailingListHeldPostApproval atomically
//     transitions approving -> approved and releases the held post's
//     own blob_refs reference (fan-out's own queue/archive references
//     already keep the blob alive by this point).
//
// Crash recovery: if the process dies between steps 1 and 3, the row is
// left durably, visibly in MailingListHeldPostApproving -- never
// silently reverted to "undecided" and never silently reported as
// fanned out when it was not. Once its claim ages past the lease TTL, a
// LATER call to ApproveHeldPost on such a row reclaims it via the SAME
// step-1 UPDATE (not a separate resume code path) and resumes: fanOut's
// per-member idempotency keys mean any member already mailed by the
// earlier, interrupted attempt is skipped rather than mailed again, and
// fileArchive's own claim means the archive gets no second copy either.
// There is no automatic background sweep for a stuck 'approving' row in
// this version; an operator (or the moderation UI) re-issuing the
// approve action after the lease has gone stale is the recovery path,
// and the held-post list surfaces 'approving' as a status distinct from
// both 'pending' and 'approved' so a stuck row is visible rather than
// silently lost.
func (e *Expander) ApproveHeldPost(ctx context.Context, id store.MailingListHeldPostID, approver store.PrincipalID) (ExpandResult, error) {
	held, err := e.Meta.ClaimMailingListHeldPostForApproval(ctx, id, approver, e.Clock.Now())
	if err != nil {
		// ErrConflict here means either a genuinely concurrent peer
		// still holds a fresh (non-stale) claim on this row, or the row
		// is already in a terminal state (approved/rejected/discarded);
		// either way this call must not proceed to fanOut.
		// ErrNotFound means id does not exist. This is the ONLY place
		// this method arbitrates concurrency -- there is no read of
		// held.Status anywhere in this function to branch on.
		return ExpandResult{}, err
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
	parsed, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewLenientParseOptions())
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
	res, err := e.fanOut(ctx, ml, ExpandInput{
		List:       ml,
		Parsed:     parsed,
		Raw:        raw,
		Auth:       auth,
		HeldPostID: id,
	})
	if err != nil {
		// fanOut itself failed outright (not a per-member skip, which
		// fanOut never surfaces as an error): the row stays 'approving'
		// for a later resume; nothing here has released its blob ref.
		return ExpandResult{}, err
	}
	if _, ferr := e.Meta.FinalizeMailingListHeldPostApproval(ctx, id, e.Clock.Now()); ferr != nil {
		if errors.Is(ferr, store.ErrConflict) {
			// A concurrent resume already finalized this row (fan-out
			// ran twice, harmlessly, thanks to the idempotency keys, but
			// only one finalize could win the CAS). Re-read: if the row
			// is now Approved, the desired end state was reached by
			// someone else and this call reports success too, rather
			// than surfacing a spurious error for an outcome that is
			// actually correct.
			final, gerr := e.Meta.GetMailingListHeldPost(ctx, id)
			if gerr == nil && final.Status == store.MailingListHeldPostApproved {
				e.audit(ctx, ml, "maillist.held.approved", fmt.Sprintf("held_post_id=%d approver=%d", id, approver))
				return res, nil
			}
		}
		e.Logger.ErrorContext(ctx, "maillist: held post fanned out but the finalize transition failed; the row remains 'approving' for a later resume",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.Uint64("held_post_id", uint64(id)),
			slog.String("err", ferr.Error()))
		return res, ferr
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
