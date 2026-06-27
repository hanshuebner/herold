package imapimport

// gmail_labels.go implements true per-message Gmail label placement via the
// X-GM-EXT-1 "X-GM-LABELS" FETCH data item (REQ-IMAP-IMP-53).
//
// This SUPERSEDES the folder-based interim placement (gmail.go,
// REQ-IMAP-IMP-50/51) for Gmail accounts: instead of syncing every per-label
// IMAP folder and then body-fetching [Gmail]/All Mail separately, the worker
// makes a single pass over [Gmail]/All Mail, fetches each message's body
// together with its X-GM-LABELS set, and derives the herold mailbox
// memberships directly from that label set. A message with K placement-bearing
// labels lands in K herold mailboxes (via the multi-mailbox-on-dedup path in
// ingestMessage), and archived/unlabeled mail lands in "Archive".
//
// Folder-based placement is retained as the fallback: the general per-folder
// loop (syncAllFolders, sync.go) for any server that does NOT advertise
// X-GM-EXT-1, and the Gmail folder-based pass (syncAllFoldersGmail, gmail.go)
// for the corner case of an X-GM-EXT-1 server with no [Gmail]/All Mail folder.
//
// Label -> mailbox mapping (REQ-IMAP-IMP-50):
//   - \Inbox            -> INBOX
//   - \Sent             -> Sent      (honouring the folder-map override)
//   - \Draft            -> Drafts
//   - \Spam             -> Junk
//   - \Trash            -> Trash
//   - \Important, \Starred, \Muted, \Chats, and any other system label
//     -> no mailbox (these are flag/virtual; \Starred maps to \Flagged, which
//     is carried by FLAGS, not by placement)
//   - user labels       -> same-name herold mailbox (slashes preserved)
//   - no placement label -> "Archive" (archived/unlabeled mail)

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// gmailSystemLabelFolder maps an X-GM-LABELS system-label token (lower-cased,
// without the leading backslash) to the Gmail IMAP folder-equivalent name. The
// folder-equivalent is then run through gmailHeroldMailboxName so the same
// folder-mapping precedence (per-account override > operator default > system
// table) applies uniformly to label- and folder-based placement.
//
// System labels NOT in this map (\Important, \Starred, \Muted, \Chats, and any
// future/category label) carry no mailbox placement.
var gmailSystemLabelFolder = map[string]string{
	"inbox":  "INBOX",
	"sent":   "[Gmail]/Sent Mail",
	"draft":  "[Gmail]/Drafts",
	"drafts": "[Gmail]/Drafts",
	"spam":   "[Gmail]/Spam",
	"junk":   "[Gmail]/Spam",
	"trash":  "[Gmail]/Trash",
}

// gmailLabelSetToMailboxNames converts a message's X-GM-LABELS set into the
// ordered, de-duplicated list of herold mailbox names it should be placed in
// (REQ-IMAP-IMP-53/50). Returns nil when no label carries a placement (the
// caller then defaults the message to "Archive").
//
// labels carries the raw tokens from the upstream: system labels are
// backslash-prefixed (\Inbox, \Sent, ...) and matched case-insensitively; user
// labels are verbatim names. userMapping/defaultMapping are the per-account and
// operator folder-map overrides (REQ-IMAP-IMP-10/11).
func gmailLabelSetToMailboxNames(labels []string, userMapping, defaultMapping map[string]string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, label := range labels {
		if strings.HasPrefix(label, "\\") {
			// System label: map the known placement-bearing ones; ignore the
			// rest (\Important, \Starred, \Muted, \Chats, categories, ...).
			key := strings.ToLower(strings.TrimPrefix(label, "\\"))
			folder, ok := gmailSystemLabelFolder[key]
			if !ok {
				continue
			}
			add(gmailHeroldMailboxName(folder, userMapping, defaultMapping))
			continue
		}
		// User label -> same-name herold mailbox (folder-map override applies).
		add(gmailHeroldMailboxName(label, userMapping, defaultMapping))
	}
	return out
}

