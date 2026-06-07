package imapimport

// gmail.go implements the Gmail All-Mail optimization (REQ-IMAP-IMP-50/51).
//
// # Detection
//
// An upstream is treated as Gmail when BOTH of the following hold:
//   - The server advertises the X-GM-EXT-1 capability after login.
//   - The folder list contains "[Gmail]/All Mail" (English name; Gmail always
//     uses English for IMAP folder names regardless of UI locale).
//
// The host check (imap.gmail.com) is a secondary convenience used only when
// the capability is present but the folder list is empty. All Gmail-specific
// behaviour is gated on isGmailServer; the normal (non-Gmail) path is
// completely unaffected.
//
// # Folder skip policy (REQ-IMAP-IMP-50)
//
// When the Gmail path is active, syncAllFolders uses [Gmail]/All Mail as the
// sole body source and skips every folder whose messages are a subset of All
// Mail. Concretely:
//
//   - Skipped (subsumed by All Mail): Inbox, [Gmail]/All Mail (handled
//     separately as body source), [Gmail]/Sent Mail, [Gmail]/Important,
//     [Gmail]/Starred, [Gmail]/Chats, [Gmail]/Inbox.
//   - NOT skipped (sync normally): [Gmail]/Drafts, [Gmail]/Spam,
//     [Gmail]/Trash. These are NOT in All Mail.
//
// This policy is intentionally conservative: when in doubt about a folder's
// relationship to All Mail it is synced, not skipped. Dedup handles
// duplicates harmlessly.
//
// # Label-to-mailbox mapping (REQ-IMAP-IMP-51) and its IMAP limitation
//
// IMPORTANT: over IMAP, Gmail does NOT expose per-message labels as an
// X-Gmail-Labels: header in the BODY[] bytes. That header is a Google
// Takeout / Vault *export* artifact only; the message FETCHed over IMAP is
// the original RFC822 and carries no label header. Over IMAP, labels are
// available ONLY via the X-GM-LABELS FETCH data item (part of X-GM-EXT-1) --
// and go-imap/v2 v2.0.0-beta.8 has no typed support for it: the FETCH
// response parser errors on any unrecognised msg-att name (handleFetch's
// default case in imapclient/fetch.go), so requesting X-GM-LABELS would
// break the connection.
//
// Consequence for real Gmail-over-IMAP today: extractXGmailLabels finds no
// header, no labels resolve, and every All Mail message falls back to INBOX.
// The genuine win of this path is still realised -- All Mail is the single
// canonical body source and the per-label folders are skipped, so each body
// is fetched ONCE instead of once per label (REQ-IMAP-IMP-50). True
// per-label *placement* (REQ-IMAP-IMP-51) is DEFERRED until X-GM-LABELS can
// be fetched (a newer go-imap, or a custom FETCH item).
//
// The X-Gmail-Labels header parser below is retained as harmless best-effort:
// it activates for any source that DOES include the header (e.g. a provider
// that mirrors Takeout semantics), but for stock Gmail IMAP it is a no-op and
// the INBOX fallback applies. Tokens, when present, are mapped through the
// locale-aware table in internal/import/gmail (ParseGmailLabels / Map);
// locale detection runs across the first batch. A message is placed into the
// primary role mailbox (Inbox/Sent/Drafts/Spam/Trash) plus any user labels,
// or INBOX when nothing resolves.
//
// # What is NOT tested (requires a real Gmail connection)
//
// The in-process imapmemserver does not implement X-GM-EXT-1 or the
// [Gmail]/All Mail folder semantics. Therefore:
//   - The Gmail sync path (syncAllFoldersGmail, syncFolderGmailAllMail) is
//     not exercised by the automated test suite.
//   - The folder-skip decision for live [Gmail]/* folders is not tested
//     end-to-end.
//
// A real-Gmail integration test would need:
//   - An OAuth2 app-password or xoauth2 token for a test Gmail account.
//   - [Gmail]/All Mail with messages carrying X-Gmail-Labels headers in at
//     least two locales.
//   - A [Gmail]/Drafts folder with messages absent from All Mail.
//   - Assertions: [Gmail]/Inbox NOT synced; [Gmail]/Drafts IS synced;
//     multi-label messages land in the correct herold mailboxes.
//
// What IS unit-tested in gmail_test.go:
//   - isGmailServer: capability-set + folder-list combinations.
//   - shouldSkipGmailFolder: every interesting folder name.
//   - gmailLabelsToMailboxNames: label sets in multiple locales.
//   - extractXGmailLabels: header extraction from raw RFC822 bytes.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"

	gmaillabels "github.com/hanshuebner/herold/internal/import/gmail"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// capGmailExt is the capability name advertised by Gmail's IMAP extension.
