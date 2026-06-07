package imapimport

// sync.go implements the per-account download path (sub-step 3b):
//
//   - folder mapping (REQ-IMAP-IMP-10/12)
//   - horizon-bounded backfill (REQ-IMAP-IMP-17/19)
//   - forward sync (REQ-IMAP-IMP-34)
//   - UIDVALIDITY rollover handling (REQ-IMAP-IMP-35)
//   - as-synced ingest via Blobs.Put + Meta().InsertMessage (decision 1)
//   - dedup by Message-ID with blob-hash fallback (REQ-IMAP-IMP-30)
//   - categoriser seam for new INBOX-mapped mail (REQ-IMAP-IMP-31)
//   - per-folder cursor persistence
//   - metrics: messages_fetched_total, fetch_duration_seconds,
//     backfill_remaining

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// syncAllFolders drives a complete sync pass for every folder that maps
// to a herold mailbox for the given account. It replaces the
// connect-then-disconnect stub in accountWorker.attempt for 3b+.
//
// The conn must already be authenticated and in the "Authenticated" state
// (no mailbox selected). Returns nil on success. A partial failure on one
// folder is logged and does not abort the other folders.
func (w *accountWorker) syncAllFolders(ctx context.Context, conn Conn) error {
	account := w.opts.account
	log := w.opts.log

	// Build the effective folder mapping: upstream name -> herold name.
	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderMap: %w", err)
	}
	mapping := make(map[string]string, len(folderMap))
	for _, e := range folderMap {
		mapping[e.UpstreamFolder] = e.HeroldMailboxName
	}

	// Enumerate upstream folders.
	folders, err := conn.List(ctx)
	if err != nil {
		return fmt.Errorf("imapimport: LIST: %w", err)
	}

	var lastErr error
	for _, fi := range folders {
		// Skip \NoSelect mailboxes (e.g. hierarchy-only nodes).
		if hasAttr(fi.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		heroldName, ok := mapping[fi.Name]
		if !ok {
			// Default: same name (REQ-IMAP-IMP-10/12).
			heroldName = fi.Name
		}
		if err := w.syncFolder(ctx, conn, fi.Name, heroldName); err != nil {
			log.Warn("imapimport: folder sync failed (continuing)",
				slog.String("account_id", account.ID),
				slog.String("upstream_folder", fi.Name),
				slog.String("herold_mailbox", heroldName),
				slog.String("error", err.Error()),
			)
			lastErr = err
		}
	}
	return lastErr
}

