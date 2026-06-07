package imapimport

// writeback.go implements Part B of sub-step 3d: the write-back loop that
// propagates herold-side changes back to the upstream IMAP server.
//
// Design (REQ-IMAP-IMP-40..45, upstream-authoritative):
//
//   - Subscribe via store.ReadChangeFeedAll for the account's principal,
//     filtered to EntityKindEmail.
//   - A durable per-account cursor ("imapimport-writeback:<accountID>") is
//     persisted via store.GetFTSCursor / SetFTSCursor so a restart resumes
//     without replaying already-processed changes.
//   - For each changed herold message, look up the IMAPImportMessageState.
//     If no state row exists for THIS account (message belongs to a different
//     account), skip.
//   - Reconcile flags (REQ-IMAP-IMP-42 three-way: last_synced, herold-now,
//     upstream-now), then push/apply as directed.
//   - Handle MOVE (REQ-IMAP-IMP-43) and DESTROY (REQ-IMAP-IMP-44) changes.
//   - Custom keywords / other datatypes: skip (REQ-IMAP-IMP-45).
//   - All writes to the upstream are best-effort. A failed UID STORE leaves
//     last_synced unchanged so it retries next time.
//
// Cursor: we reuse GetFTSCursor / SetFTSCursor with a key of the form
//   "imapimport-writeback:<accountID>"
// The cursors table is the generic store-wide cursor table used by the FTS
// worker. The key namespace distinguishes write-back cursors from FTS cursors.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// writeBackCursorKey returns the cursor key for this account.
func writeBackCursorKey(accountID string) string {
	return "imapimport-writeback:" + accountID
}

// runWriteBack is the write-back goroutine. It runs until ctx is cancelled.
// It uses writeBackConn for all upstream IMAP write operations. The caller
// is responsible for opening writeBackConn in read-write mode (SELECT, not
// EXAMINE) before handing it to individual folder operations.
//
// writeBackConn is owned entirely by this goroutine; the caller must not
// touch it again after launching.
func (w *accountWorker) runWriteBack(ctx context.Context, writeBackConn Conn) {
	account := w.opts.account
	log := w.opts.log

	defer func() {
		if logoutErr := writeBackConn.Logout(); logoutErr != nil {
			log.Debug("imapimport: write-back LOGOUT error",
				slog.String("account_id", account.ID),
				slog.String("error", logoutErr.Error()))
		}
		writeBackConn.Close()
	}()

	cursorKey := writeBackCursorKey(account.ID)

	// Load the durable cursor so we resume from where we left off.
	fromSeq, err := w.opts.store.Meta().GetFTSCursor(ctx, cursorKey)
	if err != nil && ctx.Err() == nil {
		log.Warn("imapimport: write-back: GetFTSCursor failed; starting from 0",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()),
		)
		fromSeq = 0
	}

	log.Info("imapimport: write-back loop started",
		slog.String("account_id", account.ID),
		slog.Any("principal_id", account.PrincipalID),
		slog.Uint64("from_seq", fromSeq),
	)

	const batchSize = 100

	for {
		if ctx.Err() != nil {
			return
		}

		changes, err := w.opts.store.Meta().ReadChangeFeedAll(
			ctx,
			store.PrincipalID(account.PrincipalID),
			store.ChangeSeq(fromSeq),
			batchSize,
		)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("imapimport: write-back: ReadChangeFeedAll error",
				slog.String("account_id", account.ID),
				slog.String("error", err.Error()),
			)
			// Brief backoff before retry.
			select {
			case <-ctx.Done():
				return
			case <-w.opts.clk.After(writeBackRetryDelay):
			}
			continue
		}

		for _, ch := range changes {
			if ctx.Err() != nil {
				return
			}
			if ch.Kind != store.EntityKindEmail {
				fromSeq = uint64(ch.Seq)
				continue
			}
			msgID := store.MessageID(ch.EntityID)
			w.processWriteBack(ctx, writeBackConn, ch, msgID)
			fromSeq = uint64(ch.Seq)
		}

		// Persist cursor after each batch (even if empty, so we don't
		// re-walk the same chunk on restart). REQ-IMAP-IMP-74.
		if len(changes) > 0 {
			if setErr := w.opts.store.Meta().SetFTSCursor(ctx, cursorKey, fromSeq); setErr != nil && ctx.Err() == nil {
				log.Warn("imapimport: write-back: SetFTSCursor failed",
					slog.String("account_id", account.ID),
					slog.String("error", setErr.Error()),
				)
			}
		}

		// If we got fewer than batchSize we are at the head of the feed.
		// Poll after a short delay rather than hot-looping.
		if len(changes) < batchSize {
			select {
			case <-ctx.Done():
				return
			case <-w.opts.clk.After(writeBackPollDelay):
			}
		}
	}
}

