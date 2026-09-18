package imapimport

// restore_archive.go implements the row-level logic behind `herold imapimport
// restore-archive` (re #376, second round): a one-off, store-backed repair
// for explicitly named messages that the pre-fix INBOX-resurfacing bug left
// stranded in INBOX. It lives in this package (rather than internal/admin,
// which only wires the CLI surface) so it can share MessageIsPrincipalSent /
// ThreadHasArchiveMember -- the same eligibility tests the live importer
// applies at ingest time -- without an accountWorker, and so its
// down-sync-durability behaviour can be exercised against this package's
// in-process IMAP test harness.
//
// Eligibility (re #376 second-round follow-up): the repair refuses to move a
// message that is neither principal-sent (a \Sent-role membership, or a From
// naming one of the principal's own identities) nor an imapimport dedup hit
// whose thread already has an Archive member -- the two shapes #376
// describes. A refusal is reported per message, not silently skipped;
// --force overrides it for a message the operator has independently
// confirmed.
//
// For each eligible (or forced) named message: if it is currently a member
// of INBOX, the repair ensures an Archive membership (creating the
// principal's Archive mailbox if it does not yet exist), forces $seen on
// that membership (an archived item carries no unread meaning), removes the
// INBOX membership, and updates every imapimport_message_state row that
// recorded the stale INBOX placement to point at the new Archive membership
// with $seen recorded in LastSyncedFlags -- mirroring what
// placeExistingMessage's errINBOXSuppressedByArchive path records for a live
// dedup hit, so a subsequent down-sync of the still-unseen upstream copy
// does not read the old, unseen LastSyncedFlags baseline as a herold-side
// regression and clear $seen or resurface INBOX. A message with no INBOX
// membership is left untouched and reported as already-archived (idempotent:
// safe to re-run).

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// RestoreArchiveAction is the outcome restore-archive recorded for one
// message id.
type RestoreArchiveAction string

const (
	// RestoreArchiveActionMoved means the message was (or, in --dry-run,
	// would be) moved from INBOX to Archive and marked $seen.
	RestoreArchiveActionMoved RestoreArchiveAction = "moved-to-archive"
	// RestoreArchiveActionAlreadyArchived means the message carries no
	// INBOX membership; nothing to do.
	RestoreArchiveActionAlreadyArchived RestoreArchiveAction = "already-archived"
	// RestoreArchiveActionRefused means the message is currently in INBOX
	// but matches neither #376 eligibility shape, and --force was not
	// given; the message was left untouched.
	RestoreArchiveActionRefused RestoreArchiveAction = "refused"
	// RestoreArchiveActionSkipped means an error prevented the repair; see
	// the result's Error field.
	RestoreArchiveActionSkipped RestoreArchiveAction = "skipped"
)

// RestoreArchiveResult reports what RestoreArchivePlacement did for one
// message id.
type RestoreArchiveResult struct {
	MessageID store.MessageID
	Action    RestoreArchiveAction
	// Reason explains the eligibility verdict for a message that carried
	// an INBOX membership (RestoreArchiveActionMoved or
	// RestoreArchiveActionRefused); empty for AlreadyArchived / Skipped.
	Reason string
	Error  string
}

// eligibleForArchiveRestore reports whether msg matches one of the #376
// eligibility shapes: principal-sent, or an imapimport dedup hit (it carries
// at least one imapimport_message_state row) whose thread has an Archive
// member. Returns the human-readable verdict either way.
func eligibleForArchiveRestore(ctx context.Context, st store.Store, pid store.PrincipalID, msg store.Message) (bool, string) {
	if MessageIsPrincipalSent(ctx, st, pid, msg) {
		return true, "principal-sent (a Sent-role membership, or From names a principal identity)"
	}
	if states, err := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, msg.ID); err == nil && len(states) > 0 {
		if _, ok := ThreadHasArchiveMember(ctx, st, pid, msg); ok {
			return true, "imapimport dedup hit whose thread has an archived member"
		}
	}
	return false, "neither principal-sent nor an imapimport dedup hit whose thread has an archived member"
}

