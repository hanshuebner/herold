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
//
// When the upstream is detected as Gmail (X-GM-EXT-1 capability +
// [Gmail]/All Mail folder), this function delegates to syncAllFoldersGmail
// (gmail.go) which skips per-label folders and uses All Mail as the
// canonical body source. All non-Gmail upstreams proceed through the
// original per-folder loop below. (REQ-IMAP-IMP-50/51.)
func (w *accountWorker) syncAllFolders(ctx context.Context, conn Conn) error {
	account := w.opts.account
	log := w.opts.log

	// Enumerate upstream folders (needed for both Gmail detection and the
	// default per-folder loop).
	folders, err := conn.List(ctx)
	if err != nil {
		return fmt.Errorf("imapimport: LIST: %w", err)
	}

	// Gmail detection: gate ALL Gmail-specific behaviour on this check.
	if isGmailServer(conn.Caps(), folders, account.Host) {
		log.Info("imapimport: Gmail detected; using All-Mail optimization",
			slog.String("account_id", account.ID),
			slog.String("host", account.Host),
		)
		return w.syncAllFoldersGmail(ctx, conn, folders)
	}

	// Non-Gmail path: standard per-folder loop.

	// Build the effective folder mapping: upstream name -> herold name.
	// Precedence (REQ-IMAP-IMP-10/11): per-account map wins over the
	// operator's system-wide default-for-host map, which wins over the
	// name-equals-name fallback applied below.
	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderMap: %w", err)
	}
	mapping := make(map[string]string)
	for k, v := range w.opts.cfg.IMAPImportDefaultFolderMapFor(account.Host) {
		mapping[k] = v
	}
	for _, e := range folderMap {
		mapping[e.UpstreamFolder] = e.HeroldMailboxName
	}

	// Reset the per-pass backfill-remaining accumulator; each folder adds its
	// count and we publish the account-wide sum to the gauge below (D6).
	w.backfillRemaining = 0

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
		// Update live status so observers see which folder is active.
		w.status.setSyncingFolder(fi.Name)
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
	// Publish the account-wide backfill-remaining gauge (REQ-IMAP-IMP-63).
	observe.IMAPImportBackfillRemaining.WithLabelValues(account.ID).Set(float64(w.backfillRemaining))
	return lastErr
}

