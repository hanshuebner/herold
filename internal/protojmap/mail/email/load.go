package email

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// listAccessibleMailboxes returns every mailbox the principal owns or
// can see via ACL. Mirrors the mailbox sub-package's accessor; we
// duplicate a thin version here so the email handlers do not depend on
// the mailbox package's internals.
func listAccessibleMailboxes(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Mailbox, error) {
	owned, err := meta.ListMailboxes(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("email: list mailboxes: %w", err)
	}
	shared, err := meta.ListMailboxesAccessibleBy(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("email: list shared mailboxes: %w", err)
	}
	if len(shared) == 0 {
		return owned, nil
	}
	seen := make(map[store.MailboxID]struct{}, len(owned))
	for _, mb := range owned {
		seen[mb.ID] = struct{}{}
	}
	for _, mb := range shared {
		if _, dup := seen[mb.ID]; dup {
			continue
		}
		owned = append(owned, mb)
		seen[mb.ID] = struct{}{}
	}
	return owned, nil
}

// listMailboxesForAccount returns the mailboxes the caller can see in
// the requested owner account (REQ-PROTO-33). For caller == owner this
// is the union of owned + ACL-shared. For caller != owner this is only
// the mailboxes owned by `owner` that the caller has Lookup right on
// via a direct ACL row or an "anyone" row.
func listMailboxesForAccount(
	ctx context.Context,
	meta store.Metadata,
	callerPID, ownerPID store.PrincipalID,
) ([]store.Mailbox, error) {
	if callerPID == ownerPID {
		return listAccessibleMailboxes(ctx, meta, callerPID)
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
	if mb.PrincipalID == pid {
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
// the OR of their direct ACL row and any "anyone" row. Used by the
// cross-account create/update/destroy paths to gate operations on
// specific ACL bits (Insert / Write / Seen / DeleteMessage / Expunge).
func aclRightsForCaller(
	ctx context.Context,
	meta store.Metadata,
	callerPID store.PrincipalID,
	mb store.Mailbox,
) (store.ACLRights, error) {
	if mb.PrincipalID == callerPID {
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
// requested owner account (REQ-PROTO-33). callerPID and ownerPID are
// the same for the caller's own account (the prior listPrincipalMessages
// path); for a foreign account it scopes to mailboxes owned by
// ownerPID and visible to callerPID via ACL. The implementation is
// keyset-paged per mailbox so a principal with millions of messages
// does not hold the whole list in memory at once at the storage layer;
// the returned slice is bounded only by the caller.
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
			out = append(out, batch...)
			if len(batch) < page {
				break
			}
			cursor = batch[len(batch)-1].UID
		}
	}
	return out, nil
}