// It is not a constant in go-imap/v2, so we define it locally.
const capGmailExt = imap.Cap("X-GM-EXT-1")

// gmailAllMail is the invariant English IMAP name of Gmail's All Mail folder.
const gmailAllMail = "[Gmail]/All Mail"

// gmailFoldersToSkip is the set of Gmail virtual folders that are fully
// subsumed by [Gmail]/All Mail. Keys are the exact IMAP folder names Gmail
// uses (always English, regardless of UI locale).
//
// Skipped (true = skip during Gmail sync):
//   - [Gmail]/All Mail  — the body source; handled separately
//   - [Gmail]/Sent Mail — All Mail contains sent messages
//   - [Gmail]/Important — virtual label; subsumed
//   - [Gmail]/Starred   — virtual label; subsumed
//   - Inbox             — every inbox message is also in All Mail
//   - [Gmail]/Inbox     — some accounts expose it under [Gmail]/ too
//   - [Gmail]/Chats     — chat history; not RFC822 mail
//
// NOT in the map (sync normally):
//   - [Gmail]/Drafts — NOT in All Mail
//   - [Gmail]/Spam   — NOT in All Mail
//   - [Gmail]/Trash  — NOT in All Mail
var gmailFoldersToSkip = map[string]bool{
	gmailAllMail:        true,
	"[Gmail]/Sent Mail": true,
	"[Gmail]/Important": true,
	"[Gmail]/Starred":   true,
	"Inbox":             true,
	"[Gmail]/Inbox":     true,
	"[Gmail]/Chats":     true,
}

// isGmailServer returns true when the connected IMAP server is Gmail.
//
// Detection requires BOTH:
//  1. The server advertised the X-GM-EXT-1 capability after login.
//  2. The folder list contains [Gmail]/All Mail.
//
// The host check (imap.gmail.com suffix) is used as a fallback when
// the capability is present but the folder list is empty.
func isGmailServer(caps imap.CapSet, folders []folderInfo, host string) bool {
	if !caps.Has(capGmailExt) {
		return false
	}
	for _, f := range folders {
		if f.Name == gmailAllMail {
			return true
		}
	}
	// Fallback: capability + known Gmail host, no All Mail folder visible yet.
	return strings.HasSuffix(strings.ToLower(host), "imap.gmail.com")
}

// shouldSkipGmailFolder reports whether a folder should be skipped during a
// Gmail sync pass (its messages are present in [Gmail]/All Mail and will be
// fetched from there). Returns true = skip; false = sync normally.
//
// The canonical All Mail folder itself is also returned as "skip" because
// syncAllFoldersGmail processes it via a dedicated code path.
func shouldSkipGmailFolder(folderName string) bool {
	return gmailFoldersToSkip[folderName]
}

// gmailLabelsToMailboxNames converts a raw X-Gmail-Labels header value to
// the set of herold mailbox names the message should be placed in.
//
// locale is the account's detected locale (see gmaillabels.DetectLocale).
// An empty locale falls back to "en".
//
// Returns nil when the header is empty or no tokens map to a mailbox
// (caller should fall back to INBOX). May return multiple entries.
//
// Mapping rules:
//   - Inbox label    -> "INBOX"
//   - Sent label     -> "Sent"
//   - Drafts label   -> "Drafts"
//   - Spam label     -> "Junk"
//   - Trash label    -> "Trash"
//   - User labels    -> label name verbatim (slashes preserved for hierarchy)
//   - Starred / Important / Unread / Opened / category -> no mailbox (flags only)
func gmailLabelsToMailboxNames(labelsHeader string, locale gmaillabels.Locale) []string {
	if locale == "" {
		locale = "en"
	}
	tokens := gmaillabels.ParseGmailLabels(labelsHeader)
	if len(tokens) == 0 {
		return nil
	}
	decision := gmaillabels.Map(tokens, locale)

	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	for _, role := range decision.Roles {
		switch role {
		case gmaillabels.SystemLabelInbox:
			add("INBOX")
		case gmaillabels.SystemLabelSent:
			add("Sent")
		case gmaillabels.SystemLabelDrafts:
			add("Drafts")
		case gmaillabels.SystemLabelSpam:
			add("Junk")
		case gmaillabels.SystemLabelTrash:
			add("Trash")
		}
	}
	for _, ul := range decision.UserLabels {
		if ul != "" && ul != "Chats" {
			add(ul)
		}
	}
	return out
}

