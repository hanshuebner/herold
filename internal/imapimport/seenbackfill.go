package imapimport

// seenbackfill.go implements the once-per-worker-lifetime maintenance pass
// that corrects existing message_mailboxes rows minted before the two
// $seen fixes of re #316:
//
//   - insertNewMessage forcing $seen on a fresh insert into a \Sent-role
//     mailbox regardless of the upstream \Seen flag.
//   - addProvenanceLabel mirroring $seen onto the provenance-label
//     membership when the message is already $seen via another membership.
//
// Both fixes only reach messages ingested (or provenance-labeled) AFTER
// they landed; this pass corrects the ones already on disk, so a message
// imported before the fix does not keep rendering its thread row bold with
// the principal as sender.
//
// Bounded to one account's own message_state rows (REQ-IMAP-IMP-34) rather
// than a full-store message scan: every message this account ever mirrored
// has at least one message_state row, so walking them (deduplicated by
// herold message id) reaches every candidate message without touching rows
// belonging to a different account or a different ingest path. Runs once
// per accountWorker process lifetime, right after ensureProvenanceMailbox
// and before the first sync pass of a session (worker.go) -- the same
// "cheap, idempotent, safe to repeat every session" posture
// ensureProvenanceMailbox itself uses. Best-effort: a single row's failure
// is logged and does not abort the pass or the session.
//
// A "migrated" (post-cutover, terminal) account runs no worker and so never
// reaches this pass; its historical rows are backfilled by nature of having
// gone through this pass in an earlier "enabled"/"migrating" session before
// the cutover (REQ-IMAP-IMP-90..95), or -- for a `migrated` account that
// never ran the fixed binary while active -- remain uncorrected until it is
// re-opened to `enabled` (REQ-IMAP-IMP-93/95, an operator action).

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/store"
)

// runSeenBackfill corrects existing rows for this account, once per worker
// process lifetime (accountWorker.seenBackfillDone).
func (w *accountWorker) runSeenBackfill(ctx context.Context) {
	if w.seenBackfillDone {
		return
	}
	w.seenBackfillDone = true

	account := w.opts.account
	log := w.opts.log

	states, err := w.opts.store.Meta().ListIMAPImportMessageStatesByAccount(ctx, account.ID)
	if err != nil {
		log.Warn("imapimport: seen backfill: ListIMAPImportMessageStatesByAccount failed",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()),
		)
		return
	}

	seenMsgIDs := make(map[store.MessageID]bool, len(states))
	corrected := 0
	for _, ms := range states {
		if ctx.Err() != nil {
			return
		}
		if seenMsgIDs[ms.HeroldMessageID] {
			// Multi-mailbox placement (REQ-IMAP-IMP-51): several
			// message_state rows can address the same herold message.
			// seenBackfillOne already walks every membership of the
			// message, so a second visit would only repeat work.
			continue
		}
		seenMsgIDs[ms.HeroldMessageID] = true
		if w.seenBackfillOne(ctx, ms) {
			corrected++
		}
	}
	if corrected > 0 {
		log.Info("imapimport: seen backfill corrected existing rows",
			slog.String("account_id", account.ID),
			slog.Int("corrected", corrected),
		)
	}
}

// seenBackfillOne corrects one message: forces $seen on every \Sent-role
// membership that lacks it, then mirrors $seen onto the provenance-label
// membership if the message ends up $seen via any other membership.
// Returns true if anything was written.
func (w *accountWorker) seenBackfillOne(ctx context.Context, ms store.IMAPImportMessageState) bool {
	account := w.opts.account
	log := w.opts.log

	msg, err := w.opts.store.Meta().GetMessage(ctx, ms.HeroldMessageID)
	if err != nil {
		return false
	}

	corrected := false
	sentRoleCorrected := false
	provID := account.ProvenanceMailboxID
	anySeenElsewhere := false

	for _, mm := range msg.Mailboxes {
		seen := mm.Flags&store.MessageFlagSeen != 0
		if !seen {
			mb, mbErr := w.opts.store.Meta().GetMailboxByID(ctx, mm.MailboxID)
			if mbErr == nil && mb.Attributes&store.MailboxAttrSent != 0 {
				if _, uerr := w.opts.store.Meta().UpdateMessageFlags(
					ctx, msg.ID, mm.MailboxID, store.MessageFlagSeen, 0, nil, nil, 0,
				); uerr != nil {
					log.Warn("imapimport: seen backfill: UpdateMessageFlags (Sent) failed",
						slog.String("account_id", account.ID),
						slog.Uint64("msg_id", uint64(msg.ID)),
						slog.String("error", uerr.Error()))
				} else {
					seen = true
					corrected = true
					sentRoleCorrected = true
				}
			}
		}
		if mm.MailboxID != provID && seen {
			anySeenElsewhere = true
		}
	}

	// Bring this (account, upstream folder, uid) mapping's LastSyncedFlags
	// baseline in line with the Sent-role correction above, the same way
	// ingestedSyncedFlags keeps it consistent on a fresh ingest -- otherwise
	// a later down-sync/write-back reconcile reads the correction as a
	// spurious conflict and clears $seen right back (re #316 durability).
	if sentRoleCorrected {
		if s, found, gerr := w.opts.store.Meta().GetIMAPImportMessageState(
			ctx, account.ID, ms.UpstreamFolder, ms.UpstreamUID,
		); gerr == nil && found && !s.LastSyncedFlags.HasSeen() {
			s.LastSyncedFlags |= store.IMAPImportFlagSeen
			if uerr := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, s); uerr != nil {
				log.Warn("imapimport: seen backfill: UpsertIMAPImportMessageState failed",
					slog.String("account_id", account.ID),
					slog.Uint64("msg_id", uint64(msg.ID)),
					slog.String("error", uerr.Error()))
			}
		}
	}

	if provID != 0 && anySeenElsewhere {
		for _, mm := range msg.Mailboxes {
			if mm.MailboxID != provID || mm.Flags&store.MessageFlagSeen != 0 {
				continue
			}
			if _, uerr := w.opts.store.Meta().UpdateMessageFlags(
				ctx, msg.ID, provID, store.MessageFlagSeen, 0, nil, nil, 0,
			); uerr != nil {
				log.Warn("imapimport: seen backfill: UpdateMessageFlags (provenance) failed",
					slog.String("account_id", account.ID),
					slog.Uint64("msg_id", uint64(msg.ID)),
					slog.String("error", uerr.Error()))
			} else {
				corrected = true
			}
			break
		}
	}

	return corrected
}
