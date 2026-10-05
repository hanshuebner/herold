// Package mergemailbox implements the repair path behind `herold diag
// merge-mailbox` (re #509): when two mailboxes carrying the same
// SPECIAL-USE role exist for one account -- the state the IMAP-import
// folder-mapping defect produced (a local "Junk" mailbox alongside an
// upstream-named "Spam" mailbox, both carrying the \Junk attribute) --
// this merges the duplicate into the account's surviving mailbox for that
// role.
//
// Analyze is a dry run: it validates the merge is possible and reports
// what Merge would do, without writing anything.
//
// Merge moves every membership the source mailbox holds into the target
// through the store's normal move path (MoveMessage: a fresh UID in the
// target, a ModSeq bump, a change-feed row, keywords/flags/received_to
// preserved) -- except a membership whose message already lives in the
// target too, which is simply dropped from the source rather than moved
// twice. It then rewrites every other table that references the source
// mailbox id (store.RepointMailboxRefs) and deletes the now-empty source
// mailbox.
//
// The merge refuses rather than guesses when the source mailbox has child
// mailboxes (mailboxes.parent_id has no rewrite path here -- a child would
// be orphaned) or carries ACL grants (a grant is keyed by resource_id and
// provenance; folding it into the target's grants could silently widen or
// narrow another principal's access).
package mergemailbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// ErrSameMailbox is returned when fromID and toID name the same mailbox.
var ErrSameMailbox = errors.New("mergemailbox: from and into are the same mailbox")

// ErrHasChildren is returned when the source mailbox has child mailboxes;
// RepointMailboxRefs has no rewrite path for mailboxes.parent_id, so a
// child would be orphaned by deleting the source.
var ErrHasChildren = errors.New("mergemailbox: source mailbox has child mailboxes")

// ErrHasACLGrants is returned when the source mailbox carries ACL grants;
// folding them into the target could silently widen or narrow another
// principal's access, so the merge refuses instead of guessing.
var ErrHasACLGrants = errors.New("mergemailbox: source mailbox has ACL grants")

// ErrWrongPrincipal is returned when the source or target mailbox is not
// owned by the principal the caller named.
var ErrWrongPrincipal = errors.New("mergemailbox: mailbox is not owned by the given principal")

// Plan reports what Merge would do (Analyze) or did (Merge), for the
// command's dry-run and normal-run output.
type Plan struct {
	From, To store.Mailbox
	// Moved lists the messages whose source membership was moved into the
	// target via MoveMessage.
	Moved []store.MessageID
	// Dropped lists the messages whose source membership was dropped
	// because the message already carried a target membership.
	Dropped []store.MessageID
	// Refs is the row counts across every other table referencing the
	// source mailbox id (store.CountMailboxRefs), rewritten by Merge via
	// store.RepointMailboxRefs.
	Refs store.MailboxRefCounts
}

// validate loads and checks the source and target mailboxes, refusing the
// merge per the package doc's rules. Returns the loaded mailboxes on
// success.
func validate(ctx context.Context, st store.Store, principalID store.PrincipalID, fromID, toID store.MailboxID) (from, to store.Mailbox, err error) {
	if fromID == toID {
		return store.Mailbox{}, store.Mailbox{}, ErrSameMailbox
	}
	from, err = st.Meta().GetMailboxByID(ctx, fromID)
	if err != nil {
		return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("mergemailbox: GetMailboxByID(from=%d): %w", fromID, err)
	}
	to, err = st.Meta().GetMailboxByID(ctx, toID)
	if err != nil {
		return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("mergemailbox: GetMailboxByID(into=%d): %w", toID, err)
	}
	if from.PrincipalID != principalID || to.PrincipalID != principalID {
		return store.Mailbox{}, store.Mailbox{}, ErrWrongPrincipal
	}

	mbs, err := st.Meta().ListMailboxes(ctx, principalID)
	if err != nil {
		return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("mergemailbox: ListMailboxes: %w", err)
	}
	for _, mb := range mbs {
		if mb.ParentID == fromID {
			return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("%w: %q (id %d)", ErrHasChildren, mb.Name, mb.ID)
		}
	}

	acl, err := st.Meta().GetMailboxACL(ctx, fromID)
	if err != nil {
		return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("mergemailbox: GetMailboxACL: %w", err)
	}
	if len(acl) > 0 {
		return store.Mailbox{}, store.Mailbox{}, fmt.Errorf("%w: %d grant(s)", ErrHasACLGrants, len(acl))
	}

	return from, to, nil
}

