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
	"github.com/hanshuebner/herold/internal/spam"
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
	// When the upstream advertises X-GM-EXT-1, use true per-message label
	// placement via X-GM-LABELS (REQ-IMAP-IMP-53), which supersedes the
	// folder-based interim placement (REQ-IMAP-IMP-50/51). syncAllFoldersGmailLabels
	// itself falls back to folder-based placement when no [Gmail]/All Mail folder
	// is present. Non-Gmail upstreams use the general per-folder loop below, which
	// is the folder-based fallback for any server without X-GM-EXT-1.
	if isGmailServer(conn.Caps(), folders, account.Host) {
		log.Info("imapimport: Gmail detected; using X-GM-LABELS per-message placement",
			slog.String("account_id", account.ID),
			slog.String("host", account.Host),
		)
		return w.syncAllFoldersGmailLabels(ctx, conn, folders)
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

	// excludedFolders is the per-account no-sync set (re #303/#305): an
	// excluded folder is never SELECTed, so it gets no cursor row and no
	// message_state rows. A folder that was already synced before it became
	// excluded is cleaned up below rather than just skipped.
	excludedFolders := make(map[string]bool, len(account.ExcludedFolders))
	for _, f := range account.ExcludedFolders {
		excludedFolders[f] = true
	}

	var lastErr error
	for _, fi := range folders {
		// Skip \NoSelect mailboxes (e.g. hierarchy-only nodes).
		if hasAttr(fi.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		if excludedFolders[fi.Name] {
			w.excludeFolderCleanup(ctx, fi.Name)
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

	// highWaterFailedUID is the smallest UID, from the initial-sync or
	// forward-sync batch, whose ingest failed this pass (0 if none). It
	// caps the high-water advance below at persistCursor so the failed
	// message stays strictly above high_water and is refetched on the next
	// pass rather than being silently skipped past the mark (re #279).
	// Backfill-extension failures (historical UIDs, always far below any
	// existing high_water) intentionally do not feed this cap — clamping
	// high_water to a low historical UID would stall forward sync for
	// every subsequently-arriving message too.
	var highWaterFailedUID uint64

	// currentUpstreamUIDs holds the full current UID set of the folder (no
	// SINCE bound), populated only on an incremental pass's forward-sync
	// search below. It is the base set for upstream-expunge reconciliation
	// (re #303): a message_state row whose UID is absent from this set was
	// expunged upstream since the last sync. haveCurrentUpstreamUIDs
	// distinguishes "search ran, folder happens to be empty now" from
	// "search never ran this pass" (initial sync, rollover, empty mailbox)
	// — an empty-but-populated result set must still drive reconciliation.
	var currentUpstreamUIDs []imap.UID
	var haveCurrentUpstreamUIDs bool

	if si.NumMessages == 0 {
		// Empty mailbox: nothing to fetch. If the folder was already
		// initialised, still reconcile against an empty current-UID set
		// (re #303): every message_state row this account holds for the
		// folder is now stale, whether each message was expunged
		// individually earlier (and already caught below) or the folder's
		// last message(s) vanished between this pass and the last — the
		// "goto persistCursor" below would otherwise skip that case
		// entirely, since it jumps past the guarded down-sync/reconcile
		// block that normally runs after the if/else below.
		if folderInitialised && !w.authorityIsHerold() {
			if err := w.reconcileExpungedMessages(ctx, upstreamFolder, nil); err != nil {
				log.Warn("imapimport: expunge reconcile failed (continuing)",
					slog.String("account_id", accountID),
					slog.String("upstream_folder", upstreamFolder),
					slog.String("error", err.Error()),
				)
			}
		}
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
			n, minUID, minFailed, err := w.fetchAndIngest(ctx, conn, initialUIDs, upstreamFolder, heroldMailbox, folderInitialised /* categorise */)
			if err != nil {
				return fmt.Errorf("imapimport: initial fetch: %w", err)
			}
			forwardNewCount = n
			if minUID > 0 {
				cursor.LowWaterUID = minUID
			}
			highWaterFailedUID = minFailed
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
				n, newLow, _, err := w.fetchAndIngest(ctx, conn, belowLow, upstreamFolder, heroldMailbox, false /* categorise */)
				if err != nil {
					return fmt.Errorf("imapimport: backfill fetch: %w", err)
				}
				backfillNewCount = n
				if newLow > 0 && newLow < cursor.LowWaterUID {
					cursor.LowWaterUID = newLow
				}
			}
		}

		// 3b. Forward sync: fetch UIDs strictly above high_water. The
		//     unbounded search also gives us the folder's full current UID
		//     set, reused below for upstream-expunge reconciliation
		//     (re #303) so it costs one UID SEARCH ALL round-trip, not two.
		//     REQ-IMAP-IMP-34.
		allUIDs, err := conn.UIDSearchSince(ctx, time.Time{})
		if err != nil {
			return fmt.Errorf("imapimport: forward search: %w", err)
		}
		currentUpstreamUIDs = allUIDs
		haveCurrentUpstreamUIDs = true
		var forwardUIDs []imap.UID
		for _, uid := range allUIDs {
			if uint64(uid) > cursor.HighWaterUID {
				forwardUIDs = append(forwardUIDs, uid)
			}
		}
		if len(forwardUIDs) > 0 {
			// Forward UIDs on an already-initialised folder are genuine live
			// arrivals: categorise INBOX-mapped new mail (REQ-IMAP-IMP-31).
			n, _, minFailed, err := w.fetchAndIngest(ctx, conn, forwardUIDs, upstreamFolder, heroldMailbox, true /* categorise */)
			if err != nil {
				return fmt.Errorf("imapimport: forward fetch: %w", err)
			}
			forwardNewCount = n
			highWaterFailedUID = minFailed
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

		// Reconcile upstream expunges (re #303): drop the membership and
		// message_state row for any previously-mirrored UID this pass's
		// unbounded UID search no longer sees. Only runs when the pass took
		// the incremental branch above (currentUpstreamUIDs non-nil) — an
		// initial/rollover pass or an empty mailbox has no expunges to
		// detect against. Same authority-transfer gate as downSyncFlags:
		// once herold is authoritative (migrating/migrated) upstream state
		// no longer drives herold-side removals.
		if haveCurrentUpstreamUIDs {
			if err := w.reconcileExpungedMessages(ctx, upstreamFolder, currentUpstreamUIDs); err != nil {
				log.Warn("imapimport: expunge reconcile failed (continuing)",
					slog.String("account_id", accountID),
					slog.String("upstream_folder", upstreamFolder),
					slog.String("error", err.Error()),
				)
			}
		}
	}

persistCursor:
	// Advance high-water to UIDNEXT-1 and update cursor metadata. Capped
	// below any UID that failed to ingest this pass (re #279): advancing
	// past it unconditionally would make it permanently unreachable, since
	// forward sync only fetches UIDs strictly above high_water.
	if si.UIDNext > 1 {
		newHighWater := uint64(si.UIDNext) - 1
		if highWaterFailedUID > 0 && highWaterFailedUID-1 < newHighWater {
			newHighWater = highWaterFailedUID - 1
		}
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
// minFailedUID is the smallest upstream UID in toFetch whose ingest failed
// (0 when every message in the batch either ingested or dedup-hit
// successfully). Callers use it to keep the cursor water marks from
// advancing past a message that failed to ingest (re #279): a message
// skipped by an unconditional high-water advance is never fetched again,
// since forward sync only looks strictly above high_water.
func (w *accountWorker) fetchAndIngest(
	ctx context.Context,
	conn Conn,
	toFetch []imap.UID,
	upstreamFolder, heroldMailbox string,
	categorise bool,
) (countNew int, lowestUID uint64, minFailedUID uint64, err error) {
	if len(toFetch) == 0 {
		return 0, 0, 0, nil
	}

	msgs, err := conn.UIDFetch(ctx, toFetch)
	if err != nil {
		return 0, 0, 0, err
	}

	account := w.opts.account

	for _, fm := range msgs {
		if ctx.Err() != nil {
			return countNew, lowestUID, minFailedUID, ctx.Err()
		}
		uid := uint64(fm.UID)
		if lowestUID == 0 || uid < lowestUID {
			lowestUID = uid
		}
		if hasFlag(fm.Flags, imap.FlagDeleted) {
			// A message flagged \Deleted upstream is not imported from this
			// folder (re #303). If an earlier pass had already mirrored it
			// from here (the flag was set afterwards, before expunge), drop
			// that folder's membership now.
			if ms, found, gerr := w.opts.store.Meta().GetIMAPImportMessageState(ctx, account.ID, upstreamFolder, uint32(fm.UID)); gerr == nil && found {
				w.removeMessageStateMembership(ctx, ms)
			}
			continue
		}

		isNew, isNewMember, msgID, mbID, finalMailbox, ingestErr := w.ingestMessage(ctx, fm, upstreamFolder, heroldMailbox, categorise)
		if ingestErr != nil {
			if errors.Is(ingestErr, errINBOXSuppressedByJunk) {
				// Junk wins over inbox (re #303): this folder's mapped
				// INBOX placement is intentionally suppressed because the
				// message already carries a Junk-attributed membership.
				// Not a failure: no message_state row is recorded for this
				// (folder, uid) since no membership was created here.
				continue
			}
			w.opts.log.Warn("imapimport: ingest failed",
				slog.String("account_id", account.ID),
				slog.String("upstream_folder", upstreamFolder),
				slog.Uint64("uid", uid),
				slog.String("error", ingestErr.Error()),
			)
			if minFailedUID == 0 || uid < minFailedUID {
				minFailedUID = uid
			}
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
		//
		// Gated on finalMailbox rather than heroldMailbox (#300): a spam
		// verdict can route a message that was folder-mapped to INBOX into
		// Junk instead (resolveImportSpamTarget), and Junk mail is never
		// categorised, mirroring protosmtp's classification.Verdict !=
		// spam.Spam gate.
		if isNewMember && categorise && strings.EqualFold(finalMailbox, "INBOX") {
			if catErr := w.opts.categoriser.Categorise(ctx, fmt.Sprint(account.PrincipalID), fmt.Sprint(msgID), heroldMailbox); catErr != nil {
				w.opts.log.Warn("imapimport: categorise failed (non-fatal)",
					slog.String("account_id", account.ID),
					slog.Uint64("msg_id", uint64(msgID)),
					slog.String("error", catErr.Error()),
				)
			}
		}
	}
	return countNew, lowestUID, minFailedUID, nil
}

// ingestMessage inserts fm into the herold store. Returns (isNew,
// isNewMember, heroldMessageID, heroldMailboxID, finalMailbox, error).
// isNew is false when the message already existed (dedup hit). isNewMember
// is true when the message was just placed into heroldMailbox for the first
// time — either as a fresh insert (isNew=true) or as a dedup hit where
// AddMessageToMailbox created the membership now (isNew=false). isNewMember
// is false when the message was already a member of heroldMailbox before
// this call (e.g. a second sync pass of the same folder). Callers use
// isNewMember, not isNew, to decide whether to run the categoriser so that
// categorisation happens regardless of which upstream folder is synced
// first (re #27). finalMailbox is the herold mailbox the message actually
// landed in: normally heroldMailbox verbatim, except when spam
// classification (below) redirected an INBOX-mapped fresh insert to Junk.
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
//
// liveArrival mirrors the categorise flag fetchAndIngest already carries:
// true only for genuine live arrivals on an already-initialised folder,
// false across the initial/historical backfill and the lowered-horizon
// re-scan. Spam classification (spam.go) is gated on it exactly like LLM
// categorisation (REQ-IMAP-IMP-31 / D1) and only ever runs on a fresh
// insert mapped to INBOX -- a dedup hit was already classified, if at all,
// the first time it was mirrored or delivered.
func (w *accountWorker) ingestMessage(
	ctx context.Context,
	fm fetchedMessage,
	upstreamFolder, heroldMailbox string,
	liveArrival bool,
) (isNew bool, isNewMember bool, msgID store.MessageID, mbID store.MailboxID, finalMailbox string, retErr error) {
	account := w.opts.account
	principalID := store.PrincipalID(account.PrincipalID)

	// Parse the message for the envelope. Lenient boundary/charset handling
	// (re #279): a missing closing --boundary-- marker or an undecodable
	// declared charset must not reject the whole message at ingest — the
	// render, index, search-snippet and push paths already tolerate both
	// (mailparse.NewLenientParseOptions, re #285), so a message that
	// displays and indexes correctly must also be mirrorable. This ingest
	// path keeps StrictBase64/StrictQP at their strict defaults (narrower
	// than NewLenientParseOptions) rather than adopting the render/index
	// leniency wholesale — a decision about accepting not-yet-mirrored
	// mail is out of scope for #285. Genuine structural limits
	// (MaxSize/MaxDepth/MaxParts/malformed headers) still fail hard.
	opts := mailparse.NewParseOptions()
	opts.StrictBoundary = false
	opts.StrictCharset = false
	msg, parseErr := mailparse.Parse(bytes.NewReader(fm.RFC822), opts)
	if parseErr != nil {
		return false, false, 0, 0, "", fmt.Errorf("mailparse.Parse: %w", parseErr)
	}

	// Dedup by Message-ID (primary, REQ-IMAP-IMP-30).
	rawMsgID := msg.Envelope.MessageID
	if rawMsgID != "" {
		normID := mailparse.NormalizeMessageID(rawMsgID)
		existing, lookupErr := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, normID)
		if lookupErr == nil {
			newMember, eid, embID, placeErr := w.placeExistingMessage(ctx, principalID, existing, heroldMailbox)
			return false, newMember, eid, embID, heroldMailbox, placeErr
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			return false, false, 0, 0, "", fmt.Errorf("imapimport: GetMessageByMessageIDHeader: %w", lookupErr)
		}
		// ErrNotFound -> proceed with insert.
	}

	// Store blob (idempotent). The content-addressed hash doubles as the
	// fallback dedup key for messages that have no usable Message-ID.
	blobRef, putErr := w.opts.store.Blobs().Put(ctx, bytes.NewReader(fm.RFC822))
	if putErr != nil {
		return false, false, 0, 0, "", fmt.Errorf("imapimport: Blobs.Put: %w", putErr)
	}

	// Dedup by blob_hash (fallback) when there is no Message-ID, matching the
	// bulk importer's content-hash dedup (REQ-IMAP-IMP-30). Without this a
	// no-Message-ID message re-fetched on a later pass would insert a fresh
	// messages row every time.
	if rawMsgID == "" {
		existing, lookupErr := w.opts.store.Meta().GetMessageByBlobHash(ctx, principalID, blobRef.Hash)
		if lookupErr == nil {
			newMember, eid, embID, placeErr := w.placeExistingMessage(ctx, principalID, existing, heroldMailbox)
			return false, newMember, eid, embID, heroldMailbox, placeErr
		}
		if !errors.Is(lookupErr, store.ErrNotFound) {
			return false, false, 0, 0, "", fmt.Errorf("imapimport: GetMessageByBlobHash: %w", lookupErr)
		}
		// ErrNotFound -> proceed with insert.
	}

	// Spam classification (REQ-FILT-02, issue #300): only for a fresh
	// insert, mapped to INBOX by the folder mapping, on a genuine live
	// arrival. A message the source already filed in its own Junk-
	// attributed folder never reaches this point with heroldMailbox ==
	// "INBOX", so it is never classified. The verdict picks the effective
	// target mailbox before InsertMessage ever runs: spam is routed to
	// Junk instead of INBOX; suspect stays in INBOX and gains the "$Junk"
	// keyword; ham/unclassified stays in INBOX -- the same mapping SMTP
	// delivery's resolveSieveTargets applies to its ImplicitKeep default.
	effectiveMailbox := heroldMailbox
	var spamKeywords []string
	var classification spam.Classification
	classified := false
	if liveArrival && w.opts.spamClassifier != nil && strings.EqualFold(heroldMailbox, "INBOX") {
		classification = w.opts.spamClassifier.Classify(ctx, msg)
		classified = true
		spamTarget := resolveImportSpamTarget(classification.Verdict)
		effectiveMailbox = spamTarget.mailbox
		spamKeywords = spamTarget.keywords
	}

	// Ensure the target herold mailbox exists.
	mb, mbErr := w.ensureMailbox(ctx, principalID, effectiveMailbox)
	if mbErr != nil {
		return false, false, 0, 0, "", fmt.Errorf("imapimport: ensureMailbox %q: %w", effectiveMailbox, mbErr)
	}

	// re #143 / #300: record the delivery disposition the same way the
	// SMTP ingest path does (protosmtp/deliver.go) -- derived once from
	// the resolved target mailbox's Junk special-use attribute, never
	// recomputed later from live mailbox membership.
	disposition := store.DeliveryDispositionInbox
	if mb.Attributes&store.MailboxAttrJunk != 0 {
		disposition = store.DeliveryDispositionJunk
	}

	// Build the store.Message. InternalDate and ReceivedAt are both set
	// to the upstream INTERNALDATE to preserve chronological ordering
	// (REQ-IMAP-IMP-32 byte-fidelity). ThreadID is left 0; threading
	// is handled store-side via InsertMessage's reference-chain walk.
	storeMsg := store.Message{
		PrincipalID:         principalID,
		Size:                int64(len(fm.RFC822)),
		Blob:                blobRef,
		InternalDate:        fm.InternalDate,
		ReceivedAt:          fm.InternalDate,
		Envelope:            envelopeFromParsed(msg),
		DeliveryDisposition: disposition,
	}

	flags := storeFlagsFromIMAP(fm.Flags)
	target := store.MessageMailbox{
		MailboxID: mb.ID,
		Flags:     flags,
		Keywords:  spamKeywords,
	}

	_, _, insertErr := w.opts.store.Meta().InsertMessage(ctx, storeMsg, []store.MessageMailbox{target})
	if insertErr != nil {
		return false, false, 0, 0, "", fmt.Errorf("imapimport: InsertMessage: %w", insertErr)
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

	// Persist the spam-classification transparency record (REQ-FILT-66),
	// mirroring protosmtp's persistLLMRecord. Fire-and-forget: RecordVerdict
	// never blocks or fails the import.
	if classified {
		w.opts.spamClassifier.RecordVerdict(ctx, principalID, assignedMsgID, msg, classification)
	}

	// Fresh insert: the message is a new member of effectiveMailbox.
	return true, true, assignedMsgID, mb.ID, effectiveMailbox, nil
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

	// Junk-wins precedence (re #303): an imported message never carries both
	// a Junk-attributed membership and an INBOX membership, regardless of
	// which source folder is synced first. The INBOX-suppression check runs
	// before any membership is touched; the INBOX-stripping side effect
	// (below) runs only after the new Junk membership is confirmed in
	// place, so the message always has at least one membership — stripping
	// first would let RemoveMessageFromMailbox's "last membership gone"
	// contract destroy the message out from under the pending Junk add.
	if targetMB.Attributes&store.MailboxAttrInbox != 0 {
		attrs := w.mailboxAttrByID(ctx, principalID)
		for _, mm := range existing.Mailboxes {
			if attrs[mm.MailboxID]&store.MailboxAttrJunk != 0 {
				return false, existing.ID, targetMB.ID, errINBOXSuppressedByJunk
			}
		}
	}

	alreadyMember := false
	for _, mm := range existing.Mailboxes {
		if mm.MailboxID == targetMB.ID {
			alreadyMember = true
			break
		}
	}
	isNewMember = false
	if !alreadyMember {
		// Add the membership. Idempotent per AddMessageToMailbox's ErrConflict
		// guard — a concurrent ingest of the same message into the same
		// mailbox loses the race harmlessly.
		if _, _, addErr := w.opts.store.Meta().AddMessageToMailbox(ctx, existing.ID, targetMB.ID); addErr != nil {
			if !errors.Is(addErr, store.ErrConflict) {
				return false, existing.ID, targetMB.ID, fmt.Errorf("imapimport: AddMessageToMailbox (dedup): %w", addErr)
			}
			// ErrConflict: another path already added it; not a new member here.
		} else {
			isNewMember = true
		}
	}

	if targetMB.Attributes&store.MailboxAttrJunk != 0 {
		// Placing into Junk: strip any INBOX membership this message already
		// carries from an earlier sync pass. The Junk membership added above
		// (or already present) guarantees the message is never left with
		// zero memberships by this step.
		w.stripInboxMembershipsForJunk(ctx, principalID, existing)
	}

	// Membership created now (or already in place): the caller decides
	// whether to categorise based on isNewMember and heroldMailbox == INBOX.
	return isNewMember, existing.ID, targetMB.ID, nil
}

// errINBOXSuppressedByJunk is returned by placeExistingMessage when an
// INBOX-mapped folder placement is suppressed because the message already
// carries a Junk-attributed membership (re #303). Not a failure: the caller
// treats it as a benign skip and records no message_state row for the
// (folder, uid) that would have produced the suppressed membership.
var errINBOXSuppressedByJunk = errors.New("imapimport: inbox membership suppressed by junk precedence")

// mailboxAttrByID returns a MailboxID -> Attributes map for every mailbox
// owned by pid, for callers that need to classify several
// Message.Mailboxes entries in one pass without a per-membership store
// round-trip. Returns nil (all lookups miss) on a store error; callers
// treat a miss as "no special-use bits".
func (w *accountWorker) mailboxAttrByID(ctx context.Context, pid store.PrincipalID) map[store.MailboxID]store.MailboxAttributes {
	mbs, err := w.opts.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return nil
	}
	out := make(map[store.MailboxID]store.MailboxAttributes, len(mbs))
	for _, mb := range mbs {
		out[mb.ID] = mb.Attributes
	}
	return out
}

// stripInboxMembershipsForJunk removes every INBOX-attributed membership
// `existing` currently carries and the message_state row(s) that recorded
// it, so a message just placed into a Junk-attributed mailbox never keeps
// an INBOX membership from an earlier sync pass (re #303, junk-wins).
func (w *accountWorker) stripInboxMembershipsForJunk(ctx context.Context, pid store.PrincipalID, existing store.Message) {
	attrs := w.mailboxAttrByID(ctx, pid)
	for _, mm := range existing.Mailboxes {
		if attrs[mm.MailboxID]&store.MailboxAttrInbox == 0 {
			continue
		}
		if rerr := w.opts.store.Meta().RemoveMessageFromMailbox(ctx, existing.ID, mm.MailboxID); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
			w.opts.log.Warn("imapimport: failed to remove inbox membership (junk precedence)",
				slog.String("account_id", w.opts.account.ID),
				slog.Uint64("msg_id", uint64(existing.ID)),
				slog.String("error", rerr.Error()),
			)
			continue
		}
		// Drop this account's stale message_state row(s) that pointed at the
		// membership just removed, so a later flag-sync pass does not try to
		// address a (message, mailbox) pair that no longer exists.
		states, serr := w.opts.store.Meta().ListIMAPImportMessageStatesByMessage(ctx, existing.ID)
		if serr != nil {
			continue
		}
		for _, s := range states {
			if s.AccountID == w.opts.account.ID && s.HeroldMailboxID == mm.MailboxID {
				_ = w.opts.store.Meta().DeleteIMAPImportMessageState(ctx, s.AccountID, s.UpstreamFolder, s.UpstreamUID)
			}
		}
	}
}

// removeMessageStateMembership removes the mailbox membership ms recorded
// and deletes the message_state row itself. Used when the source folder no
// longer claims the message: an upstream \Deleted flag or an upstream
// EXPUNGE (re #303). If the message has no other mailbox membership,
// RemoveMessageFromMailbox's contract destroys it — herold's normal removal
// path for a message with no mailbox.
func (w *accountWorker) removeMessageStateMembership(ctx context.Context, ms store.IMAPImportMessageState) {
	if rerr := w.opts.store.Meta().RemoveMessageFromMailbox(ctx, ms.HeroldMessageID, ms.HeroldMailboxID); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
		w.opts.log.Warn("imapimport: failed to remove mailbox membership",
			slog.String("account_id", ms.AccountID),
			slog.String("upstream_folder", ms.UpstreamFolder),
			slog.Uint64("uid", uint64(ms.UpstreamUID)),
			slog.String("error", rerr.Error()),
		)
	}
	if derr := w.opts.store.Meta().DeleteIMAPImportMessageState(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
		w.opts.log.Warn("imapimport: failed to delete message_state row",
			slog.String("account_id", ms.AccountID),
			slog.String("upstream_folder", ms.UpstreamFolder),
			slog.Uint64("uid", uint64(ms.UpstreamUID)),
			slog.String("error", derr.Error()),
		)
	}
}

// reconcileExpungedMessages detects upstream expunges for upstreamFolder: a
// message_state row whose UpstreamUID is absent from currentUIDs (the
// folder's just-observed full UID set) was expunged upstream since the last
// sync. Each such message loses the membership and state row this account
// recorded for that folder, mirroring the expunge (re #303). Best-effort
// per row; logs and continues past individual failures.
func (w *accountWorker) reconcileExpungedMessages(ctx context.Context, upstreamFolder string, currentUIDs []imap.UID) error {
	account := w.opts.account
	states, err := w.opts.store.Meta().ListIMAPImportMessageStatesByFolder(ctx, account.ID, upstreamFolder)
	if err != nil {
		return fmt.Errorf("imapimport: ListIMAPImportMessageStatesByFolder: %w", err)
	}
	if len(states) == 0 {
		return nil
	}
	present := make(map[uint32]bool, len(currentUIDs))
	for _, uid := range currentUIDs {
		present[uint32(uid)] = true
	}
	for _, s := range states {
		if present[s.UpstreamUID] {
			continue
		}
		w.removeMessageStateMembership(ctx, s)
	}
	return nil
}

// excludeFolderCleanup removes any state a previously-synced folder left
// behind once it becomes excluded (re #305): every message_state row this
// account holds for upstreamFolder, and the mailbox membership each row
// produced, are removed through removeMessageStateMembership -- the same
// store-level removal path afe08c39 uses for an upstream expunge, so a
// message with another membership survives and one with none is destroyed
// by RemoveMessageFromMailbox's normal "last membership gone" contract. The
// folder's cursor row is dropped too, so a later un-exclusion starts a fresh
// initial sync (folderInitialised=false) rather than resuming a stale
// high-water mark. Called on every pass an excluded folder is seen upstream;
// once cleaned up, ListIMAPImportMessageStatesByFolder returns empty and this
// is a cheap no-op. Best-effort: a failure is logged and does not abort the
// pass.
func (w *accountWorker) excludeFolderCleanup(ctx context.Context, upstreamFolder string) {
	account := w.opts.account
	states, err := w.opts.store.Meta().ListIMAPImportMessageStatesByFolder(ctx, account.ID, upstreamFolder)
	if err != nil {
		w.opts.log.Warn("imapimport: excluded-folder cleanup: ListIMAPImportMessageStatesByFolder failed",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}
	for _, s := range states {
		w.removeMessageStateMembership(ctx, s)
	}
	if err := w.opts.store.Meta().DeleteIMAPImportFolderCursor(ctx, account.ID, upstreamFolder); err != nil && !errors.Is(err, store.ErrNotFound) {
		w.opts.log.Warn("imapimport: excluded-folder cleanup: DeleteIMAPImportFolderCursor failed",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
			slog.String("error", err.Error()),
		)
	}
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

// hasFlag reports whether flags contains the given IMAP system flag.
func hasFlag(flags []imap.Flag, target imap.Flag) bool {
	for _, f := range flags {
		if f == target {
			return true
		}
	}
	return false
}