// RestoreArchivePlacement repairs one message: see the package doc comment
// above for the full contract. dryRun reports the intended action without
// writing anything. force moves an ineligible message anyway (its Reason
// records that it was forced). msg must belong to pid; a message belonging
// to a different principal is reported as an error and left untouched
// (repair-orphans' cross-principal guard).
func RestoreArchivePlacement(ctx context.Context, st store.Store, pid store.PrincipalID, msgID store.MessageID, dryRun, force bool) RestoreArchiveResult {
	res := RestoreArchiveResult{MessageID: msgID}

	msg, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		res.Action = RestoreArchiveActionSkipped
		res.Error = fmt.Sprintf("GetMessage: %v", err)
		return res
	}
	if msg.PrincipalID != pid {
		res.Action = RestoreArchiveActionSkipped
		res.Error = "message does not belong to the given principal"
		return res
	}

	mbs, err := st.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		res.Action = RestoreArchiveActionSkipped
		res.Error = fmt.Sprintf("ListMailboxes: %v", err)
		return res
	}
	var inboxMB, archiveMB store.Mailbox
	var haveInbox, haveArchive bool
	for _, mb := range mbs {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			inboxMB, haveInbox = mb, true
		}
		if mb.Attributes&store.MailboxAttrArchive != 0 {
			archiveMB, haveArchive = mb, true
		}
	}

	var inboxMembership *store.MessageMailbox
	if haveInbox {
		for i := range msg.Mailboxes {
			if msg.Mailboxes[i].MailboxID == inboxMB.ID {
				inboxMembership = &msg.Mailboxes[i]
				break
			}
		}
	}
	if inboxMembership == nil {
		// No INBOX membership: nothing to repair. Idempotent re-run.
		res.Action = RestoreArchiveActionAlreadyArchived
		return res
	}

	eligible, reason := eligibleForArchiveRestore(ctx, st, pid, msg)
	if !eligible {
		if !force {
			res.Action = RestoreArchiveActionRefused
			res.Reason = reason
			return res
		}
		reason = "forced despite: " + reason
	}
	res.Reason = reason

	if dryRun {
		res.Action = RestoreArchiveActionMoved
		return res
	}

	if !haveArchive {
		archiveMB, err = st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid,
			Name:        "Archive",
			Attributes:  store.MailboxAttrArchive,
		})
		if err != nil {
			res.Action = RestoreArchiveActionSkipped
			res.Error = fmt.Sprintf("InsertMailbox (Archive): %v", err)
			return res
		}
	}

	alreadyArchiveMember := false
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID == archiveMB.ID {
			alreadyArchiveMember = true
			break
		}
	}
	if !alreadyArchiveMember {
		if _, _, err := st.Meta().AddMessageToMailbox(ctx, msgID, archiveMB.ID); err != nil && !errors.Is(err, store.ErrConflict) {
			res.Action = RestoreArchiveActionSkipped
			res.Error = fmt.Sprintf("AddMessageToMailbox (Archive): %v", err)
			return res
		}
	}
	if _, err := st.Meta().UpdateMessageFlags(ctx, msgID, archiveMB.ID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		res.Action = RestoreArchiveActionSkipped
		res.Error = fmt.Sprintf("UpdateMessageFlags (force seen on Archive): %v", err)
		return res
	}
	if err := st.Meta().RemoveMessageFromMailbox(ctx, msgID, inboxMB.ID); err != nil {
		res.Action = RestoreArchiveActionSkipped
		res.Error = fmt.Sprintf("RemoveMessageFromMailbox (INBOX): %v", err)
		return res
	}

	updateArchiveRestoreMessageState(ctx, st, msgID, inboxMB.ID, archiveMB.ID)

	res.Action = RestoreArchiveActionMoved
	return res
}

// updateArchiveRestoreMessageState repairs every imapimport_message_state
// row that recorded the stale INBOX placement RestoreArchivePlacement just
// removed: HeroldMailboxID moves to archiveMBID (MappedMailboxID is left
// pointing at the folder's nominal INBOX target -- inboxMBID, recorded when
// not already set -- the same divergence errINBOXSuppressedByArchive
// records for a live dedup hit, re #319), and LastSyncedFlags gains \Seen so
// the next down-sync's three-way conflict resolution sees the forced $seen
// as the last-known-synced baseline rather than a fresh herold-side change
// to reconcile against the still-unseen upstream copy. Rows addressing a
// different placement (already Archive, a different account's copy) are
// left untouched. Best-effort: a store error is logged by the caller's
// normal UpsertIMAPImportMessageState error path and does not fail the
// repair -- the mailbox membership, which is authoritative for the
// resurfacing symptom itself, is already fixed.
func updateArchiveRestoreMessageState(ctx context.Context, st store.Store, msgID store.MessageID, inboxMBID, archiveMBID store.MailboxID) {
	states, err := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, msgID)
	if err != nil {
		return
	}
	for _, ms := range states {
		if ms.HeroldMailboxID != inboxMBID {
			continue
		}
		ms.HeroldMailboxID = archiveMBID
		if ms.MappedMailboxID == 0 {
			ms.MappedMailboxID = inboxMBID
		}
		ms.LastSyncedFlags |= store.IMAPImportFlagSeen
		_ = st.Meta().UpsertIMAPImportMessageState(ctx, ms)
	}
}
