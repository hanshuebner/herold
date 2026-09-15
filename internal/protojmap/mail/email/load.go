package email

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// listMailboxesForAccount returns the mailboxes that belong to the
// requested owner account and that the caller can see (REQ-PROTO-33).
// Per RFC 8620 §2, each JMAP accountId scopes the response: alice's own
// account returns only her own mailboxes, never mailboxes she sees by
// ACL on bob — those appear under bob's accountId as a secondary
// account on her session instead. For caller != owner the result is
// further filtered to the mailboxes the caller has Lookup right on
// (direct ACL row or "anyone") -- unless ownerPID is the caller's own
// sub-account (REQ-SUBACCT-04), in which case the full list is
// returned, matching the same-account fast path.
func listMailboxesForAccount(
	ctx context.Context,
	meta store.Metadata,
	callerPID, ownerPID store.PrincipalID,
) ([]store.Mailbox, error) {
	if protojmap.HasOwnerAccess(ctx, meta, callerPID, ownerPID) {
		owned, err := meta.ListMailboxes(ctx, ownerPID)
		if err != nil {
			return nil, fmt.Errorf("email: list mailboxes: %w", err)
		}
		return owned, nil
	}
	shared, err := meta.ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return nil, fmt.Errorf("email: list shared mailboxes: %w", err)
	}
	out := make([]store.Mailbox, 0, len(shared))
	for _, mb := range shared {
		if mb.PrincipalID == ownerPID {
			out = append(out, mb)
		}
	}
	return out, nil
}

// loadMessageForPrincipal returns the message if it lives in a mailbox
// the principal can access. ErrNotFound is mapped to errMessageMissing
// so the JMAP wire form can render "notFound" without leaking the
// existence of out-of-scope mailboxes.
func loadMessageForPrincipal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	id store.MessageID,
) (store.Message, error) {
	m, err := meta.GetMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, errMessageMissing
		}
		return store.Message{}, fmt.Errorf("email: get message: %w", err)
	}
	mb, err := meta.GetMailboxByID(ctx, m.MailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, errMessageMissing
		}
		return store.Message{}, fmt.Errorf("email: get mailbox: %w", err)
	}
	if protojmap.HasOwnerAccess(ctx, meta, pid, mb.PrincipalID) {
		return m, nil
	}
	rows, err := meta.GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return store.Message{}, fmt.Errorf("email: get mailbox acl: %w", err)
	}
	for _, r := range rows {
		if r.PrincipalID == nil {
			if r.Rights&store.ACLRightLookup != 0 {
				return m, nil
			}
			continue
		}
		if *r.PrincipalID == pid && r.Rights&store.ACLRightLookup != 0 {
			return m, nil
		}
	}
	return store.Message{}, errMessageMissing
}

// errMessageMissing is the unified "looks like never existed" error.
var errMessageMissing = errors.New("email: not found or not visible")

// aclRightsForCaller returns the combined ACLRights mask for callerPID
// against mb. The owning principal sees every right; non-owners receive
// the OR of their direct ACL row and any "anyone" row -- except a
// sub-account's parent (REQ-SUBACCT-04), who also sees every right on
// their own sub-account's mailboxes without needing an ACL row. Used by
// the cross-account create/update/destroy paths to gate operations on
// specific ACL bits (Insert / Write / Seen / DeleteMessage / Expunge).
func aclRightsForCaller(
	ctx context.Context,
	meta store.Metadata,
	callerPID store.PrincipalID,
	mb store.Mailbox,
) (store.ACLRights, error) {
	if protojmap.HasOwnerAccess(ctx, meta, callerPID, mb.PrincipalID) {
		return store.ACLRightsAll, nil
	}
	rows, err := meta.GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return 0, fmt.Errorf("email: read acl: %w", err)
	}
	var combined store.ACLRights
	for _, r := range rows {
		if r.PrincipalID == nil {
			combined |= r.Rights
			continue
		}
		if *r.PrincipalID == callerPID {
			combined |= r.Rights
		}
	}
	return combined, nil
}