// writeBackRetryDelay is the sleep after a ReadChangeFeedAll error.
const writeBackRetryDelay = 5 * writeBackPollDelay

// writeBackPollDelay is the idle sleep at the head of the change feed.
// Keep this short so flag changes propagate promptly.
const writeBackPollDelay = 2e9 // 2 seconds as nanoseconds (time.Duration)

// processWriteBack handles a single EntityKindEmail change. It looks up the
// IMAPImportMessageState and dispatches to the appropriate handler.
func (w *accountWorker) processWriteBack(ctx context.Context, conn Conn, ch store.StateChange, msgID store.MessageID) {
	account := w.opts.account
	log := w.opts.log

	// Look up import state for this account.
	ms, found, err := w.opts.store.Meta().GetIMAPImportMessageStateByMessage(ctx, account.ID, msgID)
	if err != nil {
		log.Warn("imapimport: write-back: GetIMAPImportMessageStateByMessage error",
			slog.String("account_id", account.ID),
			slog.Uint64("msg_id", uint64(msgID)),
			slog.String("error", err.Error()),
		)
		return
	}
	if !found {
		// Message does not belong to this account — skip.
		return
	}

	switch ch.Op {
	case store.ChangeOpUpdated:
		// Could be a flag update or a mailbox membership change (move).
		// We dispatch both; the handlers are no-ops when irrelevant.
		w.writeBackFlags(ctx, conn, ms, msgID)
		w.writeBackMove(ctx, conn, ms, msgID)

	case store.ChangeOpDestroyed:
		w.writeBackDestroy(ctx, conn, ms, msgID)
	}
	// ChangeOpCreated: a newly-mirrored message was created by THIS worker;
	// no write-back needed (we own it).
}