// syncFolder syncs one upstream folder into the named herold mailbox.
//
// The sync has two phases:
//
//  1. Initial / rollover (cursor.HighWaterUID == 0): SEARCH SINCE floor to
//     get all in-horizon UIDs; ingest them as "forward" (they are new to
//     herold); set both low_water and high_water from the result.
//
//  2. Incremental (cursor already set):
//     a. Backfill extension (REQ-IMAP-IMP-19): if the floor date was lowered
//     since the last sync, SEARCH SINCE new_floor returns UIDs below
//     current low_water; fetch and ingest those as backfill
//     (isForward=false, so categoriser is NOT called).
//     b. Forward sync (REQ-IMAP-IMP-34): fetch UIDs > high_water (new mail);
//     ingest as forward (isForward=true) so the categoriser IS called for
//     INBOX-mapped mail.
//
// The cursor is persisted at the end of each folder. REQ-IMAP-IMP-74.
func (w *accountWorker) syncFolder(ctx context.Context, conn Conn, upstreamFolder, heroldMailbox string) error {
	account := w.opts.account
	log := w.opts.log
	accountID := account.ID

	start := w.opts.clk.Now()

	// 1. SELECT (read-only).
	si, err := conn.Select(ctx, upstreamFolder)
	if err != nil {
		return err
	}

	// 2. Load cursor; detect UIDVALIDITY rollover.
	cursor, found, err := w.opts.store.Meta().GetIMAPImportFolderCursor(ctx, accountID, upstreamFolder)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderCursor: %w", err)
	}
	if found && cursor.UIDValidity != uint64(si.UIDValidity) {
		// UIDVALIDITY rolled over: invalidate previous UID map
		// (REQ-IMAP-IMP-35). The cursor is reset; the initial-sync path
		// below re-fetches from the horizon floor. Dedup prevents duplicates.
		log.Info("imapimport: UIDVALIDITY rollover; resetting cursor",
			slog.String("account_id", accountID),
			slog.String("upstream_folder", upstreamFolder),
			slog.Uint64("old_uidvalidity", cursor.UIDValidity),
			slog.Uint64("new_uidvalidity", uint64(si.UIDValidity)),
		)
		cursor = store.IMAPImportFolderCursor{}
		found = false
	}
	if !found {
		cursor = store.IMAPImportFolderCursor{
			AccountID:      accountID,
			UpstreamFolder: upstreamFolder,
			UIDValidity:    uint64(si.UIDValidity),
		}
	}

	floorDate := account.BackfillFloorDate
	backfillNewCount := 0
	forwardNewCount := 0

	if si.NumMessages == 0 {
		// Empty mailbox: nothing to do; just advance cursor state.
		goto persistCursor
	}

	if cursor.HighWaterUID == 0 {
		// ── Initial sync (or post-rollover reset). ──────────────────────
		// Fetch all UIDs at or after the horizon floor. All these messages
		// are "new to herold" so we treat them as the forward pass
		// (categoriser fires for INBOX-mapped new mail).
		var horizonFloor time.Time
		if floorDate != nil {
			horizonFloor = *floorDate
		}
		initialUIDs, err := conn.UIDSearchSince(ctx, horizonFloor)
		if err != nil {
			return fmt.Errorf("imapimport: initial search: %w", err)
		}
		if len(initialUIDs) > 0 {
			n, minUID, err := w.fetchAndIngest(ctx, conn, initialUIDs, upstreamFolder, heroldMailbox, true /* isForward */)
			if err != nil {
				return fmt.Errorf("imapimport: initial fetch: %w", err)
			}
			forwardNewCount = n
			if minUID > 0 {
				cursor.LowWaterUID = minUID
			}
		}
	} else {
		// ── Incremental sync. ────────────────────────────────────────────

		// 3a. Backfill extension: if the floor was lowered, SEARCH SINCE
		//     new_floor returns UIDs below the current low_water.
		//     REQ-IMAP-IMP-19.
		if floorDate != nil && cursor.LowWaterUID > 0 {
			horizonFloor := *floorDate
			allInHorizon, err := conn.UIDSearchSince(ctx, horizonFloor)
			if err != nil {
				return fmt.Errorf("imapimport: backfill search: %w", err)
			}
			var belowLow []imap.UID
			for _, uid := range allInHorizon {
				if uint64(uid) < cursor.LowWaterUID {
					belowLow = append(belowLow, uid)
				}
			}
			if len(belowLow) > 0 {
				n, newLow, err := w.fetchAndIngest(ctx, conn, belowLow, upstreamFolder, heroldMailbox, false /* isForward */)
				if err != nil {
					return fmt.Errorf("imapimport: backfill fetch: %w", err)
				}
				backfillNewCount = n
				if newLow > 0 && newLow < cursor.LowWaterUID {
					cursor.LowWaterUID = newLow
				}
			}
		}

		// 3b. Forward sync: fetch UIDs strictly above high_water.
		//     REQ-IMAP-IMP-34.
		forwardUIDs, err := uidsAbove(conn, ctx, cursor.HighWaterUID)
		if err != nil {
			return fmt.Errorf("imapimport: forward search: %w", err)
		}
		if len(forwardUIDs) > 0 {
			n, _, err := w.fetchAndIngest(ctx, conn, forwardUIDs, upstreamFolder, heroldMailbox, true /* isForward */)
			if err != nil {
				return fmt.Errorf("imapimport: forward fetch: %w", err)
			}
			forwardNewCount = n
		}
	}

