package admin

// imapimport_repair_archive.go implements the row-level logic behind
// `herold imapimport restore-archive` (re #376, second round, comment
// 5083's repair item): a one-off, store-backed maintenance command (opens
// the store from --system-config, in the family of `imapimport
// repair-orphans` / `spam apply-verdicts`; no admin server needed) that
// reverses the "principal-sent or already-archived message resurfaced in
// Inbox" symptom for explicitly named messages.
//
// The imapimport fix in internal/imapimport/sync.go and writeback.go stops
// a dedup hit / down-sync reconcile from creating or re-exposing an unwanted
// INBOX membership going forward; it cannot retroactively re-derive a
// thread's prior Archive placement once every trace of it (the message's
// own Archive membership, any sibling thread member still carrying one) has
// already been erased by the pre-fix bug -- that history is gone, so the
// maintainer names the affected message ids explicitly rather than this
// command guessing at them from a heuristic scan.
//
// For each named message: if it is currently a member of INBOX, the command
// ensures an Archive membership (creating the principal's Archive mailbox
// if it does not yet exist), forces $seen on that membership (an archived
// item carries no unread meaning), and removes the INBOX membership. A
// message with no INBOX membership is left untouched and reported as
// already-archived (idempotent: safe to re-run).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// IMAPImportRestoreArchiveResult reports what restoreIMAPImportArchive did
// for one message id.
type IMAPImportRestoreArchiveResult struct {
	MessageID store.MessageID `json:"message_id"`
	// Action is one of "moved-to-archive", "already-archived" (no INBOX
	// membership found, nothing to do), or "skipped" (an error prevented
	// the repair; see Error).
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// restoreIMAPImportArchive moves each message in msgIDs from INBOX to
// Archive (creating the Archive mailbox if needed) and forces $seen on the
// resulting membership. dryRun reports the intended action per message
// without writing anything. Every message must belong to pid; a message
// belonging to a different principal is reported as an error and left
// untouched (repair-orphans' cross-principal guard).
func restoreIMAPImportArchive(ctx context.Context, st store.Store, pid store.PrincipalID, msgIDs []store.MessageID, dryRun bool) ([]IMAPImportRestoreArchiveResult, error) {
	results := make([]IMAPImportRestoreArchiveResult, 0, len(msgIDs))

	for _, msgID := range msgIDs {
		res := IMAPImportRestoreArchiveResult{MessageID: msgID}

		msg, err := st.Meta().GetMessage(ctx, msgID)
		if err != nil {
			res.Action = "skipped"
			res.Error = fmt.Sprintf("GetMessage: %v", err)
			results = append(results, res)
			continue
		}
		if msg.PrincipalID != pid {
			res.Action = "skipped"
			res.Error = "message does not belong to the given principal"
			results = append(results, res)
			continue
		}

		mbs, err := st.Meta().ListMailboxes(ctx, pid)
		if err != nil {
			res.Action = "skipped"
			res.Error = fmt.Sprintf("ListMailboxes: %v", err)
			results = append(results, res)
			continue
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
			res.Action = "already-archived"
			results = append(results, res)
			continue
		}

		if dryRun {
			res.Action = "moved-to-archive"
			results = append(results, res)
			continue
		}

		if !haveArchive {
			archiveMB, err = st.Meta().InsertMailbox(ctx, store.Mailbox{
				PrincipalID: pid,
				Name:        "Archive",
				Attributes:  store.MailboxAttrArchive,
			})
			if err != nil {
				res.Action = "skipped"
				res.Error = fmt.Sprintf("InsertMailbox (Archive): %v", err)
				results = append(results, res)
				continue
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
				res.Action = "skipped"
				res.Error = fmt.Sprintf("AddMessageToMailbox (Archive): %v", err)
				results = append(results, res)
				continue
			}
		}
		if _, err := st.Meta().UpdateMessageFlags(ctx, msgID, archiveMB.ID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
			res.Action = "skipped"
			res.Error = fmt.Sprintf("UpdateMessageFlags (force seen on Archive): %v", err)
			results = append(results, res)
			continue
		}
		if err := st.Meta().RemoveMessageFromMailbox(ctx, msgID, inboxMB.ID); err != nil {
			res.Action = "skipped"
			res.Error = fmt.Sprintf("RemoveMessageFromMailbox (INBOX): %v", err)
			results = append(results, res)
			continue
		}

		res.Action = "moved-to-archive"
		results = append(results, res)
	}

	return results, nil
}

// formatIMAPImportRestoreArchiveResults renders the per-message results as
// plain lines, one per message, for the CLI's human-readable output.
func formatIMAPImportRestoreArchiveResults(results []IMAPImportRestoreArchiveResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(&b, "message %d: %s (%s)\n", r.MessageID, r.Action, r.Error)
		} else {
			fmt.Fprintf(&b, "message %d: %s\n", r.MessageID, r.Action)
		}
	}
	return b.String()
}