// writeBackFlags implements the three-way flag reconcile for \Seen and
// \Flagged. REQ-IMAP-IMP-42.
func (w *accountWorker) writeBackFlags(ctx context.Context, conn Conn, ms store.IMAPImportMessageState, msgID store.MessageID) {
	account := w.opts.account
	log := w.opts.log

	// Fetch herold-current flags.
	heroldMsg, err := w.opts.store.Meta().GetMessage(ctx, msgID)
	if err != nil {
		// Message already gone — nothing to do.
		return
	}

	heroldSynced := syncedFlagsFromStoreFlags(heroldMsg.Flags)
	lastSynced := ms.LastSyncedFlags

	if heroldSynced == lastSynced {
		// No change on herold side — nothing to push.
		return
	}

	// Select the upstream folder read-write so we can STORE.
	if _, err := conn.SelectReadWrite(ctx, ms.UpstreamFolder); err != nil {
		log.Warn("imapimport: write-back: SelectReadWrite failed",
			slog.String("account_id", account.ID),
			slog.String("folder", ms.UpstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}

	// Fetch upstream-current flags.
	upstreamFlags, err := conn.UIDFetchFlags(ctx, imap.UID(ms.UpstreamUID))
	if err != nil || upstreamFlags == nil {
		// Message gone upstream or fetch failed — skip.
		return
	}
	upstreamSynced := syncedFlagsFromIMAP(upstreamFlags)

	switch {
	case upstreamSynced == lastSynced:
		// Only herold changed → push herold's value to upstream.
		if pushErr := w.pushFlagsToUpstream(ctx, conn, ms, heroldSynced); pushErr != nil {
			log.Warn("imapimport: write-back: push flags failed (will retry)",
				slog.String("account_id", account.ID),
				slog.Uint64("uid", uint64(ms.UpstreamUID)),
				slog.String("error", pushErr.Error()),
			)
			// Do NOT update last_synced — retry next tick. REQ-IMAP-IMP-42.
			return
		}
		ms.LastSyncedFlags = heroldSynced
		w.upsertMessageState(ctx, ms)
		observe.IMAPImportFlagsPropagatedTotal.WithLabelValues(account.ID, "up").Inc()

	case heroldSynced == lastSynced:
		// Only upstream changed → apply upstream to herold.
		w.applyUpstreamFlagsToHerold(ctx, ms, upstreamSynced, heroldMsg)
		ms.LastSyncedFlags = upstreamSynced
		w.upsertMessageState(ctx, ms)

	default:
		// Both sides changed — upstream wins. REQ-IMAP-IMP-42 (decision 5).
		log.Info("imapimport: write-back: flag conflict; upstream wins",
			slog.String("account_id", account.ID),
			slog.Uint64("uid", uint64(ms.UpstreamUID)),
		)
		w.applyUpstreamFlagsToHerold(ctx, ms, upstreamSynced, heroldMsg)
		ms.LastSyncedFlags = upstreamSynced
		w.upsertMessageState(ctx, ms)
		observe.IMAPImportConflictsTotal.WithLabelValues(account.ID, "flag").Inc()
	}
}

// writeBackMove checks whether the herold-side mailbox membership has
// changed and, if so, issues a UID MOVE to the upstream.
// REQ-IMAP-IMP-43.
func (w *accountWorker) writeBackMove(ctx context.Context, conn Conn, ms store.IMAPImportMessageState, msgID store.MessageID) {
	account := w.opts.account
	log := w.opts.log

	// Fetch herold message to find its current mailbox.
	heroldMsg, err := w.opts.store.Meta().GetMessage(ctx, msgID)
	if err != nil {
		return
	}

	// If the herold MailboxID hasn't changed there is nothing to do.
	if heroldMsg.MailboxID == ms.HeroldMailboxID {
		return
	}

	// Find the upstream folder that corresponds to the new herold mailbox.
	newUpstreamFolder, err := w.reverseMapMailbox(ctx, heroldMsg.MailboxID)
	if err != nil {
		log.Debug("imapimport: write-back: move: no upstream mapping for new mailbox",
			slog.String("account_id", account.ID),
			slog.Uint64("mailbox_id", uint64(heroldMsg.MailboxID)),
		)
		return
	}

	if newUpstreamFolder == ms.UpstreamFolder {
		return // no change needed
	}

	// Select source folder read-write.
	if _, err := conn.SelectReadWrite(ctx, ms.UpstreamFolder); err != nil {
		log.Warn("imapimport: write-back: move SelectReadWrite failed",
			slog.String("account_id", account.ID),
			slog.String("folder", ms.UpstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}

	if moveErr := conn.UIDMove(ctx, imap.UID(ms.UpstreamUID), newUpstreamFolder); moveErr != nil {
		log.Warn("imapimport: write-back: UID MOVE failed (best-effort)",
			slog.String("account_id", account.ID),
			slog.Uint64("uid", uint64(ms.UpstreamUID)),
			slog.String("dest", newUpstreamFolder),
			slog.String("error", moveErr.Error()),
		)
		// Best-effort: don't update state. On the next pass, if the upstream
		// also moved the message, the download reconcile will fix herold.
		return
	}

	// Update message state with new folder and mailbox.
	ms.UpstreamFolder = newUpstreamFolder
	ms.HeroldMailboxID = heroldMsg.MailboxID
	w.upsertMessageState(ctx, ms)
}

// writeBackDestroy handles a ChangeOpDestroyed change on the herold side.
// REQ-IMAP-IMP-44.
func (w *accountWorker) writeBackDestroy(ctx context.Context, conn Conn, ms store.IMAPImportMessageState, msgID store.MessageID) {
	account := w.opts.account
	log := w.opts.log

	if !account.DeletePropagates {
		// Delete-locally-only mode: do nothing upstream. REQ-IMAP-IMP-44.
		return
	}

	// Select the upstream folder read-write.
	if _, err := conn.SelectReadWrite(ctx, ms.UpstreamFolder); err != nil {
		log.Warn("imapimport: write-back: destroy SelectReadWrite failed",
			slog.String("account_id", account.ID),
			slog.String("folder", ms.UpstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}

	uid := imap.UID(ms.UpstreamUID)

	// Mark \Deleted.
	if storeErr := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); storeErr != nil {
		log.Warn("imapimport: write-back: UID STORE \\Deleted failed (best-effort)",
			slog.String("account_id", account.ID),
			slog.Uint64("uid", uint64(uid)),
			slog.String("error", storeErr.Error()),
		)
		// Best-effort: if STORE failed, EXPUNGE won't land either. Log the
		// documented consequence (message will re-mirror on next download).
		log.Info("imapimport: write-back: destroy: EXPUNGE skipped because STORE failed; message may re-mirror",
			slog.String("account_id", account.ID),
			slog.Uint64("msg_id", uint64(msgID)),
		)
		return
	}

	// EXPUNGE.
	if expErr := conn.UIDExpunge(ctx, uid); expErr != nil {
		log.Warn("imapimport: write-back: UID EXPUNGE failed (best-effort; message may re-mirror)",
			slog.String("account_id", account.ID),
			slog.Uint64("uid", uint64(uid)),
			slog.String("error", expErr.Error()),
		)
	}
}

// pushFlagsToUpstream issues the UID STORE commands needed to make the
// upstream \Seen / \Flagged state match heroldDesired.
func (w *accountWorker) pushFlagsToUpstream(ctx context.Context, conn Conn, ms store.IMAPImportMessageState, heroldDesired store.IMAPImportSyncedFlags) error {
	uid := imap.UID(ms.UpstreamUID)

	if heroldDesired.HasSeen() {
		if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagSeen}); err != nil {
			return err
		}
	} else {
		if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsDel, []imap.Flag{imap.FlagSeen}); err != nil {
			return err
		}
	}

	if heroldDesired.HasFlagged() {
		if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagFlagged}); err != nil {
			return err
		}
	} else {
		if err := conn.UIDStoreFlags(ctx, uid, imap.StoreFlagsDel, []imap.Flag{imap.FlagFlagged}); err != nil {
			return err
		}
	}
	return nil
}