// syncAllFoldersGmailLabels is the X-GM-LABELS variant of the Gmail sync path
// (REQ-IMAP-IMP-53). It makes a single pass over [Gmail]/All Mail, fetching each
// message's body together with its label set and placing it by labels.
//
// Fallback: when the upstream advertises X-GM-EXT-1 but does not expose a
// [Gmail]/All Mail folder (e.g. isGmailServer matched on the host fallback),
// there is no single canonical source to read labels from, so the worker falls
// back to folder-based placement (syncAllFoldersGmail).
func (w *accountWorker) syncAllFoldersGmailLabels(ctx context.Context, conn Conn, folders []folderInfo) error {
	account := w.opts.account
	log := w.opts.log

	allMailPresent := false
	for _, fi := range folders {
		if fi.Name == gmailAllMail {
			allMailPresent = true
			break
		}
	}
	if !allMailPresent {
		log.Info("imapimport: Gmail X-GM-LABELS path unavailable (no All Mail folder); falling back to folder-based placement",
			slog.String("account_id", account.ID),
		)
		return w.syncAllFoldersGmail(ctx, conn, folders)
	}

	// Folder-map overrides (REQ-IMAP-IMP-10/11), applied per label in
	// gmailLabelSetToMailboxNames via gmailHeroldMailboxName.
	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderMap: %w", err)
	}
	userMapping := make(map[string]string, len(folderMap))
	for _, e := range folderMap {
		userMapping[e.UpstreamFolder] = e.HeroldMailboxName
	}
	defaultMapping := w.opts.cfg.IMAPImportDefaultFolderMapFor(account.Host)

	// Reset the per-pass backfill-remaining accumulator (REQ-IMAP-IMP-63 / D6).
	w.backfillRemaining = 0

	w.status.setSyncingFolder(gmailAllMail)
	err = w.syncGmailAllMailLabels(ctx, conn, userMapping, defaultMapping)

	observe.IMAPImportBackfillRemaining.WithLabelValues(account.ID).Set(float64(w.backfillRemaining))
	return err
}

// syncGmailAllMailLabels syncs [Gmail]/All Mail with per-message label
// placement. Cursor handling (UIDVALIDITY rollover, horizon floor, low/high
// water marks) mirrors syncFolder; the difference is the fetch (body + labels
// via UIDFetchWithLabels) and the placement (by label set, into K mailboxes).
func (w *accountWorker) syncGmailAllMailLabels(ctx context.Context, conn Conn, userMapping, defaultMapping map[string]string) error {
	account := w.opts.account
	accountID := account.ID
	log := w.opts.log

	start := w.opts.clk.Now()

	si, err := conn.Select(ctx, gmailAllMail)
	if err != nil {
		return err
	}

	cursor, found, err := w.opts.store.Meta().GetIMAPImportFolderCursor(ctx, accountID, gmailAllMail)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderCursor (All Mail labels): %w", err)
	}
	if found && cursor.UIDValidity != uint64(si.UIDValidity) {
		log.Info("imapimport: Gmail All Mail UIDVALIDITY rollover; resetting cursor",
			slog.String("account_id", accountID),
			slog.Uint64("old_uidvalidity", cursor.UIDValidity),
			slog.Uint64("new_uidvalidity", uint64(si.UIDValidity)),
		)
		cursor = store.IMAPImportFolderCursor{}
		found = false
	}
	if !found {
		cursor = store.IMAPImportFolderCursor{
			AccountID:      accountID,
			UpstreamFolder: gmailAllMail,
			UIDValidity:    uint64(si.UIDValidity),
		}
	}

	// folderInitialised gates LLM categorisation: only live arrivals on an
	// already-synced All Mail categorise; the initial/historical backfill and a
	// post-rollover re-sync do not (REQ-IMAP-IMP-31 / D1).
	folderInitialised := found

	var inHorizonUIDs []imap.UID
	totalNew := 0

	if si.NumMessages == 0 {
		goto persistCursor
	}

	{
		var floor time.Time
		if account.BackfillFloorDate != nil {
			floor = *account.BackfillFloorDate
		}
		allUIDs, searchErr := conn.UIDSearchSince(ctx, floor)
		if searchErr != nil {
			return fmt.Errorf("imapimport: Gmail All Mail labels search: %w", searchErr)
		}
		inHorizonUIDs = allUIDs

		if cursor.HighWaterUID == 0 {
			// Initial sync (or post-rollover): place every in-horizon message by
			// its labels. Not categorised unless the folder was already
			// initialised on a prior pass.
			if len(allUIDs) > 0 {
				n, minUID, fErr := w.fetchAndIngestLabels(ctx, conn, allUIDs, userMapping, defaultMapping, folderInitialised)
				if fErr != nil {
					return fErr
				}
				totalNew += n
				if minUID > 0 {
					cursor.LowWaterUID = minUID
				}
			}
		} else {
			// Incremental: lowered-horizon backfill (below low water, not
			// categorised) + forward sync (above high water, categorised).
			if cursor.LowWaterUID > 0 {
				var belowLow []imap.UID
				for _, uid := range allUIDs {
					if uint64(uid) < cursor.LowWaterUID {
						belowLow = append(belowLow, uid)
					}
				}
				if len(belowLow) > 0 {
					n, newLow, fErr := w.fetchAndIngestLabels(ctx, conn, belowLow, userMapping, defaultMapping, false)
					if fErr != nil {
						return fErr
					}
					totalNew += n
					if newLow > 0 && newLow < cursor.LowWaterUID {
						cursor.LowWaterUID = newLow
					}
				}
			}
			var forward []imap.UID
			for _, uid := range allUIDs {
				if uint64(uid) > cursor.HighWaterUID {
					forward = append(forward, uid)
				}
			}
			if len(forward) > 0 {
				n, _, fErr := w.fetchAndIngestLabels(ctx, conn, forward, userMapping, defaultMapping, true)
				if fErr != nil {
					return fErr
				}
				totalNew += n
			}
		}
	}

