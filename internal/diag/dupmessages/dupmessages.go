// Package dupmessages implements the recovery path behind `herold diag
// duplicate-messages list|merge` (re #496): SMTP delivery and the
// IMAP/JMAP importers each dedup independently by Message-ID, so a
// message that reaches one principal via two ingest paths whose stored
// bytes differ (e.g. an external IMAP mirror's raw copy of a message that
// also arrives, seconds later, by direct SMTP delivery through an alias
// forward) can still end up as two live rows sharing one Message-ID in
// the same thread -- the ingest-time dedup guard only folds an exact
// stored-blob match.
//
// List is a dry run: it reports every Message-ID a principal holds more
// than one live row for, oldest row first. Writes nothing.
//
// Merge folds every duplicate in a group onto its oldest row -- the
// survivor gains every mailbox membership any duplicate held, each
// membership's $seen flag and keywords OR'd in (so the survivor ends up
// seen if any duplicate was seen) -- and removes every other row through
// the normal destroy path (Metadata.ExpungeMessages), so the change feed
// sees the removal exactly like an ordinary Email/set destroy. Each
// removed row's blob hash and mailbox memberships are returned so a
// caller can record an undo log: the blob store is never garbage-
// collected on message deletion (orphanblobs.Restore's own premise, re
// #487), so a removed duplicate's content survives and a later `herold
// diag orphan-blobs restore` (or a dedicated undo replay) can recover it.
package dupmessages

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// Group is one Message-ID for which a principal holds more than one live
// message row, fully loaded and ordered oldest first (the order Merge
// keeps as the canonical survivor).
type Group struct {
	MessageID string
	Messages  []store.Message
}

// List returns every duplicate-Message-ID group for principalID, each
// message fully loaded via Metadata.GetMessage so Merge can act on it
// without a second round-trip. Read-only.
func List(ctx context.Context, st store.Store, principalID store.PrincipalID) ([]Group, error) {
	raw, err := st.Meta().ListDuplicateMessageIDs(ctx, principalID)
	if err != nil {
		return nil, fmt.Errorf("dupmessages: ListDuplicateMessageIDs: %w", err)
	}
	out := make([]Group, 0, len(raw))
	for _, g := range raw {
		msgs := make([]store.Message, 0, len(g.Messages))
		for _, id := range g.Messages {
			m, err := st.Meta().GetMessage(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("dupmessages: GetMessage(%d): %w", id, err)
			}
			msgs = append(msgs, m)
		}
		out = append(out, Group{MessageID: g.MessageID, Messages: msgs})
	}
	return out, nil
}

// RemovedMembership is one mailbox membership a removed duplicate held,
// captured before Merge expunges the row.
type RemovedMembership struct {
	MailboxID store.MailboxID
	Flags     store.MessageFlags
	Keywords  []string
}

// RemovedMessage is one row Merge removed, with enough state to write an
// undo-log entry: the removed row's own message id (no longer live after
// Merge returns), the blob it pointed at (still on disk, re #487), and
// every mailbox membership it held.
type RemovedMessage struct {
	MessageID store.MessageID
	BlobHash  string
	Mailboxes []RemovedMembership
}

// MergeResult reports what Merge did for one duplicate group.
type MergeResult struct {
	MessageID     string
	KeptMessageID store.MessageID
	Removed       []RemovedMessage
}

// Merge folds every duplicate in g (g.Messages[1:]) onto g.Messages[0] and
// removes them. g must hold at least 2 messages -- callers should skip
// groups List did not return, which are never smaller than 2.
func Merge(ctx context.Context, st store.Store, g Group) (MergeResult, error) {
	if len(g.Messages) < 2 {
		return MergeResult{}, fmt.Errorf("dupmessages: group %q has %d message(s), need at least 2", g.MessageID, len(g.Messages))
	}
	kept := g.Messages[0]
	res := MergeResult{MessageID: g.MessageID, KeptMessageID: kept.ID}

	keptMailboxes := make(map[store.MailboxID]bool, len(kept.Mailboxes))
	for _, mm := range kept.Mailboxes {
		keptMailboxes[mm.MailboxID] = true
	}

	for _, dup := range g.Messages[1:] {
		removed := RemovedMessage{MessageID: dup.ID, BlobHash: dup.Blob.Hash}
		for _, mm := range dup.Mailboxes {
			removed.Mailboxes = append(removed.Mailboxes, RemovedMembership{
				MailboxID: mm.MailboxID,
				Flags:     mm.Flags,
				Keywords:  mm.Keywords,
			})
			if !keptMailboxes[mm.MailboxID] {
				if _, _, err := st.Meta().AddMessageToMailbox(ctx, kept.ID, mm.MailboxID); err != nil {
					if !errors.Is(err, store.ErrConflict) {
						return MergeResult{}, fmt.Errorf("dupmessages: AddMessageToMailbox: %w", err)
					}
				}
				keptMailboxes[mm.MailboxID] = true
			}
			// Only $seen is unioned onto the survivor -- a duplicate's
			// other system flags (\Deleted, \Answered, \Flagged, \Draft)
			// describe that copy's own history, not an instruction to
			// apply to the merged row. Keywords (categories, provenance
			// labels) union in full.
			flagAdd := mm.Flags & store.MessageFlagSeen
			if flagAdd != 0 || len(mm.Keywords) > 0 {
				if _, err := st.Meta().UpdateMessageFlags(ctx, kept.ID, mm.MailboxID, flagAdd, 0, mm.Keywords, nil, 0); err != nil {
					return MergeResult{}, fmt.Errorf("dupmessages: UpdateMessageFlags: %w", err)
				}
			}
		}
		// Remove through the normal destroy path (REQ-STORE-33), exactly
		// like Email/set destroy: expunge from every mailbox the
		// duplicate belonged to so the change feed carries the removal.
		for _, mm := range dup.Mailboxes {
			if err := st.Meta().ExpungeMessages(ctx, mm.MailboxID, []store.MessageID{dup.ID}); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return MergeResult{}, fmt.Errorf("dupmessages: ExpungeMessages: %w", err)
				}
			}
		}
		res.Removed = append(res.Removed, removed)
	}
	return res, nil
}