// applyUpstreamFlagsToHerold updates the herold-side \Seen / \Flagged to
// match the upstream value. Called on conflict-upstream-wins and on
// upstream-only-changed paths. REQ-IMAP-IMP-42.
func (w *accountWorker) applyUpstreamFlagsToHerold(ctx context.Context, ms store.IMAPImportMessageState, upstreamSynced store.IMAPImportSyncedFlags, heroldMsg store.Message) {
	log := w.opts.log
	account := w.opts.account

	var flagAdd, flagClear store.MessageFlags

	if upstreamSynced.HasSeen() {
		flagAdd |= store.MessageFlagSeen
	} else {
		flagClear |= store.MessageFlagSeen
	}
	if upstreamSynced.HasFlagged() {
		flagAdd |= store.MessageFlagFlagged
	} else {
		flagClear |= store.MessageFlagFlagged
	}

	// Nothing to do when both herold-now and upstream agree (no-op delta).
	if flagAdd == 0 && flagClear == 0 {
		return
	}
	// Compute actual delta against herold-current flags to avoid spurious writes.
	currentSeen := heroldMsg.Flags&store.MessageFlagSeen != 0
	currentFlagged := heroldMsg.Flags&store.MessageFlagFlagged != 0
	desiredSeen := upstreamSynced.HasSeen()
	desiredFlagged := upstreamSynced.HasFlagged()

	var realAdd, realClear store.MessageFlags
	if desiredSeen && !currentSeen {
		realAdd |= store.MessageFlagSeen
	} else if !desiredSeen && currentSeen {
		realClear |= store.MessageFlagSeen
	}
	if desiredFlagged && !currentFlagged {
		realAdd |= store.MessageFlagFlagged
	} else if !desiredFlagged && currentFlagged {
		realClear |= store.MessageFlagFlagged
	}
	if realAdd == 0 && realClear == 0 {
		return
	}

	if _, err := w.opts.store.Meta().UpdateMessageFlags(
		ctx,
		heroldMsg.ID,
		heroldMsg.MailboxID,
		realAdd, realClear,
		nil, nil,
		0, // no UNCHANGEDSINCE constraint
	); err != nil {
		log.Warn("imapimport: write-back: UpdateMessageFlags (herold) failed",
			slog.String("account_id", account.ID),
			slog.Uint64("msg_id", uint64(heroldMsg.ID)),
			slog.String("error", err.Error()),
		)
	}
}

// upsertMessageState updates the persisted message state.
func (w *accountWorker) upsertMessageState(ctx context.Context, ms store.IMAPImportMessageState) {
	if err := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil && ctx.Err() == nil {
		w.opts.log.Warn("imapimport: write-back: UpsertIMAPImportMessageState failed",
			slog.String("account_id", w.opts.account.ID),
			slog.String("error", err.Error()),
		)
	}
}

// reverseMapMailbox finds the upstream folder name corresponding to a herold
// MailboxID, using the inverse of the folder map.
func (w *accountWorker) reverseMapMailbox(ctx context.Context, heroldMailboxID store.MailboxID) (string, error) {
	account := w.opts.account

	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return "", fmt.Errorf("GetIMAPImportFolderMap: %w", err)
	}

	// Look up the mailbox name from the store.
	mbs, err := w.opts.store.Meta().ListMailboxes(ctx, store.PrincipalID(account.PrincipalID))
	if err != nil {
		return "", fmt.Errorf("ListMailboxes: %w", err)
	}
	var heroldName string
	for _, mb := range mbs {
		if mb.ID == heroldMailboxID {
			heroldName = mb.Name
			break
		}
	}
	if heroldName == "" {
		return "", fmt.Errorf("mailbox %d not found", heroldMailboxID)
	}

	// Reverse lookup: find an upstream folder mapped to this herold name.
	for _, e := range folderMap {
		if strings.EqualFold(e.HeroldMailboxName, heroldName) {
			return e.UpstreamFolder, nil
		}
	}
	// Default: name-equals-name.
	return heroldName, nil
}

// syncedFlagsFromStoreFlags converts store.MessageFlags to
// IMAPImportSyncedFlags (only \Seen and \Flagged).
func syncedFlagsFromStoreFlags(flags store.MessageFlags) store.IMAPImportSyncedFlags {
	var sf store.IMAPImportSyncedFlags
	if flags&store.MessageFlagSeen != 0 {
		sf |= store.IMAPImportFlagSeen
	}
	if flags&store.MessageFlagFlagged != 0 {
		sf |= store.IMAPImportFlagFlagged
	}
	return sf
}
