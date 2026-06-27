package store

// imapimport_removal.go implements account removal with the keep-or-purge
// choice (REQ-IMAP-IMP-102..105). It lives in the store package — operating
// on the Store interface — so both the JMAP IMAPImport/set destroy surface and
// the admin REST DELETE surface share one dedup-safe implementation without an
// import cycle through the worker package.

import (
	"context"
	"errors"
)

// RemoveIMAPImportResult reports what an account removal did, for audit/UI.
type RemoveIMAPImportResult struct {
	// MessagesDestroyed counts messages fully destroyed (purge mode only):
	// single-source mail whose only claim was this account.
	MessagesDestroyed int
	// LabelsDetached counts messages kept but with this account's
	// provenance-label membership removed (purge mode only): mail also
	// claimed by another import account or carrying a foreign membership.
	LabelsDetached int
}

// RemoveIMAPImportAccount removes the import account (id) owned by principalID,
// honouring the delete_imported_mail choice (REQ-IMAP-IMP-102..105).
//
//   - deleteImportedMail == false (KEEP, the default): drop the account row and
//     (by cascade) its credential, cursors, folder map, and message_state. The
//     imported mail and the provenance label stay in place as an ordinary user
//     label (REQ-IMAP-IMP-104).
//
//   - deleteImportedMail == true (PURGE): walk this account's message_state and,
//     for each message, destroy it only when this account's provenance label is
//     its sole claim — no other import account's message_state and no mailbox
//     membership the import did not itself create — otherwise just remove this
//     account's provenance-label membership and keep the message
//     (REQ-IMAP-IMP-103). The account row is then dropped. Purge is herold-side
//     only; it never issues upstream \Deleted/EXPUNGE (REQ-IMAP-IMP-102).
//
// The purge runs before the account row is deleted (so message_state is still
// available) and is re-entrant: every step is idempotent, and a crash leaves
// the account and its message_state in place to be completed by re-running the
// removal (REQ-IMAP-IMP-105).
func RemoveIMAPImportAccount(ctx context.Context, st Store, principalID PrincipalID, id string, deleteImportedMail bool) (RemoveIMAPImportResult, error) {
	var res RemoveIMAPImportResult

	acc, err := st.Meta().GetIMAPImportAccount(ctx, id)
	if err != nil {
		return res, err
	}
	if acc.PrincipalID != principalID {
		return res, ErrNotFound
	}

	if deleteImportedMail && acc.ProvenanceMailboxID != 0 {
		if err := purgeIMAPImportedMail(ctx, st, acc, &res); err != nil {
			return res, err
		}
	}

	if err := st.Meta().DeleteIMAPImportAccount(ctx, principalID, id); err != nil {
		return res, err
	}
	return res, nil
}

// purgeIMAPImportedMail destroys or detaches each message tracked by acc's
// message_state per the dedup-safe rule of REQ-IMAP-IMP-103.
func purgeIMAPImportedMail(ctx context.Context, st Store, acc IMAPImportAccount, res *RemoveIMAPImportResult) error {
	prov := acc.ProvenanceMailboxID

	states, err := st.Meta().ListIMAPImportMessageStatesByAccount(ctx, acc.ID)
	if err != nil {
		return err
	}

	// Collect, per message id, the set of herold mailboxes THIS account placed
	// it into (its folder-mapped targets), plus the distinct message ids in a
	// deterministic order.
	targets := map[MessageID]map[MailboxID]bool{}
	var order []MessageID
	for _, s := range states {
		if s.HeroldMessageID == 0 {
			continue
		}
		if targets[s.HeroldMessageID] == nil {
			targets[s.HeroldMessageID] = map[MailboxID]bool{}
			order = append(order, s.HeroldMessageID)
		}
		targets[s.HeroldMessageID][s.HeroldMailboxID] = true
	}

	for _, msgID := range order {
		msg, gerr := st.Meta().GetMessage(ctx, msgID)
		if errors.Is(gerr, ErrNotFound) {
			// Already destroyed by an earlier, interrupted run.
			continue
		}
		if gerr != nil {
			return gerr
		}

		// Does another import account also claim this message?
		allStates, lerr := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, msgID)
		if lerr != nil {
			return lerr
		}
		otherClaim := false
		for _, s := range allStates {
			if s.AccountID != acc.ID {
				otherClaim = true
				break
			}
		}

		// soleClaim: no other import account claims it AND every remaining
		// membership (after dropping this account's provenance label) is a
		// mailbox this account itself placed the message into. Any other
		// membership — a foreign label, another account's provenance label, a
		// manual move, or native delivery into a non-target mailbox — keeps
		// the message.
		aTargets := targets[msgID]
		soleClaim := !otherClaim
		for _, mm := range msg.Mailboxes {
			if mm.MailboxID == prov {
				continue
			}
			if !aTargets[mm.MailboxID] {
				soleClaim = false
				break
			}
		}

		if soleClaim {
			// Destroy: remove from every mailbox. The store cleans up the
			// messages row and decrements the blob refcount when the last
			// membership goes (RemoveMessageFromMailbox contract).
			for _, mm := range msg.Mailboxes {
				if rerr := st.Meta().RemoveMessageFromMailbox(ctx, msgID, mm.MailboxID); rerr != nil && !errors.Is(rerr, ErrNotFound) {
					return rerr
				}
			}
			res.MessagesDestroyed++
			continue
		}

		// Keep the message; detach only this account's provenance label.
		if rerr := st.Meta().RemoveMessageFromMailbox(ctx, msgID, prov); rerr != nil && !errors.Is(rerr, ErrNotFound) {
			return rerr
		}
		res.LabelsDetached++
	}
	return nil
}
