package mailbox

import (
	"context"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// renderMailbox converts a store.Mailbox into the JMAP wire form,
// computing totalEmails / unreadEmails by paginating through the
// mailbox's messages, and deriving myRights from ACL when the caller
// is not the mailbox owner.
//
// totalThreads / unreadThreads collapse to totalEmails / unreadEmails
// for v1 because we have not yet wired the JMAP Thread datatype (the
// parallel agent's surface). RFC 8621 permits this — Thread/get returns
// "unknownDataType" and clients fall back to per-Email rendering.
func renderMailbox(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	mb store.Mailbox,
) (jmapMailbox, error) {
	totalEmails, unreadEmails, err := countMessages(ctx, meta, mb.ID)
	if err != nil {
		return jmapMailbox{}, err
	}

	rights, err := rightsForPrincipal(ctx, meta, pid, mb)
	if err != nil {
		return jmapMailbox{}, err
	}

	var parent *jmapID
	if mb.ParentID != 0 {
		s := jmapIDFromMailbox(mb.ParentID)
		parent = &s
	}

	var color *string
	if mb.Color != nil {
		v := *mb.Color
		color = &v
	}
	return jmapMailbox{
		ID:            jmapIDFromMailbox(mb.ID),
		Name:          mb.Name,
		ParentID:      parent,
		Role:          roleFromAttributes(mb.Attributes),
		SortOrder:     mb.SortOrder,
		TotalEmails:   totalEmails,
		UnreadEmails:  unreadEmails,
		TotalThreads:  totalEmails,
		UnreadThreads: unreadEmails,
		MyRights:      rights,
		IsSubscribed:  mb.Attributes&store.MailboxAttrSubscribed != 0,
		Color:         color,
	}, nil
}

// countMessages returns (total, unread) for mailboxID via a single
// SQL aggregate (Metadata.CountMessages). The earlier implementation
// paginated through ListMessages decoding every Message struct,
// which on a freshly-imported Gmail mailbox (100k+ rows in the
// largest folder, called once per mailbox per Mailbox/get) made the
// suite's polling pin multiple cores.
func countMessages(
	ctx context.Context,
	meta store.Metadata,
	mailboxID store.MailboxID,
) (total, unread int64, err error) {
	t, u, cerr := meta.CountMessages(ctx, mailboxID)
	if cerr != nil {
		return 0, 0, fmt.Errorf("mailbox: count messages: %w", cerr)
	}
	return t, u, nil
}

// rightsForPrincipal returns the JMAP myRights envelope for pid against
// mb. The owning principal sees the full rights set; non-owners receive
// the projection of their ACL row (plus any "anyone" row).
func rightsForPrincipal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	mb store.Mailbox,
) (myRights, error) {
	if mb.PrincipalID == pid {
		return rightsForOwner(), nil
	}
	rows, err := meta.GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return myRights{}, fmt.Errorf("mailbox: read acl: %w", err)
	}
	var combined store.ACLRights
	for _, r := range rows {
		if r.PrincipalID == nil { // anyone
			combined |= r.Rights
			continue
		}
		if *r.PrincipalID == pid {
			combined |= r.Rights
		}
	}
	return rightsFromACL(combined), nil
}

// listMailboxesForAccount returns the mailboxes that belong to the
// requested owner account and that the caller can see (REQ-PROTO-33).
// Per RFC 8620 §2 each JMAP accountId scopes the response: caller's
// own account returns only her own mailboxes — mailboxes she reaches by
// ACL on another principal appear under that principal's accountId as a
// secondary account on her session. For caller != owner the result is
// further filtered to mailboxes the caller has Lookup right on (direct
// ACL row or "anyone").
func listMailboxesForAccount(
	ctx context.Context,
	meta store.Metadata,
	callerPID, ownerPID store.PrincipalID,
) ([]store.Mailbox, error) {
	if callerPID == ownerPID {
		owned, err := meta.ListMailboxes(ctx, ownerPID)
		if err != nil {
			return nil, fmt.Errorf("mailbox: list mailboxes: %w", err)
		}
		return owned, nil
	}
	shared, err := meta.ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return nil, fmt.Errorf("mailbox: list shared: %w", err)
	}
	out := make([]store.Mailbox, 0, len(shared))
	for _, mb := range shared {
		if mb.PrincipalID == ownerPID {
			out = append(out, mb)
		}
	}
	return out, nil
}
