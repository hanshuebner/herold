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

	return jmapMailbox{
		ID:            jmapIDFromMailbox(mb.ID),
		Name:          mb.Name,
		ParentID:      parent,
		Role:          roleFromAttributes(mb.Attributes),
		SortOrder:     0,
		TotalEmails:   totalEmails,
		UnreadEmails:  unreadEmails,
		TotalThreads:  totalEmails,
		UnreadThreads: unreadEmails,
		MyRights:      rights,
		IsSubscribed:  mb.Attributes&store.MailboxAttrSubscribed != 0,
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

// listAccessibleMailboxes returns every mailbox the principal can see:
// the owned set (ListMailboxes) plus the ACL-shared set
// (ListMailboxesAccessibleBy), de-duplicated by MailboxID.
func listAccessibleMailboxes(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Mailbox, error) {
	owned, err := meta.ListMailboxes(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("mailbox: list owned: %w", err)
	}
	shared, err := meta.ListMailboxesAccessibleBy(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("mailbox: list shared: %w", err)
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