// listAccountMessages returns every message the caller can see in the
// requested owner account (REQ-PROTO-33), one store.Message per message
// -- not per mailbox membership. callerPID and ownerPID are the same
// for the caller's own account (the prior listPrincipalMessages path);
// for a foreign account it scopes to mailboxes owned by ownerPID and
// visible to callerPID via ACL. The implementation is keyset-paged per
// mailbox so a principal with millions of messages does not hold the
// whole list in memory at once at the storage layer; the returned
// slice is bounded only by the caller.
//
// meta.ListMessages is scoped to a single mailbox, so a message that
// belongs to several mailboxes (Gmail-style labels) surfaces once per
// owned mailbox it sits in, each row's Mailboxes carrying only that
// row's own membership (see storesqlite/storepg scanMessage). Those
// rows are merged here into one store.Message per message ID whose
// Mailboxes field carries the union of every membership, so the
// caller (the Email/query filter matcher) can evaluate inMailbox /
// inMailboxOtherThan against the message's complete mailbox set per
// RFC 8621 section 4.4.1 instead of one row's single MailboxID (re
// #402).
//
// Convenience-field tie-break (re #402 verifier round): once every
// membership is merged, the convenience fields (MailboxID / UID /
// ModSeq / Flags / Keywords / SnoozedUntil / ReceivedTo) are
// overwritten from the membership with the lowest MailboxID -- the
// same ORDER BY mailbox_id tie-break storesqlite/storepg loadMailboxes
// applies for an unscoped GetMessage (mailboxID==0). Email/get always
// renders keywords via GetMessage, so Email/query's keyword predicates
// (hasKeyword, notKeyword, the hasKeyword sort comparator, all of
// which read the convenience fields off store.Message rather than
// iterating Mailboxes) now agree with what Email/get reports for the
// same message, independent of which mailbox ListMailboxes happened to
// return first. Per-mailbox flags/keywords otherwise stay independent
// (UpdateMessageFlags contract above); this tie-break only decides
// which single membership's state a mailbox-independent JMAP property
// exposes for a message filed under several mailboxes.
func listAccountMessages(
	ctx context.Context,
	meta store.Metadata,
	callerPID, ownerPID store.PrincipalID,
) ([]store.Message, error) {
	mailboxes, err := listMailboxesForAccount(ctx, meta, callerPID, ownerPID)
	if err != nil {
		return nil, err
	}
	const page = 1000
	seen := make(map[store.MessageID]int)
	var out []store.Message
	for _, mb := range mailboxes {
		var cursor store.UID
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			batch, ferr := meta.ListMessages(ctx, mb.ID, store.MessageFilter{
				AfterUID:     cursor,
				Limit:        page,
				WithEnvelope: true,
			})
			if ferr != nil {
				return nil, fmt.Errorf("email: list messages: %w", ferr)
			}
			for _, m := range batch {
				if idx, dup := seen[m.ID]; dup {
					out[idx].Mailboxes = append(out[idx].Mailboxes, m.Mailboxes...)
					continue
				}
				seen[m.ID] = len(out)
				out = append(out, m)
			}
			if len(batch) < page {
				break
			}
			cursor = batch[len(batch)-1].UID
		}
	}
	for i := range out {
		applyCanonicalMembership(&out[i])
	}
	return out, nil
}

// applyCanonicalMembership overwrites m's convenience fields
// (MailboxID / UID / ModSeq / Flags / Keywords / SnoozedUntil /
// ReceivedTo) from the entry in m.Mailboxes with the lowest MailboxID,
// matching the ORDER BY mailbox_id tie-break storesqlite/storepg
// loadMailboxes uses for an unscoped GetMessage. A no-op when Mailboxes
// has zero or one entry (the merge above always leaves at least the
// row's own membership in place).
func applyCanonicalMembership(m *store.Message) {
	if len(m.Mailboxes) == 0 {
		return
	}
	canonical := m.Mailboxes[0]
	for _, mm := range m.Mailboxes[1:] {
		if mm.MailboxID < canonical.MailboxID {
			canonical = mm
		}
	}
	m.MailboxID = canonical.MailboxID
	m.UID = canonical.UID
	m.ModSeq = canonical.ModSeq
	m.Flags = canonical.Flags
	m.Keywords = canonical.Keywords
	m.SnoozedUntil = canonical.SnoozedUntil
	m.ReceivedTo = canonical.ReceivedTo
}
