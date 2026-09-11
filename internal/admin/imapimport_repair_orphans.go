package admin

// imapimport_repair_orphans.go implements the row-level logic behind
// `herold imapimport repair-orphans` (issue #319): a one-off, store-backed
// maintenance command (opens the store from --system-config, in the family
// of `spam apply-verdicts` / `diag reparse-envelopes`; no admin server
// needed) that repairs the orphan shape #319 describes -- a message
// carrying only an IMAP-import account's provenance label
// (REQ-IMAP-IMP-100) and no imapimport_message_state row for that
// account, left behind by the pre-fix removeMessageStateMembership bug
// when the upstream copy that produced its only real membership was
// flagged \Deleted.
//
// The fix in internal/imapimport/sync.go stops new orphans from being
// created; this command repairs the ones a pre-fix worker already wrote.
// It cannot restore the lost upstream folder/UID provenance (that history
// is gone), only the missing folder membership: a message with a recorded
// "spam" verdict (llm_classifications) is filed into the principal's Junk
// mailbox, matching the live import path's own routing (REQ-FILT-02);
// every other orphan is filed into INBOX. The provenance label and
// message_state history are not recreated.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// IMAPImportRepairOrphansSummary totals one repair-orphans run.
type IMAPImportRepairOrphansSummary struct {
	// AccountsScanned is the number of the principal's IMAP-import
	// accounts that carry a provenance label (accounts never enabled
	// have none and are skipped).
	AccountsScanned int `json:"accounts_scanned"`
	// LabelMembers is the total number of messages found as members of
	// any scanned account's provenance label.
	LabelMembers int `json:"label_members"`
	// Orphans is the subset of LabelMembers that carry no other mailbox
	// membership and no message_state row for the owning account -- the
	// #319 shape this command repairs.
	Orphans int `json:"orphans"`
	// FiledJunk is the number of orphans filed into Junk (a recorded
	// "spam" verdict).
	FiledJunk int `json:"filed_junk"`
	// FiledInbox is the number of orphans filed into INBOX (no recorded
	// "spam" verdict).
	FiledInbox int `json:"filed_inbox"`
	// Errors is the number of orphans skipped after a store error.
	Errors int `json:"errors"`
}

// repairIMAPImportOrphans scans every IMAP-import account belonging to pid
// for label-only orphans and files each one into Junk or INBOX per its
// recorded spam verdict. dryRun reports the summary without writing
// anything.
func repairIMAPImportOrphans(ctx context.Context, st store.Store, pid store.PrincipalID, dryRun bool) (IMAPImportRepairOrphansSummary, error) {
	var sum IMAPImportRepairOrphansSummary

	accounts, err := st.Meta().ListIMAPImportAccountsByPrincipal(ctx, pid)
	if err != nil {
		return sum, fmt.Errorf("list imap import accounts: %w", err)
	}

	for _, acc := range accounts {
		if acc.ProvenanceMailboxID == 0 {
			// Never enabled (no provenance label created yet): nothing to
			// scan.
			continue
		}
		sum.AccountsScanned++

		var after store.UID
		for {
			msgs, lerr := st.Meta().ListMessages(ctx, acc.ProvenanceMailboxID, store.MessageFilter{AfterUID: after, Limit: 1000})
			if lerr != nil {
				return sum, fmt.Errorf("list label messages for account %s: %w", acc.ID, lerr)
			}
			if len(msgs) == 0 {
				break
			}
			for _, lm := range msgs {
				after = lm.UID
				sum.LabelMembers++

				orphan, oerr := repairOneIMAPImportOrphan(ctx, st, pid, acc, lm.ID, dryRun)
				if oerr != nil {
					sum.Errors++
					continue
				}
				if !orphan.isOrphan {
					continue
				}
				sum.Orphans++
				if orphan.filedJunk {
					sum.FiledJunk++
				} else {
					sum.FiledInbox++
				}
			}
			if len(msgs) < 1000 {
				break
			}
		}
	}
	return sum, nil
}

// imapImportOrphanResult reports what repairOneIMAPImportOrphan found/did
// for one label member.
type imapImportOrphanResult struct {
	isOrphan  bool
	filedJunk bool
}

// repairOneIMAPImportOrphan inspects one message that is a member of
// acc's provenance label and, if it is an orphan (no other membership, no
// message_state row for acc), files it into Junk (recorded "spam"
// verdict) or INBOX (dry-run: reports the decision without writing
// anything).
func repairOneIMAPImportOrphan(ctx context.Context, st store.Store, pid store.PrincipalID, acc store.IMAPImportAccount, msgID store.MessageID, dryRun bool) (imapImportOrphanResult, error) {
	full, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Raced away (e.g. concurrently destroyed) -- not an error.
			return imapImportOrphanResult{}, nil
		}
		return imapImportOrphanResult{}, err
	}
	if len(full.Mailboxes) != 1 || full.Mailboxes[0].MailboxID != acc.ProvenanceMailboxID {
		// Has a real membership already (or belongs to more than this
		// label) -- not an orphan.
		return imapImportOrphanResult{}, nil
	}
	if _, found, serr := st.Meta().GetIMAPImportMessageStateByMessage(ctx, acc.ID, msgID); serr != nil {
		return imapImportOrphanResult{}, serr
	} else if found {
		// Tracked: the #319 fix keeps this addressable even when its real
		// placement diverges from the label alone -- not an orphan.
		return imapImportOrphanResult{}, nil
	}

	filedJunk := false
	if rec, cerr := st.Meta().GetLLMClassification(ctx, msgID); cerr == nil && rec.SpamVerdict != nil && strings.EqualFold(*rec.SpamVerdict, "spam") {
		filedJunk = true
	}

	if dryRun {
		return imapImportOrphanResult{isOrphan: true, filedJunk: filedJunk}, nil
	}

	targetName := "INBOX"
	targetAttr := store.MailboxAttrInbox
	if filedJunk {
		targetName = "Junk"
		targetAttr = store.MailboxAttrJunk
	}
	mb, merr := resolveOrCreateSpecialMailbox(ctx, st, pid, targetAttr, targetName)
	if merr != nil {
		return imapImportOrphanResult{}, merr
	}
	if _, _, aerr := st.Meta().AddMessageToMailbox(ctx, msgID, mb.ID); aerr != nil && !errors.Is(aerr, store.ErrConflict) {
		return imapImportOrphanResult{}, aerr
	}
	return imapImportOrphanResult{isOrphan: true, filedJunk: filedJunk}, nil
}

// resolveOrCreateSpecialMailbox returns pid's mailbox carrying attr
// (INBOX or Junk), falling back to a case-insensitive name match, and
// creating one named name with attr set when neither exists. Mirrors
// imapimport.accountWorker.ensureMailbox (internal/imapimport/sync.go).
func resolveOrCreateSpecialMailbox(ctx context.Context, st store.Store, pid store.PrincipalID, attr store.MailboxAttributes, name string) (store.Mailbox, error) {
	mbs, err := st.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mb := range mbs {
		if mb.Attributes&attr != 0 {
			return mb, nil
		}
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, name) {
			return mb, nil
		}
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        name,
		Attributes:  attr,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			mbs2, _ := st.Meta().ListMailboxes(ctx, pid)
			for _, mb2 := range mbs2 {
				if strings.EqualFold(mb2.Name, name) {
					return mb2, nil
				}
			}
		}
		return store.Mailbox{}, err
	}
	return mb, nil
}