// extractXGmailLabels extracts the raw value of the X-Gmail-Labels header
// from RFC822 bytes. Returns "" when the header is absent.
//
// Only the header block (before the first blank line) is scanned; the
// message body is never read, keeping this function cheap.
//
// Folded headers (continuation lines starting with whitespace) are supported,
// though Gmail in practice emits X-Gmail-Labels on a single line.
func extractXGmailLabels(rfc822 []byte) string {
	// Find end of header block: CRLF CRLF or LF LF.
	headerEnd := bytes.Index(rfc822, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		headerEnd = bytes.Index(rfc822, []byte("\n\n"))
		if headerEnd < 0 {
			headerEnd = len(rfc822)
		}
	}
	header := rfc822[:headerEnd]

	target := []byte("x-gmail-labels:")
	lines := bytes.Split(header, []byte("\n"))
	var value strings.Builder
	capturing := false
	for _, raw := range lines {
		line := bytes.TrimRight(raw, "\r")
		if capturing {
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				value.WriteByte(' ')
				value.Write(bytes.TrimLeft(line, " \t"))
				continue
			}
			break
		}
		if len(line) >= len(target) && bytes.EqualFold(line[:len(target)], target) {
			val := line[len(target):]
			value.Write(bytes.TrimLeft(val, " \t"))
			capturing = true
		}
	}
	return strings.TrimSpace(value.String())
}