persistCursor:
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

	if err := w.opts.store.Meta().UpsertIMAPImportFolderCursor(ctx, cursor); err != nil {
		return fmt.Errorf("imapimport: UpsertIMAPImportFolderCursor (All Mail labels): %w", err)
	}

	observe.IMAPImportMessagesFetchedTotal.WithLabelValues(accountID).Add(float64(totalNew))
	w.status.incFetched(int64(totalNew))
	observe.IMAPImportFetchDurationSeconds.WithLabelValues(accountID).Observe(
		w.opts.clk.Now().Sub(start).Seconds(),
	)
	w.backfillRemaining += int64(countUIDsBelow(inHorizonUIDs, cursor.LowWaterUID))

	log.Debug("imapimport: Gmail All Mail label sync complete",
		slog.String("account_id", accountID),
		slog.Int("new", totalNew),
	)
	return nil
}

// fetchAndIngestLabels body-fetches uids together with their X-GM-LABELS set and
// places each message into the herold mailbox(es) derived from its labels
// (REQ-IMAP-IMP-53). Returns (countNew, lowestUID, error). A per-message ingest
// failure is logged and skipped; it does not abort the batch.
func (w *accountWorker) fetchAndIngestLabels(
	ctx context.Context,
	conn Conn,
	toFetch []imap.UID,
	userMapping, defaultMapping map[string]string,
	categorise bool,
) (countNew int, lowestUID uint64, err error) {
	if len(toFetch) == 0 {
		return 0, 0, nil
	}

	msgs, err := conn.UIDFetchWithLabels(ctx, toFetch)
	if err != nil {
		return 0, 0, err
	}

	account := w.opts.account
	for _, lm := range msgs {
		if ctx.Err() != nil {
			return countNew, lowestUID, ctx.Err()
		}
		uid := uint64(lm.UID)
		if lowestUID == 0 || uid < lowestUID {
			lowestUID = uid
		}

		names := gmailLabelSetToMailboxNames(lm.Labels, userMapping, defaultMapping)
		if len(names) == 0 {
			// Archived / unlabeled mail: no placement-bearing label.
			names = []string{"Archive"}
		}

		var (
			primaryMsgID   store.MessageID
			primaryMbID    store.MailboxID
			anyNew         bool
			inboxNewMember bool
			ingestErr      error
		)
		for i, name := range names {
			isNew, isNewMember, msgID, mbID, e := w.ingestMessage(ctx, lm.fetchedMessage, gmailAllMail, name)
			if e != nil {
				ingestErr = e
				break
			}
			if i == 0 {
				primaryMsgID = msgID
				primaryMbID = mbID
			}
			if isNew {
				anyNew = true
			}
			if isNewMember && strings.EqualFold(name, "INBOX") {
				inboxNewMember = true
			}
		}
		if ingestErr != nil {
			w.opts.log.Warn("imapimport: Gmail label ingest failed",
				slog.String("account_id", account.ID),
				slog.Uint64("uid", uid),
				slog.String("error", ingestErr.Error()),
			)
			continue
		}
		if anyNew {
			countNew++
		}

		// Record import state on the All Mail folder so write-back can address
		// this upstream UID (REQ-IMAP-IMP-34). The primary (first) mailbox is
		// recorded as the herold anchor.
		sf := syncedFlagsFromIMAP(lm.Flags)
		if msErr := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
			AccountID:       account.ID,
			UpstreamFolder:  gmailAllMail,
			UpstreamUID:     uint32(lm.UID),
			HeroldMessageID: primaryMsgID,
			HeroldMailboxID: primaryMbID,
			LastSyncedFlags: sf,
		}); msErr != nil {
			w.opts.log.Warn("imapimport: Gmail label UpsertIMAPImportMessageState failed",
				slog.String("account_id", account.ID),
				slog.Uint64("uid", uid),
				slog.String("error", msErr.Error()),
			)
		}

		// Categorise new INBOX-mapped live arrivals (REQ-IMAP-IMP-31 / D1).
		if categorise && inboxNewMember {
			if catErr := w.opts.categoriser.Categorise(ctx, fmt.Sprint(account.PrincipalID), fmt.Sprint(primaryMsgID), "INBOX"); catErr != nil {
				w.opts.log.Warn("imapimport: categorise failed (non-fatal)",
					slog.String("account_id", account.ID),
					slog.Uint64("msg_id", uint64(primaryMsgID)),
					slog.String("error", catErr.Error()),
				)
			}
		}
	}
	return countNew, lowestUID, nil
}