// syncFolder syncs one upstream folder into the named herold mailbox.
//
// The sync has two phases:
//
//  1. Initial / rollover (cursor.HighWaterUID == 0): SEARCH SINCE floor to
//     get all in-horizon UIDs and ingest them. This is the historical
//     backfill: the LLM categoriser is NOT run across it (REQ-IMAP-IMP-31),
//     unless the folder was already initialised on a prior pass (an
//     already-known but previously-empty folder receiving its first live
//     mail). Both low_water and high_water are set from the result.
//
//  2. Incremental (cursor already set):
//     a. Backfill extension (REQ-IMAP-IMP-19): if the floor date was lowered
//     since the last sync, SEARCH SINCE new_floor returns UIDs below
//     current low_water; fetch and ingest those as backfill
//     (categorise=false, so the categoriser is NOT called).
//     b. Forward sync (REQ-IMAP-IMP-34): fetch UIDs > high_water (new mail);
//     these are genuine live arrivals on an already-initialised folder, so
//     the categoriser IS called for INBOX-mapped mail (categorise=true).
//
// The categoriser gate (REQ-IMAP-IMP-31 / D1): categorisation runs ONLY for
// genuinely-new live arrivals — mail that turns up after the folder's first
// sync has completed — and NEVER across the initial/historical backfill. The
// signal for "the folder was already initialised before this pass" is whether
// a cursor row already existed (found); a brand-new folder and a UIDVALIDITY
// rollover both reset that to false so their (re)mirror is treated as backfill.
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

	// folderInitialised is true when a cursor row already existed before this
	// pass, i.e. the folder has been synced at least once. It is the gate for
	// LLM categorisation (REQ-IMAP-IMP-31 / D1): the initial/historical
	// backfill of a fresh folder (and a forced re-sync after a UIDVALIDITY
	// rollover, where found was reset to false above) must NOT be categorised;
	// only mail arriving on an already-initialised folder is.
	folderInitialised := found

	// inHorizonUIDs holds the result of the most recent UID SEARCH SINCE
	// <floor> in this pass; it is used at persistCursor to compute the
	// backfill-remaining gauge (REQ-IMAP-IMP-63 / D6). Nil when the pass did
	// not search (empty mailbox, or a forward-only incremental pass whose
	// backfill is already complete) — in which case the remaining count is 0.
	var inHorizonUIDs []imap.UID

	if si.NumMessages == 0 {
		// Empty mailbox: nothing to do; just advance cursor state.
		goto persistCursor
	}

	if cursor.HighWaterUID == 0 {
		// ── Initial sync (or post-rollover reset). ──────────────────────
		// Fetch all UIDs at or after the horizon floor. On a brand-new or
		// post-rollover folder this is the historical backfill and is NOT
		// categorised. If the folder was already initialised (a previously-
		// empty known folder now receiving its first live mail), categorise.
		var horizonFloor time.Time
		if floorDate != nil {
			horizonFloor = *floorDate
		}
		initialUIDs, err := conn.UIDSearchSince(ctx, horizonFloor)
		if err != nil {
			return fmt.Errorf("imapimport: initial search: %w", err)
		}
		inHorizonUIDs = initialUIDs
		if len(initialUIDs) > 0 {
			n, minUID, err := w.fetchAndIngest(ctx, conn, initialUIDs, upstreamFolder, heroldMailbox, folderInitialised /* categorise */)
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
		//     REQ-IMAP-IMP-19. A nil floor ("all", e.g. forced by the
		//     complete-migration cutover, REQ-IMAP-IMP-91) drives the re-scan
		//     down to the earliest UID: UIDSearchSince(zero) returns every UID
		//     and the below-low_water set is the entire un-fetched history.
		if cursor.LowWaterUID > 0 {
			var horizonFloor time.Time
			if floorDate != nil {
				horizonFloor = *floorDate
			}
			allInHorizon, err := conn.UIDSearchSince(ctx, horizonFloor)
			if err != nil {
				return fmt.Errorf("imapimport: backfill search: %w", err)
			}
			inHorizonUIDs = allInHorizon
			var belowLow []imap.UID
			for _, uid := range allInHorizon {
				if uint64(uid) < cursor.LowWaterUID {
					belowLow = append(belowLow, uid)
				}
			}
			if len(belowLow) > 0 {
				n, newLow, err := w.fetchAndIngest(ctx, conn, belowLow, upstreamFolder, heroldMailbox, false /* categorise */)
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
			// Forward UIDs on an already-initialised folder are genuine live
			// arrivals: categorise INBOX-mapped new mail (REQ-IMAP-IMP-31).
			n, _, err := w.fetchAndIngest(ctx, conn, forwardUIDs, upstreamFolder, heroldMailbox, true /* categorise */)
			if err != nil {
				return fmt.Errorf("imapimport: forward fetch: %w", err)
			}
			forwardNewCount = n
		}
	}

	// Apply upstream-only \Seen / \Flagged changes down to herold for an
	// already-initialised folder (D2+D3, REQ-IMAP-IMP-40/24/42). Uses the
	// cursor's stored highest_modseq as the CONDSTORE base; persistCursor then
	// advances it from the SELECT response below. Best-effort: a failure logs
	// and does not abort the folder. The initial pass is skipped (the messages
	// were just mirrored with their current flags as last_synced).
	//
	// Authority transfer (REQ-IMAP-IMP-92): once the complete-migration cutover
	// has begun (migrating/migrated), herold is authoritative for already-
	// mirrored mail. The down-sync MUST NOT overwrite herold-side \Seen /
	// \Flagged with the upstream value, so it is skipped entirely — the cutover
	// only ADDS not-yet-mirrored messages (handled by the fetch paths above)
	// and never rewrites the flags of existing mail.
	if folderInitialised && !w.authorityIsHerold() {
		if err := w.downSyncFlags(ctx, conn, upstreamFolder, &cursor, floorDate); err != nil {
			log.Warn("imapimport: down-sync flags failed (continuing)",
				slog.String("account_id", accountID),
				slog.String("upstream_folder", upstreamFolder),
				slog.String("error", err.Error()),
			)
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
	// Live snapshot counter mirrors the Prometheus counter.
	w.status.incFetched(int64(total))
	observe.IMAPImportFetchDurationSeconds.WithLabelValues(accountID).Observe(
		w.opts.clk.Now().Sub(start).Seconds(),
	)
	// backfill_remaining (REQ-IMAP-IMP-63 / D6): the count of in-horizon UIDs
	// below this folder's low-water mark that are not yet mirrored. After a
	// completed backfill this is 0; it is non-zero while older in-horizon mail
	// remains to be fetched (e.g. a freshly-lowered horizon, or a resumed
	// partial backfill). Accumulated per folder; syncAllFolders publishes the
	// account-wide sum to the gauge once the pass finishes.
	w.backfillRemaining += int64(countUIDsBelow(inHorizonUIDs, cursor.LowWaterUID))

	log.Debug("imapimport: folder sync complete",
		slog.String("account_id", accountID),
		slog.String("upstream_folder", upstreamFolder),
		slog.Int("new_backfill", backfillNewCount),
		slog.Int("new_forward", forwardNewCount),
	)
	return nil
}

// countUIDsBelow returns how many UIDs in uids are strictly below mark. It is
// the per-folder backfill-remaining measure (REQ-IMAP-IMP-63 / D6): given the
// in-horizon UID set and the folder's low-water mark, the UIDs still below the
// mark are the in-horizon mail not yet fetched. Returns 0 for a nil/empty set
// or a zero mark.
func countUIDsBelow(uids []imap.UID, mark uint64) int {
	if mark == 0 {
		return 0
	}
	n := 0
	for _, uid := range uids {
		if uint64(uid) < mark {
			n++
		}
	}
	return n
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
// categorise indicates whether new INBOX-mapped members ingested in this pass
// should be handed to the LLM categoriser. It is true ONLY for genuine live
// arrivals on an already-initialised folder, and false across the
// initial/historical backfill and the lowered-horizon re-scan
// (REQ-IMAP-IMP-31 / D1).
func (w *accountWorker) fetchAndIngest(
	ctx context.Context,
	conn Conn,
	toFetch []imap.UID,
	upstreamFolder, heroldMailbox string,
	categorise bool,
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
		isNew, isNewMember, msgID, mbID, ingestErr := w.ingestMessage(ctx, fm, upstreamFolder, heroldMailbox)
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

		// Categoriser seam: for new INBOX-mapped messages on a live-arrival
		// pass (REQ-IMAP-IMP-31). Never called across the initial/historical
		// backfill (categorise=false) — D1.
		//
		// isNewMember is true for both fresh inserts and for dedup hits where
		// AddMessageToMailbox just placed the message into this mailbox for the
		// first time. The latter handles the Gmail label-before-INBOX ordering:
		// when a user-label folder syncs before INBOX the message is created
		// there (isNew=true, but mailbox != INBOX so no categorise), then INBOX
		// is synced and the dedup path adds the membership (isNew=false but
		// isNewMember=true, mailbox == INBOX so categorise fires). Without this
		// check the message would never receive a $category-* keyword (re #27).
		if isNewMember && categorise && strings.EqualFold(heroldMailbox, "INBOX") {
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
// isNewMember, heroldMessageID, heroldMailboxID, error). isNew is false when
// the message already existed (dedup hit). isNewMember is true when the
// message was just placed into heroldMailbox for the first time — either as a
// fresh insert (isNew=true) or as a dedup hit where AddMessageToMailbox
// created the membership now (isNew=false). isNewMember is false when the
// message was already a member of heroldMailbox before this call (e.g. a
// second sync pass of the same folder). Callers use isNewMember, not isNew,
// to decide whether to run the categoriser so that categorisation happens
// regardless of which upstream folder is synced first (re #27).
//
// On any error, msgID and mbID are still populated where known so the caller
// can record the import state.
//
// Multi-mailbox dedup (the foundation of Gmail label placement and any
// multi-folder IMAP account): if the message already exists in herold
// but is NOT yet a member of heroldMailbox, AddMessageToMailbox is
// called to create the additional membership. This makes "a message in
// K upstream folders -> K herold mailbox memberships" correct for any
// IMAP account. Re-fetching the SAME folder is a no-op (the membership
// already exists), keeping TestDedup green.
func (w *accountWorker) ingestMessage(
	ctx context.Context,
	fm fetchedMessage,
	upstreamFolder, heroldMailbox string,
) (isNew bool, isNewMember bool, msgID store.MessageID, mbID store.MailboxID, retErr error) {
	account := w.opts.account
	principalID := store.PrincipalID(account.PrincipalID)

	// Parse the message for the envelope.
	msg, parseErr := mailparse.Parse(bytes.NewReader(fm.RFC822), mailparse.NewParseOptions())
	if parseErr != nil {
		return false, false, 0, 0, fmt.Errorf("mailparse.Parse: %w", parseErr)
	}

	// Dedup by Message-ID (primary, REQ-IMAP-IMP-30).
	rawMsgID := msg.Envelope.MessageID
	if rawMsgID != "" {
		normID := mailparse.NormalizeMessageID(rawMsgID)
		existing, lookupErr := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, normID)
		if lookupErr == nil {
			newMember, eid, embID, placeErr := w.placeExistingMessage(ctx, principalID, existing, heroldMailbox)
			return false, newMember, eid, embID, placeErr
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			return false, false, 0, 0, fmt.Errorf("imapimport: GetMessageByMessageIDHeader: %w", lookupErr)
		}
		// ErrNotFound -> proceed with insert.
	}

	// Store blob (idempotent). The content-addressed hash doubles as the
	// fallback dedup key for messages that have no usable Message-ID.
	blobRef, putErr := w.opts.store.Blobs().Put(ctx, bytes.NewReader(fm.RFC822))
	if putErr != nil {
		return false, false, 0, 0, fmt.Errorf("imapimport: Blobs.Put: %w", putErr)
	}

	// Dedup by blob_hash (fallback) when there is no Message-ID, matching the
	// bulk importer's content-hash dedup (REQ-IMAP-IMP-30). Without this a
	// no-Message-ID message re-fetched on a later pass would insert a fresh
	// messages row every time.
	if rawMsgID == "" {
		existing, lookupErr := w.opts.store.Meta().GetMessageByBlobHash(ctx, principalID, blobRef.Hash)
		if lookupErr == nil {
			newMember, eid, embID, placeErr := w.placeExistingMessage(ctx, principalID, existing, heroldMailbox)
			return false, newMember, eid, embID, placeErr
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			return false, false, 0, 0, fmt.Errorf("imapimport: GetMessageByBlobHash: %w", lookupErr)
		}
		// ErrNotFound -> proceed with insert.
	}

	// Ensure the target herold mailbox exists.
	mb, mbErr := w.ensureMailbox(ctx, principalID, heroldMailbox)
	if mbErr != nil {
		return false, false, 0, 0, fmt.Errorf("imapimport: ensureMailbox %q: %w", heroldMailbox, mbErr)
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
		return false, false, 0, 0, fmt.Errorf("imapimport: InsertMessage: %w", insertErr)
	}

	// Retrieve the assigned MessageID for state recording. InsertMessage does
	// not return it directly; look it up by Message-ID when present, else by
	// the content hash (so write-back can address no-Message-ID mail too).
	var assignedMsgID store.MessageID
	if rawMsgID != "" {
		normID := mailparse.NormalizeMessageID(rawMsgID)
		if inserted, err2 := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, normID); err2 == nil {
			assignedMsgID = inserted.ID
		}
	} else {
		if inserted, err2 := w.opts.store.Meta().GetMessageByBlobHash(ctx, principalID, blobRef.Hash); err2 == nil {
			assignedMsgID = inserted.ID
		}
	}
	// On lookup failure we still return isNew=true; mbID is known.

	// Tag with the per-account provenance label (REQ-IMAP-IMP-100).
	w.addProvenanceLabel(ctx, assignedMsgID)

	// Fresh insert: the message is a new member of heroldMailbox.
	return true, true, assignedMsgID, mb.ID, nil
}

// placeExistingMessage handles a dedup hit: the message `existing` is already
// mirrored in herold. It ensures the message is also a member of heroldMailbox
// (multi-mailbox-on-dedup, REQ-IMAP-IMP-51) and reports whether the membership
// was created by this call. Used by both the Message-ID and the blob_hash
// dedup paths (REQ-IMAP-IMP-30). isNewMember=false when the message was
// already in heroldMailbox or a concurrent ingest won the race (ErrConflict),
// so the caller does not double-categorise.
func (w *accountWorker) placeExistingMessage(
	ctx context.Context,
	principalID store.PrincipalID,
	existing store.Message,
	heroldMailbox string,
) (isNewMember bool, msgID store.MessageID, mbID store.MailboxID, err error) {
	targetMB, mbErr := w.ensureMailbox(ctx, principalID, heroldMailbox)
	if mbErr != nil {
		return false, existing.ID, existing.MailboxID, fmt.Errorf("imapimport: ensureMailbox %q (dedup): %w", heroldMailbox, mbErr)
	}
	// Tag with the per-account provenance label (REQ-IMAP-IMP-100); idempotent
	// across the K folder placements of a multi-mailbox dedup.
	w.addProvenanceLabel(ctx, existing.ID)
	for _, mm := range existing.Mailboxes {
		if mm.MailboxID == targetMB.ID {
			// Already a member (e.g. re-fetching the same folder): no-op.
			return false, existing.ID, targetMB.ID, nil
		}
	}
	// Add the membership. Idempotent per AddMessageToMailbox's ErrConflict
	// guard — a concurrent ingest of the same message into the same mailbox
	// loses the race harmlessly.
	if _, _, addErr := w.opts.store.Meta().AddMessageToMailbox(ctx, existing.ID, targetMB.ID); addErr != nil {
		if !errors.Is(addErr, store.ErrConflict) {
			return false, existing.ID, targetMB.ID, fmt.Errorf("imapimport: AddMessageToMailbox (dedup): %w", addErr)
		}
		// ErrConflict: another path already added it; not a new member here.
		return false, existing.ID, targetMB.ID, nil
	}
	// Membership created now: the caller should categorise if INBOX.
	return true, existing.ID, targetMB.ID, nil
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

// ensureProvenanceMailbox makes sure this account's provenance label
// (REQ-IMAP-IMP-100..101) exists and caches its id on the account row and
// the in-memory account copy. The label is a normal herold Mailbox with no
// special-use role, named from the account name. Idempotent: a no-op once
// the id is cached. Called once per session before the first ingest.
func (w *accountWorker) ensureProvenanceMailbox(ctx context.Context) error {
	if w.opts.account.ProvenanceMailboxID != 0 {
		return nil
	}
	pid := store.PrincipalID(w.opts.account.PrincipalID)
	name := w.opts.account.AccountName
	if name == "" {
		name = "Imported (" + w.opts.account.Host + ")"
	}

	// Find an existing mailbox by name; adopt it if present (so a restart
	// after the row was created but before the id was cached re-binds it).
	mbs, err := w.opts.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return err
	}
	var mbID store.MailboxID
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, name) {
			mbID = mb.ID
			break
		}
	}
	if mbID == 0 {
		// Create with NO special-use role (REQ-IMAP-IMP-101).
		mb, ierr := w.opts.store.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid,
			Name:        name,
			Attributes:  store.MailboxAttributes(0),
		})
		if ierr != nil {
			if errors.Is(ierr, store.ErrConflict) {
				mbs2, _ := w.opts.store.Meta().ListMailboxes(ctx, pid)
				for _, mb2 := range mbs2 {
					if strings.EqualFold(mb2.Name, name) {
						mbID = mb2.ID
						break
					}
				}
			}
			if mbID == 0 {
				return ierr
			}
		} else {
			mbID = mb.ID
		}
	}

	if err := w.opts.store.Meta().SetIMAPImportProvenanceMailbox(ctx, w.opts.account.ID, mbID); err != nil {
		return err
	}
	w.opts.account.ProvenanceMailboxID = mbID
	return nil
}

// addProvenanceLabel adds the account's provenance-label membership to msgID
// (REQ-IMAP-IMP-100). Idempotent: an existing membership (ErrConflict) is
// ignored, so backfilled and re-synced mail converge on a single membership.
// A no-op when the provenance label has not been created yet (id zero) or the
// message id is unknown.
func (w *accountWorker) addProvenanceLabel(ctx context.Context, msgID store.MessageID) {
	provID := w.opts.account.ProvenanceMailboxID
	if provID == 0 || msgID == 0 {
		return
	}
	if _, _, err := w.opts.store.Meta().AddMessageToMailbox(ctx, msgID, provID); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			w.opts.log.Warn("imapimport: failed to add provenance label",
				slog.String("account_id", w.opts.account.ID),
				slog.String("error", err.Error()))
		}
	}
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