// planMembers enumerates fromID's message memberships, in ascending UID
// order, and classifies each as "would move" (the message has no target
// membership yet) or "would drop" (the message already lives in toID).
// Read-only: used by both Analyze and Merge so the two can never
// disagree about which messages fall into which bucket.
func planMembers(ctx context.Context, st store.Store, fromID, toID store.MailboxID) (moved, dropped []store.MessageID, err error) {
	var afterUID store.UID
	for {
		batch, err := st.Meta().ListMessages(ctx, fromID, store.MessageFilter{AfterUID: afterUID, Limit: 500})
		if err != nil {
			return nil, nil, fmt.Errorf("mergemailbox: ListMessages: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, m := range batch {
			if m.UID > afterUID {
				afterUID = m.UID
			}
			full, err := st.Meta().GetMessage(ctx, m.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("mergemailbox: GetMessage(%d): %w", m.ID, err)
			}
			inTarget := false
			for _, mm := range full.Mailboxes {
				if mm.MailboxID == toID {
					inTarget = true
					break
				}
			}
			if inTarget {
				dropped = append(dropped, m.ID)
			} else {
				moved = append(moved, m.ID)
			}
		}
		if len(batch) < 500 {
			break
		}
	}
	return moved, dropped, nil
}

// Analyze validates the merge and reports what Merge would do, without
// writing anything (`herold diag merge-mailbox --dry-run`).
func Analyze(ctx context.Context, st store.Store, principalID store.PrincipalID, fromID, toID store.MailboxID) (Plan, error) {
	from, to, err := validate(ctx, st, principalID, fromID, toID)
	if err != nil {
		return Plan{}, err
	}
	moved, dropped, err := planMembers(ctx, st, fromID, toID)
	if err != nil {
		return Plan{}, err
	}
	refs, err := st.Meta().CountMailboxRefs(ctx, fromID)
	if err != nil {
		return Plan{}, fmt.Errorf("mergemailbox: CountMailboxRefs: %w", err)
	}
	return Plan{From: from, To: to, Moved: moved, Dropped: dropped, Refs: refs}, nil
}

// Merge performs the mailbox merge described in the package doc: moves or
// drops every membership of fromID, rewrites every other table
// referencing fromID to reference toID, and deletes the now-empty fromID
// mailbox. Returns the same Plan shape Analyze would have reported.
func Merge(ctx context.Context, st store.Store, principalID store.PrincipalID, fromID, toID store.MailboxID) (Plan, error) {
	from, to, err := validate(ctx, st, principalID, fromID, toID)
	if err != nil {
		return Plan{}, err
	}
	moved, dropped, err := planMembers(ctx, st, fromID, toID)
	if err != nil {
		return Plan{}, err
	}
	refs, err := st.Meta().CountMailboxRefs(ctx, fromID)
	if err != nil {
		return Plan{}, fmt.Errorf("mergemailbox: CountMailboxRefs: %w", err)
	}

	for _, msgID := range dropped {
		if err := st.Meta().RemoveMessageFromMailbox(ctx, msgID, fromID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Plan{}, fmt.Errorf("mergemailbox: RemoveMessageFromMailbox(%d): %w", msgID, err)
		}
	}
	for _, msgID := range moved {
		if err := st.Meta().MoveMessage(ctx, msgID, fromID, toID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Plan{}, fmt.Errorf("mergemailbox: MoveMessage(%d): %w", msgID, err)
		}
	}

	if err := st.Meta().RepointMailboxRefs(ctx, fromID, toID); err != nil {
		return Plan{}, fmt.Errorf("mergemailbox: RepointMailboxRefs: %w", err)
	}
	if err := st.Meta().DeleteMailbox(ctx, fromID); err != nil {
		return Plan{}, fmt.Errorf("mergemailbox: DeleteMailbox: %w", err)
	}

	return Plan{From: from, To: to, Moved: moved, Dropped: dropped, Refs: refs}, nil
}