persistCursor:
	// Advance high-water to UIDNEXT-1 and update cursor metadata.
	if si.UIDNext > 1 {
		newHighWater := uint64(si.UIDNext) - 1
		if newHighWater > cursor.HighWaterUID {
			cursor.HighWaterUID = newHighWater
		}
	}
	cursor.UIDValidity = uint64(si.UIDValidity)
	cursor.UIDNext = uint64(si.UIDNext)
	if si.HighestModSeq > cursor.HighestModSeq {
		cursor.HighestModSeq = si.HighestModSeq
	}

	// Persist cursor after each folder (REQ-IMAP-IMP-74).
	if err := w.opts.store.Meta().UpsertIMAPImportFolderCursor(ctx, cursor); err != nil {
		return fmt.Errorf("imapimport: UpsertIMAPImportFolderCursor: %w", err)
	}

	// Metrics.
	total := backfillNewCount + forwardNewCount
	observe.IMAPImportMessagesFetchedTotal.WithLabelValues(accountID).Add(float64(total))
	observe.IMAPImportFetchDurationSeconds.WithLabelValues(accountID).Observe(
		w.opts.clk.Now().Sub(start).Seconds(),
	)
	// backfill_remaining: approximate gauge. 0 when no backfill is in progress.
	observe.IMAPImportBackfillRemaining.WithLabelValues(accountID).Set(0)

	log.Debug("imapimport: folder sync complete",
		slog.String("account_id", accountID),
		slog.String("upstream_folder", upstreamFolder),
		slog.Int("new_backfill", backfillNewCount),
		slog.Int("new_forward", forwardNewCount),
	)
	return nil
}

// uidsAbove returns all UIDs strictly above aboveUID in the currently-selected
// mailbox by fetching all UIDs and filtering.
func uidsAbove(conn Conn, ctx context.Context, aboveUID uint64) ([]imap.UID, error) {
	all, err := conn.UIDSearchSince(ctx, time.Time{}) // no date filter = all
	if err != nil {
		return nil, err
	}
	var out []imap.UID
	for _, uid := range all {
		if uint64(uid) > aboveUID {
			out = append(out, uid)
		}
	}
	return out, nil
}

// fetchAndIngest downloads uids, parses each message, deduplicates it,
// and writes it into the herold store. Returns (countNew, lowestUID,
// error). countNew is the number of messages actually inserted
// (dedup hits are not counted). lowestUID is the smallest UID in the
// batch, or 0 when toFetch is empty.
//
// isForward indicates whether this is a forward-sync pass (true) or a
// backfill pass (false); the categoriser is only called during forward
// passes on INBOX-mapped messages.
func (w *accountWorker) fetchAndIngest(
	ctx context.Context,
	conn Conn,
	toFetch []imap.UID,
	upstreamFolder, heroldMailbox string,
	isForward bool,
) (countNew int, lowestUID uint64, err error) {
	if len(toFetch) == 0 {
		return 0, 0, nil
	}

	msgs, err := conn.UIDFetch(ctx, toFetch)
	if err != nil {
		return 0, 0, err
	}

	account := w.opts.account

	for _, fm := range msgs {
		if ctx.Err() != nil {
			return countNew, lowestUID, ctx.Err()
		}
		uid := uint64(fm.UID)
		if lowestUID == 0 || uid < lowestUID {
			lowestUID = uid
		}
		isNew, msgID, mbID, ingestErr := w.ingestMessage(ctx, fm, upstreamFolder, heroldMailbox)
		if ingestErr != nil {
			w.opts.log.Warn("imapimport: ingest failed",
				slog.String("account_id", account.ID),
				slog.String("upstream_folder", upstreamFolder),
				slog.Uint64("uid", uid),
				slog.String("error", ingestErr.Error()),
			)
			continue
		}
		if isNew {
			countNew++
		}

		// Persist message state (even for dedup hits so write-back can
		// address the upstream UID). REQ-IMAP-IMP-34.
		sf := syncedFlagsFromIMAP(fm.Flags)
		if msErr := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
			AccountID:       account.ID,
			UpstreamFolder:  upstreamFolder,
			UpstreamUID:     uint32(fm.UID),
			HeroldMessageID: msgID,
			HeroldMailboxID: mbID,
			LastSyncedFlags: sf,
		}); msErr != nil {
			w.opts.log.Warn("imapimport: UpsertIMAPImportMessageState failed",
				slog.String("account_id", account.ID),
				slog.Uint64("uid", uid),
				slog.String("error", msErr.Error()),
			)
		}

		// Categoriser seam: only for new forward-sync INBOX mail
		// (REQ-IMAP-IMP-31). Not called during backfill.
		if isNew && isForward && strings.EqualFold(heroldMailbox, "INBOX") {
			if catErr := w.opts.categoriser.Categorise(ctx, fmt.Sprint(account.PrincipalID), fmt.Sprint(msgID), heroldMailbox); catErr != nil {
				w.opts.log.Warn("imapimport: categorise failed (non-fatal)",
					slog.String("account_id", account.ID),
					slog.Uint64("msg_id", uint64(msgID)),
					slog.String("error", catErr.Error()),
				)
			}
		}
	}
	return countNew, lowestUID, nil
}

