package maillist

// policy.go — REQ-MLIST-80 posting-policy enforcement at post time (the
// v2 moderation milestone, issue #189). Four named policies, each with a
// single deterministic disposition for a post the policy does not admit:
//
//   - open:          any address may post (S1 default; unchanged by this
//                     file).
//   - members-only:  only an active roster member (or the list owner)
//                     may post; a non-member post is REJECTED (dropped
//                     with an audit record — the same "dropped, not
//                     delivered, no DSN" disposition the REQ-MLIST-30..32
//                     loop/abuse guards already use, since by the time
//                     Expand runs the SMTP transaction has already been
//                     accepted and there is no 5xx left to give the
//                     poster).
//   - announce-only:  only the list owner may post; every other post is
//                     REJECTED, same disposition as members-only.
//   - moderated:      EVERY post, regardless of sender, is HELD for an
//                     explicit owner/moderator approve/reject/discard
//                     decision rather than being fanned out or rejected
//                     outright.
//
// A rejected post never reaches the queue or the archive; a held post
// reaches neither until approved. Both dispositions are decided in
// Expand BEFORE ShapeMessage/ARC-seal/enqueue runs, so "no fan-out, no
// archive copy for a non-conforming post" holds by construction — fanOut
// (expand.go) is the only place queue submission and archive filing
// happen, and neither PostReject nor PostHold ever reaches it directly.

import (
	"context"
	"errors"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// PostDecision is the REQ-MLIST-80 posting-policy verdict for one post.
type PostDecision string

const (
	// PostAllow fans the post out through the normal S1 path.
	PostAllow PostDecision = "allow"
	// PostHold queues the post for an owner/moderator decision
	// (moderated policy only).
	PostHold PostDecision = "hold"
	// PostReject drops the post with an audit record (members-only /
	// announce-only, non-conforming poster).
	PostReject PostDecision = "reject"
)

// posterAddress extracts the RFC 5322 From: address mailparse's Envelope
// carries — the identity posting-policy decisions are made against,
// distinct from the SMTP envelope MAIL FROM (which VERP/bounce
// processing uses for its own, unrelated purpose). Empty when the
// message carries no parseable From: address.
func posterAddress(parsed mailparse.Message) string {
	if len(parsed.Envelope.From) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Envelope.From[0].Address))
}

// decidePosting applies ml.PostingPolicy to a post whose loop/abuse
// guards (REQ-MLIST-30..32) have already passed, returning the
// disposition and, for a hold, the reason recorded on the held-post row.
func decidePosting(ctx context.Context, meta store.Metadata, ml store.MailingList, parsed mailparse.Message) (PostDecision, store.MailingListHeldPostReason, error) {
	switch ml.PostingPolicy {
	case "", store.MailingListPostingOpen:
		return PostAllow, "", nil
	case store.MailingListPostingModerated:
		return PostHold, store.MailingListHeldReasonModerated, nil
	case store.MailingListPostingMembersOnly:
		ok, err := posterAuthorized(ctx, meta, ml, parsed, false)
		if err != nil {
			return "", "", err
		}
		if ok {
			return PostAllow, "", nil
		}
		return PostReject, "", nil
	case store.MailingListPostingAnnounceOnly:
		ok, err := posterAuthorized(ctx, meta, ml, parsed, true)
		if err != nil {
			return "", "", err
		}
		if ok {
			return PostAllow, "", nil
		}
		return PostReject, "", nil
	default:
		// Unknown/legacy posting_policy value: fail open to the S1
		// behaviour rather than silently blocking every post on a list
		// whose column holds something this binary version does not
		// recognise (e.g. a downgrade after a forward migration).
		return PostAllow, "", nil
	}
}

// posterAuthorized reports whether parsed's From: address may post to ml
// under members-only (announceOnly=false) or announce-only
// (announceOnly=true): the list owner may always post regardless of
// roster membership; for members-only, an active roster member
// (external-address or principal, any delivery mode) may also post; for
// announce-only, only the owner may.
func posterAuthorized(ctx context.Context, meta store.Metadata, ml store.MailingList, parsed mailparse.Message, announceOnly bool) (bool, error) {
	addr := posterAddress(parsed)
	if addr == "" {
		return false, nil
	}
	owner, err := meta.GetPrincipalByID(ctx, ml.OwnerID)
	switch {
	case err == nil:
		if strings.EqualFold(owner.CanonicalEmail, addr) {
			return true, nil
		}
	case !errors.Is(err, store.ErrNotFound):
		return false, err
	}
	if announceOnly {
		return false, nil
	}
	return isActiveMember(ctx, meta, ml, addr)
}

// isActiveMember reports whether addr is an active roster member of ml,
// matching either an external-address row directly (the hot path — one
// indexed lookup) or a principal member whose CanonicalEmail equals
// addr (falls back to a streamed roster scan, bounded by the same
// "hundreds of members" scale StreamActiveMembers/StreamActiveEachMembers
// already assume elsewhere in this package).
func isActiveMember(ctx context.Context, meta store.Metadata, ml store.MailingList, addr string) (bool, error) {
	m, err := meta.GetMailingListMemberByAddress(ctx, ml.ID, addr)
	if err == nil {
		return m.State == store.MailingListMemberActive, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	p, err := meta.GetPrincipalByEmail(ctx, addr)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	found := false
	err = store.StreamActiveMembers(ctx, meta, ml.ID, func(mem store.MailingListMember) error {
		if mem.PrincipalID != nil && *mem.PrincipalID == p.ID {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}
