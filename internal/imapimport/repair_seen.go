package imapimport

// repair_seen.go implements the row-level logic behind `herold imapimport
// repair-seen` (re #435): a one-off, store-backed repair for an explicitly
// named message whose read state was cleared by the multi-copy reconcile bug
// (a Message-ID dedup hit folds two upstream copies onto one herold message,
// and before the fix in this issue, write-back's single-row lookup pushed a
// read to only one of the copies -- see writeback.go's reconcileMessageFlags
// doc comment for the full mechanism).
//
// The repair forces $seen on every current membership of the named message
// (mirroring the #316/#376 "every membership, not just the triggering one"
// rule) and, for every imapimport_message_state row recorded for it, sets
// LastSyncedFlags to include \Seen so the fixed reconcile (this issue) does
// not read the pre-repair, unseen baseline as a fresh conflict against the
// still-unseen upstream copies on its first tick after the repair -- it
// records the message as already caught up, then pushes \Seen upstream to
// every row the same as any other herold-side read (REQ-IMAP-IMP-42, re
// #435). It lives in this package (rather than internal/admin, which only
// wires the CLI surface) so it can share the store access this package
// already has, matching restore_archive.go's split.

import (
	"context"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// RepairSeenAction is the outcome RepairSeen recorded for one message id.
type RepairSeenAction string

const (
	// RepairSeenActionMarked means the message was (or, in --dry-run, would
	// be) marked $seen on every current membership.
	RepairSeenActionMarked RepairSeenAction = "marked-seen"
	// RepairSeenActionAlreadySeen means every current membership already
	// carried $seen; nothing to do.
	RepairSeenActionAlreadySeen RepairSeenAction = "already-seen"
	// RepairSeenActionSkipped means an error, or an ineligible message,
	// prevented the repair; see the result's Error field.
	RepairSeenActionSkipped RepairSeenAction = "skipped"
)

// RepairSeenResult reports what RepairSeen did for one message id.
type RepairSeenResult struct {
	MessageID store.MessageID
	Action    RepairSeenAction
	Error     string
}

// RepairSeen repairs one message: see the package doc comment above for the
// full contract. dryRun reports the intended action without writing
// anything. msg must belong to pid and carry at least one
// imapimport_message_state row (this repair is scoped to imported mail,
// matching restore-archive's cross-principal guard and imapimport scope);
// otherwise the message is reported as an error and left untouched.
func RepairSeen(ctx context.Context, st store.Store, pid store.PrincipalID, msgID store.MessageID, dryRun bool) RepairSeenResult {
	res := RepairSeenResult{MessageID: msgID}

	msg, err := st.Meta().GetMessage(ctx, msgID)
	if err != nil {
		res.Action = RepairSeenActionSkipped
		res.Error = fmt.Sprintf("GetMessage: %v", err)
		return res
	}
	if msg.PrincipalID != pid {
		res.Action = RepairSeenActionSkipped
		res.Error = "message does not belong to the given principal"
		return res
	}

	states, err := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, msgID)
	if err != nil {
		res.Action = RepairSeenActionSkipped
		res.Error = fmt.Sprintf("ListIMAPImportMessageStatesByMessage: %v", err)
		return res
	}
	if len(states) == 0 {
		res.Action = RepairSeenActionSkipped
		res.Error = "message carries no imapimport_message_state row (not an imported message)"
		return res
	}

	allSeen := true
	for _, mm := range msg.Mailboxes {
		if mm.Flags&store.MessageFlagSeen == 0 {
			allSeen = false
			break
		}
	}
	if allSeen {
		res.Action = RepairSeenActionAlreadySeen
		return res
	}

	if dryRun {
		res.Action = RepairSeenActionMarked
		return res
	}

	for _, mm := range msg.Mailboxes {
		if mm.Flags&store.MessageFlagSeen != 0 {
			continue
		}
		if _, err := st.Meta().UpdateMessageFlags(ctx, msgID, mm.MailboxID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
			res.Action = RepairSeenActionSkipped
			res.Error = fmt.Sprintf("UpdateMessageFlags (mailbox %d): %v", mm.MailboxID, err)
			return res
		}
	}

	for _, s := range states {
		if s.LastSyncedFlags.HasSeen() {
			continue
		}
		s.LastSyncedFlags |= store.IMAPImportFlagSeen
		if err := st.Meta().UpsertIMAPImportMessageState(ctx, s); err != nil {
			// The message-side fix is already applied; a state row that
			// fails to update here just means the fixed reconcile's next
			// write-back tick sees this row as a fresh local-vs-baseline
			// difference and pushes \Seen to it as normal -- not silent
			// data loss, so this does not fail the repair.
			res.Error = fmt.Sprintf("UpsertIMAPImportMessageState (uid %d): %v", s.UpstreamUID, err)
		}
	}

	res.Action = RepairSeenActionMarked
	return res
}
