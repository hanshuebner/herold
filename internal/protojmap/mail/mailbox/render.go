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

// countMessages walks the mailbox's messages with a keyset cursor and
// returns (total, unread). Bounded by a per-page limit; iterates until
// a short page is returned by ListMessages.
func countMessages(
	ctx context.Context,
	meta store.Metadata,
	mailboxID store.MailboxID,
) (total, unread int64, err error) {
	const page = 1000
	var cursor store.UID
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		batch, ferr := meta.ListMessages(ctx, mailboxID, store.MessageFilter{
			AfterUID: cursor,
			Limit:    page,
		})
		if ferr != nil {
			return 0, 0, fmt.Errorf("mailbox: count messages: %w", ferr)
		}
		for _, m := range batch {
			total++
			if m.Flags&store.MessageFlagSeen == 0 {
				unread++
			}
			cursor = m.UID
		}
		if len(batch) < page {
			return total, unread, nil
		}
	}
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

// listAccessibleMailboxes returns the mailboxes owned by pid. Cross-
// account JMAP routing scopes mailbox visibility per-accountId: a
// client that wants to see mailboxes from a foreign account issues a
// separate request with that account's id, and the cross-account
// branch in each handler queries ListMailboxesAccessibleBy filtered to
// that owner. Lumping owned + shared into one accountId would violate
// RFC 8621 §2 (mailboxes are per-Account) and would surface the same
// foreign mailbox under both accountIds.
func listAccessibleMailboxes(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Mailbox, error) {
	owned, err := meta.ListMailboxes(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("mailbox: list owned: %w", err)
	}
	return owned, nil
}