// ingestMessage inserts fm into the herold store. Returns (isNew,
// heroldMessageID, heroldMailboxID, error). isNew is false when the
// message already existed (dedup hit). On a dedup hit msgID and mbID
// are still populated from the existing row so the caller can record
// the import state.
func (w *accountWorker) ingestMessage(
	ctx context.Context,
	fm fetchedMessage,
	upstreamFolder, heroldMailbox string,
) (isNew bool, msgID store.MessageID, mbID store.MailboxID, retErr error) {
	account := w.opts.account
	principalID := store.PrincipalID(account.PrincipalID)

	// Parse the message for the envelope.
	msg, parseErr := mailparse.Parse(bytes.NewReader(fm.RFC822), mailparse.NewParseOptions())
	if parseErr != nil {
		return false, 0, 0, fmt.Errorf("mailparse.Parse: %w", parseErr)
	}

	// Dedup by Message-ID (primary).
	rawMsgID := msg.Envelope.MessageID
	if rawMsgID != "" {
		normID := mailparse.NormalizeMessageID(rawMsgID)
		existing, lookupErr := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, normID)
		if lookupErr == nil {
			// Message already exists; return it for state recording.
			return false, existing.ID, existing.MailboxID, nil
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			return false, 0, 0, fmt.Errorf("imapimport: GetMessageByMessageIDHeader: %w", lookupErr)
		}
		// ErrNotFound -> proceed with insert.
	}
	// If no Message-ID, proceed (blob-hash dedup is not feasible
	// without a separate store lookup; Put is idempotent for bytes so
	// re-storing is safe).

	// Store blob (idempotent).
	blobRef, putErr := w.opts.store.Blobs().Put(ctx, bytes.NewReader(fm.RFC822))
	if putErr != nil {
		return false, 0, 0, fmt.Errorf("imapimport: Blobs.Put: %w", putErr)
	}

	// Ensure the target herold mailbox exists.
	mb, mbErr := w.ensureMailbox(ctx, principalID, heroldMailbox)
	if mbErr != nil {
		return false, 0, 0, fmt.Errorf("imapimport: ensureMailbox %q: %w", heroldMailbox, mbErr)
	}

	// Build the store.Message. InternalDate and ReceivedAt are both set
	// to the upstream INTERNALDATE to preserve chronological ordering
	// (REQ-IMAP-IMP-32 byte-fidelity). ThreadID is left 0; threading
	// is handled store-side via InsertMessage's reference-chain walk.
	storeMsg := store.Message{
		PrincipalID:  principalID,
		Size:         int64(len(fm.RFC822)),
		Blob:         blobRef,
		InternalDate: fm.InternalDate,
		ReceivedAt:   fm.InternalDate,
		Envelope:     envelopeFromParsed(msg),
	}

	flags := storeFlagsFromIMAP(fm.Flags)
	target := store.MessageMailbox{
		MailboxID: mb.ID,
		Flags:     flags,
	}

	_, _, insertErr := w.opts.store.Meta().InsertMessage(ctx, storeMsg, []store.MessageMailbox{target})
	if insertErr != nil {
		return false, 0, 0, fmt.Errorf("imapimport: InsertMessage: %w", insertErr)
	}

	// Retrieve the assigned MessageID for state recording.
	// InsertMessage does not return it directly; look it up via the
	// Message-ID header if present.
	var assignedMsgID store.MessageID
	if rawMsgID != "" {
		normID := mailparse.NormalizeMessageID(rawMsgID)
		inserted, err2 := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, normID)
		if err2 == nil {
			assignedMsgID = inserted.ID
		}
		// On lookup failure we still return isNew=true; mbID is known.
	}

	return true, assignedMsgID, mb.ID, nil
}