// syncAllFoldersGmail is the Gmail-specific variant of syncAllFolders.
//
// It:
//  1. Syncs [Gmail]/All Mail as the canonical body source with
//     label-aware mailbox placement.
//  2. Skips all per-label folders subsumed by All Mail.
//  3. Syncs [Gmail]/Drafts, [Gmail]/Spam, and [Gmail]/Trash separately.
func (w *accountWorker) syncAllFoldersGmail(ctx context.Context, conn Conn, folders []folderInfo) error {
	account := w.opts.account
	log := w.opts.log

	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderMap: %w", err)
	}
	mapping := make(map[string]string, len(folderMap))
	for _, e := range folderMap {
		mapping[e.UpstreamFolder] = e.HeroldMailboxName
	}

	var lastErr error

	// Phase 1: Sync [Gmail]/All Mail using the label-aware ingest path.
	w.status.setSyncingFolder(gmailAllMail)
	if err := w.syncFolderGmailAllMail(ctx, conn, mapping); err != nil {
		log.Warn("imapimport: Gmail All Mail sync failed",
			slog.String("account_id", account.ID),
			slog.String("error", err.Error()),
		)
		lastErr = err
	}

	// Phase 2: Sync folders NOT subsumed by All Mail.
	for _, fi := range folders {
		if hasAttr(fi.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		if shouldSkipGmailFolder(fi.Name) {
			log.Debug("imapimport: Gmail folder skipped (subsumed by All Mail)",
				slog.String("account_id", account.ID),
				slog.String("upstream_folder", fi.Name),
			)
			continue
		}
		heroldName, ok := mapping[fi.Name]
		if !ok {
			heroldName = fi.Name
		}
		w.status.setSyncingFolder(fi.Name)
		if err := w.syncFolder(ctx, conn, fi.Name, heroldName); err != nil {
			log.Warn("imapimport: Gmail folder sync failed (continuing)",
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

// syncFolderGmailAllMail syncs [Gmail]/All Mail into herold mailboxes
// derived from each message's X-Gmail-Labels header.
//
// Cursor semantics are identical to syncFolder: UIDVALIDITY, LowWaterUID,
// HighWaterUID, and HighestModSeq are tracked and persisted. The horizon
// floor (backfill) applies here too (REQ-IMAP-IMP-50).
func (w *accountWorker) syncFolderGmailAllMail(ctx context.Context, conn Conn, mapping map[string]string) error {
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
		return fmt.Errorf("imapimport: GetIMAPImportFolderCursor (All Mail): %w", err)
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

	floorDate := account.BackfillFloorDate
	backfillNewCount := 0
	forwardNewCount := 0

	// labelSamples collects parsed token lists from the first 20 messages
	// for locale detection. Gmail serves localised label values inside
	// X-Gmail-Labels so we need to detect before mapping.
	var labelSamples [][]string

	// ingestGmailBatch fetches and ingests a slice of UIDs from All Mail,
	// using X-Gmail-Labels to determine mailbox placement.
	ingestGmailBatch := func(uids []imap.UID, isForward bool) (countNew int, lowestUID uint64, retErr error) {
		if len(uids) == 0 {
			return 0, 0, nil
		}
		msgs, fetchErr := conn.UIDFetch(ctx, uids)
		if fetchErr != nil {
			return 0, 0, fetchErr
		}
		for _, fm := range msgs {
			if ctx.Err() != nil {
				return countNew, lowestUID, ctx.Err()
			}
			uid := uint64(fm.UID)
			if lowestUID == 0 || uid < lowestUID {
				lowestUID = uid
			}

			rawLabels := extractXGmailLabels(fm.RFC822)
			if rawLabels != "" && len(labelSamples) < 20 {
				if tokens := gmaillabels.ParseGmailLabels(rawLabels); len(tokens) > 0 {
					labelSamples = append(labelSamples, tokens)
				}
			}

			locale := gmaillabels.DetectLocale(labelSamples)
			mailboxNames := gmailLabelsToMailboxNames(rawLabels, locale)

			// Apply user-defined folder-map overrides.
			resolved := make([]string, 0, len(mailboxNames))
			for _, mb := range mailboxNames {
				if override, ok := mapping[mb]; ok {
					resolved = append(resolved, override)
				} else {
					resolved = append(resolved, mb)
				}
			}

			// Fallback: no labels resolved -> use INBOX.
			if len(resolved) == 0 {
				if override, ok := mapping["INBOX"]; ok {
					resolved = []string{override}
				} else {
					resolved = []string{"INBOX"}
				}
			}

			// Primary mailbox: full ingest (dedup + blob + InsertMessage).
			primaryMailbox := resolved[0]
			isNew, msgID, mbID, ingestErr := w.ingestMessage(ctx, fm, gmailAllMail, primaryMailbox)
			if ingestErr != nil {
				log.Warn("imapimport: Gmail All Mail ingest failed",
					slog.String("account_id", accountID),
					slog.Uint64("uid", uid),
					slog.String("error", ingestErr.Error()),
				)
				continue
			}
			if isNew {
				countNew++
			}

			// Additional mailboxes: the message body is already stored; we
			// only need additional MessageMailbox rows. Re-calling
			// ingestMessage handles dedup gracefully (returns isNew=false for
			// the same Message-ID). The blob refcount is already correct
			// because Blobs.Put is idempotent.
			for _, extraMB := range resolved[1:] {
				if _, _, _, extraErr := w.ingestMessage(ctx, fm, gmailAllMail, extraMB); extraErr != nil {
					log.Warn("imapimport: Gmail All Mail extra-label ingest failed",
						slog.String("account_id", accountID),
						slog.Uint64("uid", uid),
						slog.String("mailbox", extraMB),
						slog.String("error", extraErr.Error()),
					)
				}
			}

			// Persist message state (primary mailbox only; write-back
			// targets the first label-derived mailbox as canonical).
			sf := syncedFlagsFromIMAP(fm.Flags)
			if msErr := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
				AccountID:       accountID,
				UpstreamFolder:  gmailAllMail,
				UpstreamUID:     uint32(fm.UID),
				HeroldMessageID: msgID,
				HeroldMailboxID: mbID,
				LastSyncedFlags: sf,
			}); msErr != nil {
				log.Warn("imapimport: UpsertIMAPImportMessageState (All Mail) failed",
					slog.String("account_id", accountID),
					slog.Uint64("uid", uid),
					slog.String("error", msErr.Error()),
				)
			}

			// Categoriser for new, forward, INBOX-placed messages (REQ-IMAP-IMP-31).
			if isNew && isForward && strings.EqualFold(primaryMailbox, "INBOX") {
				if catErr := w.opts.categoriser.Categorise(ctx,
					fmt.Sprint(account.PrincipalID),
					fmt.Sprint(msgID),
					primaryMailbox,
				); catErr != nil {
					log.Warn("imapimport: Gmail categorise failed (non-fatal)",
						slog.String("account_id", accountID),
						slog.Uint64("msg_id", uint64(msgID)),
						slog.String("error", catErr.Error()),
					)
				}
			}
		}
		return countNew, lowestUID, nil
	}

	if si.NumMessages == 0 {
		goto persistCursor
	}

	if cursor.HighWaterUID == 0 {
		// Initial sync.
		var floor time.Time
		if floorDate != nil {
			floor = *floorDate
		}
		initialUIDs, searchErr := conn.UIDSearchSince(ctx, floor)
		if searchErr != nil {
			return fmt.Errorf("imapimport: Gmail All Mail initial search: %w", searchErr)
		}
		if len(initialUIDs) > 0 {
			n, minUID, fetchErr := ingestGmailBatch(initialUIDs, true)
			if fetchErr != nil {
				return fmt.Errorf("imapimport: Gmail All Mail initial fetch: %w", fetchErr)
			}
			forwardNewCount = n
			if minUID > 0 {
				cursor.LowWaterUID = minUID
			}
		}
	} else {
		// Incremental sync: backfill extension.
		if floorDate != nil && cursor.LowWaterUID > 0 {
			allInHorizon, searchErr := conn.UIDSearchSince(ctx, *floorDate)
			if searchErr != nil {
				return fmt.Errorf("imapimport: Gmail All Mail backfill search: %w", searchErr)
			}
			var belowLow []imap.UID
			for _, uid := range allInHorizon {
				if uint64(uid) < cursor.LowWaterUID {
					belowLow = append(belowLow, uid)
				}
			}
			if len(belowLow) > 0 {
				n, newLow, fetchErr := ingestGmailBatch(belowLow, false)
				if fetchErr != nil {
					return fmt.Errorf("imapimport: Gmail All Mail backfill fetch: %w", fetchErr)
				}
				backfillNewCount = n
				if newLow > 0 && newLow < cursor.LowWaterUID {
					cursor.LowWaterUID = newLow
				}
			}
		}
		// Forward sync: UIDs above high-water.
		forwardUIDs, err2 := uidsAbove(conn, ctx, cursor.HighWaterUID)
		if err2 != nil {
			return fmt.Errorf("imapimport: Gmail All Mail forward search: %w", err2)
		}
		if len(forwardUIDs) > 0 {
			n, _, fetchErr := ingestGmailBatch(forwardUIDs, true)
			if fetchErr != nil {
				return fmt.Errorf("imapimport: Gmail All Mail forward fetch: %w", fetchErr)
			}
			forwardNewCount = n
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
		return fmt.Errorf("imapimport: UpsertIMAPImportFolderCursor (All Mail): %w", err)
	}

	total := backfillNewCount + forwardNewCount
	observe.IMAPImportMessagesFetchedTotal.WithLabelValues(accountID).Add(float64(total))
	w.status.incFetched(int64(total))
	observe.IMAPImportFetchDurationSeconds.WithLabelValues(accountID).Observe(
		w.opts.clk.Now().Sub(start).Seconds(),
	)
	observe.IMAPImportBackfillRemaining.WithLabelValues(accountID).Set(0)

	log.Debug("imapimport: Gmail All Mail sync complete",
		slog.String("account_id", accountID),
		slog.Int("new_backfill", backfillNewCount),
		slog.Int("new_forward", forwardNewCount),
	)
	return nil
}
