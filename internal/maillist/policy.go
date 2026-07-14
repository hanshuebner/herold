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
//
// Poster identity (issue #189 verification fix): members-only and
// announce-only decide admission from an AUTHORITATIVE signal, never
// from the bare RFC 5322 From: header, which is attacker-controlled and
// trivially forged by any anonymous sender. authoritativePosterAddress
// is the sole entry point that produces a trusted address, using
// exactly one of two signals, in priority order:
//
//  1. SASL/submission authentication (ExpandInput.SubmissionPrincipalID):
//     when the accepting SMTP session authenticated as a local
//     principal, that principal proved control of their own herold
//     credentials for this session. Their CanonicalEmail is trusted
//     outright, regardless of what From: claims — this is the
//     "authenticated local principal" case the verification report
//     calls out.
//  2. DMARC/DKIM alignment (ExpandInput.Auth, computed once at SMTP
//     accept time): for anonymous/inbound mail, the From: domain is
//     trusted only when backed by a DMARC pass for that exact domain,
//     or — when the domain publishes no DMARC policy at all (DMARC
//     evaluates to AuthNone rather than AuthFail) — by an aligned,
//     passing DKIM signature for that domain. A DMARC fail is a hard
//     no: it is not weakened by falling back to a bare DKIM check DMARC
//     itself already considered and rejected.
//
// A From: address backed by NEITHER signal yields no authoritative
// address at all (authoritativePosterAddress's ok return is false), and
// posterAuthorized treats that identically to "not a member" — REJECTED,
// the same disposition an honest non-member gets, not held for
// moderation. This keeps the security property simple to state: these
// two policies admit authenticated identities only; a forged claim
// (whether of a member's address or the owner's) produces exactly the
// outcome a genuine stranger's post would, with the same audit-visible
// drop reason. It is deliberately NOT held: moderation-holding a forgery
// would create a second, avoidable class of operator-visible state for
// an outcome that is otherwise indistinguishable from ordinary
// non-membership.

import (
	"context"
	"errors"
	"strings"

	"github.com/hanshuebner/herold/internal/mailauth"
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
	// announce-only, non-conforming or unauthenticated poster).
	PostReject PostDecision = "reject"
)

// posterAddress extracts the RFC 5322 From: address mailparse's Envelope
// carries. This is NOT an authoritative identity by itself — it is
// attacker-controlled — and MUST NOT be used to decide members-only /
// announce-only admission on its own; see authoritativePosterAddress.
// It is still used verbatim for held-post display (FromAddress on
// store.MailingListHeldPost) and is distinct from the SMTP envelope MAIL
// FROM, which VERP/bounce processing uses for its own, unrelated
// purpose. Empty when the message carries no parseable From: address.
func posterAddress(parsed mailparse.Message) string {
	if len(parsed.Envelope.From) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Envelope.From[0].Address))
}

// domainOf returns the lowercased domain part of an addr-spec, or "" if
// addr has no "@".
func domainOf(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

// domainAligns reports whether fromDomain aligns with dkimDomain under
// RFC 7489 "relaxed" alignment: equal, or fromDomain is a subdomain of
// dkimDomain (e.g. d=example.com aligns with From: sub.example.com).
func domainAligns(dkimDomain, fromDomain string) bool {
	dkimDomain = strings.ToLower(dkimDomain)
	fromDomain = strings.ToLower(fromDomain)
	if dkimDomain == "" || fromDomain == "" {
		return false
	}
	if dkimDomain == fromDomain {
		return true
	}
	return strings.HasSuffix(fromDomain, "."+dkimDomain)
}

// posterAuthenticated reports whether addr's domain is authoritatively
// backed by auth (computed once at SMTP accept time) for an
// anonymous/inbound post — see the file doc comment's signal-2. Never
// consulted when a SASL/submission identity is present (signal 1 takes
// priority and skips this check entirely).
func posterAuthenticated(addr string, auth mailauth.AuthResults) bool {
	domain := domainOf(addr)
	if domain == "" {
		return false
	}
	if auth.DMARC.Status == mailauth.AuthPass && strings.EqualFold(auth.DMARC.HeaderFrom, domain) {
		return true
	}
	if auth.DMARC.Status == mailauth.AuthFail {
		// An explicit DMARC fail for this domain is a hard no: do not
		// weaken it by falling back to a bare DKIM check DMARC's own
		// alignment evaluation already considered.
		return false
	}
	// No conclusive DMARC verdict (e.g. AuthNone: the domain publishes
	// no DMARC policy at all). The minimum bar is an aligned, passing
	// DKIM signature for this exact domain.
	for _, d := range auth.DKIM {
		if d.Status == mailauth.AuthPass && domainAligns(d.Domain, domain) {
			return true
		}
	}
	return false
}

// authoritativePosterAddress resolves the ONE trusted identity for a
// post, per the file doc comment's two-signal priority order. ok is
// false when neither signal establishes a trusted address — callers
// MUST treat that exactly like "unknown address", never like "the From:
// header's claim".
func authoritativePosterAddress(ctx context.Context, meta store.Metadata, in ExpandInput) (addr string, ok bool, err error) {
	if in.SubmissionPrincipalID != nil {
		p, err := meta.GetPrincipalByID(ctx, *in.SubmissionPrincipalID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", false, nil
			}
			return "", false, err
		}
		return strings.ToLower(p.CanonicalEmail), true, nil
	}
	candidate := posterAddress(in.Parsed)
	if candidate == "" {
		return "", false, nil
	}
	if !posterAuthenticated(candidate, in.Auth) {
		return "", false, nil
	}
	return candidate, true, nil
}

// decidePosting applies ml.PostingPolicy to a post whose loop/abuse
// guards (REQ-MLIST-30..32) have already passed, returning the
// disposition and, for a hold, the reason recorded on the held-post row.
func decidePosting(ctx context.Context, meta store.Metadata, ml store.MailingList, in ExpandInput) (PostDecision, store.MailingListHeldPostReason, error) {
	switch ml.PostingPolicy {
	case "", store.MailingListPostingOpen:
		return PostAllow, "", nil
	case store.MailingListPostingModerated:
		return PostHold, store.MailingListHeldReasonModerated, nil
	case store.MailingListPostingMembersOnly:
		ok, err := posterAuthorized(ctx, meta, ml, in, false)
		if err != nil {
			return "", "", err
		}
		if ok {
			return PostAllow, "", nil
		}
		return PostReject, "", nil
	case store.MailingListPostingAnnounceOnly:
		ok, err := posterAuthorized(ctx, meta, ml, in, true)
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

// posterAuthorized reports whether in's post may post to ml under
// members-only (announceOnly=false) or announce-only (announceOnly=
// true), using ONLY the authoritative identity authoritativePosterAddress
// resolves (never the bare From: header): the list owner may always
// post regardless of roster membership; for members-only, an active
// roster member (external-address or principal, any delivery mode) may
// also post; for announce-only, only the owner may. A post with no
// authoritative identity (no SASL session and no DMARC/DKIM-backed
// From:) is never authorized, regardless of what From: claims.
func posterAuthorized(ctx context.Context, meta store.Metadata, ml store.MailingList, in ExpandInput, announceOnly bool) (bool, error) {
	addr, ok, err := authoritativePosterAddress(ctx, meta, in)
	if err != nil {
		return false, err
	}
	if !ok {
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