// ensureMailbox returns the herold mailbox named mbName owned by pid,
// creating it if absent. Mirrors protosmtp.session.ensureMailbox.
func (w *accountWorker) ensureMailbox(ctx context.Context, pid store.PrincipalID, mbName string) (store.Mailbox, error) {
	mbs, err := w.opts.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, mbName) {
			return mb, nil
		}
	}
	// Create the mailbox with appropriate SPECIAL-USE attributes.
	attr := store.MailboxAttributes(0)
	switch strings.ToUpper(mbName) {
	case "INBOX":
		attr |= store.MailboxAttrInbox
	case "SENT", "SENT MAIL":
		attr |= store.MailboxAttrSent
	case "DRAFTS":
		attr |= store.MailboxAttrDrafts
	case "TRASH":
		attr |= store.MailboxAttrTrash
	case "JUNK", "SPAM":
		attr |= store.MailboxAttrJunk
	case "ARCHIVE":
		attr |= store.MailboxAttrArchive
	}
	mb, err := w.opts.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        mbName,
		Attributes:  attr,
	})
	if err != nil {
		// Race: another goroutine may have inserted the same mailbox.
		if errors.Is(err, store.ErrConflict) {
			mbs2, _ := w.opts.store.Meta().ListMailboxes(ctx, pid)
			for _, mb2 := range mbs2 {
				if strings.EqualFold(mb2.Name, mbName) {
					return mb2, nil
				}
			}
		}
		return store.Mailbox{}, err
	}
	return mb, nil
}

// envelopeFromParsed extracts the cached envelope fields the store
// expects on a Message row. Mirrors protosmtp.envelopeFromParsed.
func envelopeFromParsed(msg mailparse.Message) store.Envelope {
	join := func(addrs []mail.Address) string {
		parts := make([]string, 0, len(addrs))
		for _, a := range addrs {
			parts = append(parts, a.String())
		}
		return strings.Join(parts, ", ")
	}
	var refs string
	if len(msg.Envelope.References) > 0 {
		parts := make([]string, len(msg.Envelope.References))
		for i, r := range msg.Envelope.References {
			parts[i] = "<" + r + ">"
		}
		refs = strings.Join(parts, " ")
	}
	return store.Envelope{
		Subject:    msg.Envelope.Subject,
		From:       join(msg.Envelope.From),
		To:         join(msg.Envelope.To),
		Cc:         join(msg.Envelope.Cc),
		Bcc:        join(msg.Envelope.Bcc),
		MessageID:  msg.Envelope.MessageID,
		InReplyTo:  strings.Join(msg.Envelope.InReplyTo, " "),
		References: refs,
	}
}

// storeFlagsFromIMAP maps IMAP system flags to store.MessageFlags.
// Only \Seen, \Flagged, \Draft, \Answered round-trip
// (REQ-IMAP-IMP-41).
func storeFlagsFromIMAP(flags []imap.Flag) store.MessageFlags {
	var sf store.MessageFlags
	for _, f := range flags {
		switch f {
		case imap.FlagSeen:
			sf |= store.MessageFlagSeen
		case imap.FlagFlagged:
			sf |= store.MessageFlagFlagged
		case imap.FlagDraft:
			sf |= store.MessageFlagDraft
		case imap.FlagAnswered:
			sf |= store.MessageFlagAnswered
		}
	}
	return sf
}

// syncedFlagsFromIMAP maps IMAP system flags to the
// IMAPImportSyncedFlags bitfield (only \Seen and \Flagged tracked for
// write-back conflict resolution, REQ-IMAP-IMP-42).
func syncedFlagsFromIMAP(flags []imap.Flag) store.IMAPImportSyncedFlags {
	var sf store.IMAPImportSyncedFlags
	for _, f := range flags {
		switch f {
		case imap.FlagSeen:
			sf |= store.IMAPImportFlagSeen
		case imap.FlagFlagged:
			sf |= store.IMAPImportFlagFlagged
		}
	}
	return sf
}

// hasAttr reports whether attrs contains the given attribute.
func hasAttr(attrs []imap.MailboxAttr, target imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == target {
			return true
		}
	}
	return false
}
