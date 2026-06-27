package imapimport

// gmail.go implements the Gmail folder-based label placement
// (REQ-IMAP-IMP-50/51) and the general multi-mailbox-on-dedup mechanism.
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
// # Folder-based label placement (REQ-IMAP-IMP-50/51) — Option B
//
// Gmail exposes per-message labels as IMAP folders (one folder per label).
// X-GM-LABELS is NOT used because go-imap/v2 beta.8 cannot parse unknown
// msg-att names and would break the connection. X-Gmail-Labels is a
// Takeout/Vault export artifact and is NOT present in messages FETCHed over
// live IMAP. Therefore placement is derived from which folder each message
// lives in, not from per-message label metadata.
//
// Folder classification:
//
//   - SKIP (virtual/flag folders, no unique content):
//     [Gmail]/Important, [Gmail]/Starred, [Gmail]/Chats.
//
//   - SYNC NORMALLY as mapped folders (placement comes from here):
//     INBOX, [Gmail]/Sent Mail -> Sent, [Gmail]/Drafts -> Drafts,
//     [Gmail]/Spam -> Junk, [Gmail]/Trash -> Trash.
//     Every user-label folder maps to a same-named herold mailbox (slashes
//     preserved for hierarchy). A message appearing in several label folders
//     lands in several herold mailboxes via the multi-mailbox-on-dedup
//     mechanism in ingestMessage (sync.go).
//
//   - SPECIAL, synced LAST: [Gmail]/All Mail — envelope-first dedup.
//     For each UID in the horizon, fetch ENVELOPE (cheap) to get the
//     Message-ID WITHOUT the body. If the Message-ID is already mirrored in
//     herold (GetMessageByMessageIDHeader), SKIP it — the label folders
//     already placed it. Only for messages NOT yet mirrored, body-fetch
//     (BODY.PEEK[]) and ingest into "Archive" (archived mail with no label).
//     This avoids re-downloading every body from All Mail while still
//     catching archived-unlabeled mail.
//
// Sync order: all label folders first, then [Gmail]/All Mail last (so the
// envelope-dedup in All Mail can tell what is already placed).
//
// # Category tabs
//
// Gmail category tabs (Primary, Social, Promotions, etc.) are NOT attempted
// as IMAP folders — they are not standard IMAP folders across all Gmail
// accounts. herold's own LLM categoriser runs on imported INBOX mail
// (REQ-IMAP-IMP-31) and produces category-$FOO keywords locally; no Gmail
// category data is replicated.
//
// # What is NOT tested (requires a real Gmail connection)
//
// The in-process imapmemserver does not implement X-GM-EXT-1 or the
// [Gmail]/All Mail folder semantics. Therefore:
//   - The Gmail sync path (syncAllFoldersGmail, syncFolderGmailAllMailEnvelopeDedup)
//     is not exercised by the automated test suite.
//   - The folder-skip decision for live [Gmail]/* folders is not tested
//     end-to-end against a real Gmail account.
//
// A real-Gmail integration test would need:
//   - An OAuth2 app-password or xoauth2 token for a test Gmail account.
//   - [Gmail]/All Mail containing messages some of which have no label folders.
//   - At least two user-label folders with overlapping messages.
//   - Assertions: skipped folders ([Gmail]/Important, etc.) not synced;
//     INBOX / Sent / Drafts / Spam / Trash synced to correct herold mailboxes;
//     user-label folders create matching herold mailboxes; All Mail
//     unlabeled messages land in Archive; message in multiple labels has
//     multiple herold memberships.
//
// What IS unit-tested in gmail_test.go:
//   - isGmailServer: capability-set + folder-list combinations.
//   - shouldSkipGmailFolder / classifyGmailFolder: every interesting folder name.
//   - gmailLabelsToMailboxNames: label sets in multiple locales (retained for
//     compatibility with the X-Gmail-Labels best-effort parser).
//   - extractXGmailLabels: header extraction from raw RFC822 bytes.
//   - envelopeDedupDecision: unit test of the All Mail envelope-dedup logic.
//
// What IS integration-tested (against the in-process memory server) in
// sync_test.go / gmail_test.go:
//   - TestMultiMailboxPlacement: two folders, same Message-ID, same message
//     in both; sync -> message has membership in both mapped herold mailboxes.
//     Re-sync is idempotent (no duplicate memberships). This exercises the
//     multi-mailbox AddMessageToMailbox path directly.

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

// gmailFolderClass classifies a Gmail folder for the folder-based sync pass.
type gmailFolderClass int

const (
	// gmailFolderClassSkip: virtual/flag folder, no unique content.
	// [Gmail]/Important, [Gmail]/Starred, [Gmail]/Chats.
	gmailFolderClassSkip gmailFolderClass = iota
	// gmailFolderClassNormal: sync as a mapped herold mailbox; this is the
	// source of placement. Includes INBOX and all non-virtual, non-AllMail
	// folders (per-label folders, Sent, Drafts, Spam, Trash).
	gmailFolderClassNormal
	// gmailFolderClassAllMail: [Gmail]/All Mail, synced last with
	// envelope-first dedup to capture archived-unlabeled mail.
	gmailFolderClassAllMail
)

// gmailSystemFolderMap maps Gmail system folder names to herold mailbox names.
// User-label folders are not in this map; they map to themselves.
var gmailSystemFolderMap = map[string]string{
	"INBOX":             "INBOX",
	"[Gmail]/Sent Mail": "Sent",
	"[Gmail]/Drafts":    "Drafts",
	"[Gmail]/Spam":      "Junk",
	"[Gmail]/Trash":     "Trash",
}

// gmailSkipFolders is the set of Gmail virtual folders to skip entirely.
// These contain no unique content (everything in them is also in All Mail
// or is not RFC822 mail at all).
var gmailSkipFolders = map[string]bool{
	"[Gmail]/Important": true,
	"[Gmail]/Starred":   true,
	"[Gmail]/Chats":     true,
}

// classifyGmailFolder returns the classification for a Gmail folder name.
func classifyGmailFolder(folderName string) gmailFolderClass {
	if folderName == gmailAllMail {
		return gmailFolderClassAllMail
	}
	if gmailSkipFolders[folderName] {
		return gmailFolderClassSkip
	}
	return gmailFolderClassNormal
}

// shouldSkipGmailFolder reports whether a folder should be skipped during a
// Gmail sync pass. Returns true for virtual/flag folders.
//
// [Gmail]/All Mail is NOT returned as "skip" here — it is handled specially
// as gmailFolderClassAllMail by classifyGmailFolder; this function is kept
// for backward compat with gmail_test.go.
func shouldSkipGmailFolder(folderName string) bool {
	return gmailSkipFolders[folderName]
}

// gmailHeroldMailboxName returns the herold mailbox name for a Gmail folder.
// Precedence (REQ-IMAP-IMP-10/11): per-account override (userMapping) wins
// over the operator's system-wide default-for-host map (defaultMapping),
// which wins over the fixed Gmail system table; a user-label folder with no
// match maps to itself (preserving slashes for hierarchy).
func gmailHeroldMailboxName(folderName string, userMapping, defaultMapping map[string]string) string {
	// User-provided per-account override wins.
	if override, ok := userMapping[folderName]; ok {
		return override
	}
	// Operator system-wide default for this host.
	if def, ok := defaultMapping[folderName]; ok {
		return def
	}
	// System folder table.
	if herold, ok := gmailSystemFolderMap[folderName]; ok {
		return herold
	}
	// User label: use the folder name verbatim (slashes preserved).
	return folderName
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

// gmailLabelsToMailboxNames converts a raw X-Gmail-Labels header value to
// the set of herold mailbox names the message should be placed in.
//
// This is retained as best-effort for sources that DO include the
// X-Gmail-Labels header (e.g. a provider that mirrors Takeout semantics).
// For live Gmail IMAP it is a no-op (the header is absent from FETCHed
// messages; placement now comes from folder membership via the folder-based
// sync path).
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

// envelopeFetchResult is the result of a UIDFetchEnvelope call for one message.
// It carries just enough metadata to decide whether to body-fetch.
type envelopeFetchResult struct {
	// UID is the upstream IMAP UID.
	UID imap.UID
	// MessageID is the normalized RFC 5322 Message-ID header value (without
	// angle brackets). Empty when the message has no Message-ID header.
	MessageID string
	// InternalDate is the upstream INTERNALDATE.
	InternalDate time.Time
	// Flags is the upstream system-flag set.
	Flags []imap.Flag
}

// syncAllFoldersGmail is the Gmail-specific variant of syncAllFolders.
//
// Design (Option B — folder-based label placement):
//
//  1. Enumerate all folders; classify each as Skip / Normal / AllMail.
//  2. Phase 1 — sync all Normal folders (INBOX, Sent, Drafts, Spam, Trash,
//     user-label folders) into their mapped herold mailboxes. A message in K
//     label folders gets K herold mailbox memberships via the
//     multi-mailbox-on-dedup path in ingestMessage (sync.go). Bodies are
//     fetched once per folder; the dedup in ingestMessage ensures the blob is
//     stored once.
//  3. Phase 2 — sync [Gmail]/All Mail LAST with envelope-first dedup: for
//     each UID in the horizon, fetch ENVELOPE (no body) to get the Message-ID.
//     If that Message-ID is already mirrored (GetMessageByMessageIDHeader),
//     skip it entirely — the label folders already placed it. Only for
//     messages NOT yet mirrored (archived mail with no label folder), do a
//     full body-fetch and ingest into "Archive".
//
// Categories: herold's own LLM categoriser runs on imported INBOX mail
// (REQ-IMAP-IMP-31), producing $category-* keywords locally. Gmail category
// tabs are not replicated — they are not consistent IMAP folders across all
// accounts.
func (w *accountWorker) syncAllFoldersGmail(ctx context.Context, conn Conn, folders []folderInfo) error {
	account := w.opts.account
	log := w.opts.log

	folderMap, err := w.opts.store.Meta().GetIMAPImportFolderMap(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("imapimport: GetIMAPImportFolderMap: %w", err)
	}
	userMapping := make(map[string]string, len(folderMap))
	for _, e := range folderMap {
		userMapping[e.UpstreamFolder] = e.HeroldMailboxName
	}
	// Operator system-wide default folder map for this host (REQ-IMAP-IMP-11);
	// per-account entries above win over it (applied in gmailHeroldMailboxName).
	defaultMapping := w.opts.cfg.IMAPImportDefaultFolderMapFor(account.Host)

	// Reset the per-pass backfill-remaining accumulator; the label-folder
	// passes and the All Mail pass each add their count, and we publish the
	// account-wide sum to the gauge below (REQ-IMAP-IMP-63 / D6).
	w.backfillRemaining = 0

	var lastErr error

	// Phase 1: Sync all Normal folders (label/system folders except All Mail).
	for _, fi := range folders {
		if hasAttr(fi.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		switch classifyGmailFolder(fi.Name) {
		case gmailFolderClassSkip:
			log.Debug("imapimport: Gmail folder skipped (virtual/flag folder)",
				slog.String("account_id", account.ID),
				slog.String("upstream_folder", fi.Name),
			)
			continue
		case gmailFolderClassAllMail:
			// Deferred to Phase 2.
			continue
		case gmailFolderClassNormal:
			heroldName := gmailHeroldMailboxName(fi.Name, userMapping, defaultMapping)
			w.status.setSyncingFolder(fi.Name)
			if err := w.syncFolder(ctx, conn, fi.Name, heroldName); err != nil {
				log.Warn("imapimport: Gmail label folder sync failed (continuing)",
					slog.String("account_id", account.ID),
					slog.String("upstream_folder", fi.Name),
					slog.String("herold_mailbox", heroldName),
					slog.String("error", err.Error()),
				)
				lastErr = err
			}
		}
	}

	// Phase 2: Sync [Gmail]/All Mail last with envelope-first dedup.
	// Only messages not already placed by a label folder are body-fetched
	// and ingested into "Archive".
	allMailPresent := false
	for _, fi := range folders {
		if fi.Name == gmailAllMail {
			allMailPresent = true
			break
		}
	}
	if allMailPresent {
		w.status.setSyncingFolder(gmailAllMail)
		if err := w.syncFolderGmailAllMailEnvelopeDedup(ctx, conn); err != nil {
			log.Warn("imapimport: Gmail All Mail envelope-dedup sync failed",
				slog.String("account_id", account.ID),
				slog.String("error", err.Error()),
			)
			lastErr = err
		}
	}

	// Publish the account-wide backfill-remaining gauge (REQ-IMAP-IMP-63).
	observe.IMAPImportBackfillRemaining.WithLabelValues(account.ID).Set(float64(w.backfillRemaining))

	return lastErr
}

// syncFolderGmailAllMailEnvelopeDedup syncs [Gmail]/All Mail using an
// envelope-first dedup strategy:
//
//  1. SELECT [Gmail]/All Mail.
//  2. For each UID in the horizon, fetch ENVELOPE + UID + INTERNALDATE + FLAGS
//     (no body — cheap).
//  3. If the Message-ID is already in herold (GetMessageByMessageIDHeader),
//     skip: the label folders already placed it.
//  4. For UIDs not yet mirrored, do a full BODY.PEEK[] fetch and ingest into
//     "Archive" (mailbox with \Archive special-use).
//
// This avoids re-downloading every body from All Mail while still capturing
// archived mail that has no label folder.
func (w *accountWorker) syncFolderGmailAllMailEnvelopeDedup(ctx context.Context, conn Conn) error {
	account := w.opts.account
	accountID := account.ID
	principalID := store.PrincipalID(account.PrincipalID)
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

	if si.NumMessages == 0 {
		goto persistCursor
	}

	{
		// Collect UIDs to examine (apply horizon floor).
		var floor time.Time
		if account.BackfillFloorDate != nil {
			floor = *account.BackfillFloorDate
		}
		allUIDs, searchErr := conn.UIDSearchSince(ctx, floor)
		if searchErr != nil {
			return fmt.Errorf("imapimport: Gmail All Mail search: %w", searchErr)
		}

		// Filter to UIDs we have not seen yet in this folder (forward only for
		// All Mail; we don't separately track backfill for All Mail because the
		// label-folder pass already applies the horizon).
		var uidsToExamine []imap.UID
		for _, uid := range allUIDs {
			if cursor.HighWaterUID == 0 || uint64(uid) > cursor.HighWaterUID {
				uidsToExamine = append(uidsToExamine, uid)
			}
		}

		// Also handle lowered-horizon backfill for All Mail (below low_water).
		if account.BackfillFloorDate != nil && cursor.LowWaterUID > 0 {
			var belowLow []imap.UID
			for _, uid := range allUIDs {
				if uint64(uid) < cursor.LowWaterUID {
					belowLow = append(belowLow, uid)
				}
			}
			uidsToExamine = append(uidsToExamine, belowLow...)
		}

		if len(uidsToExamine) == 0 {
			goto persistCursor
		}

		// Fetch envelopes (no body) for the candidate UIDs.
		envelopes, envErr := conn.UIDFetchEnvelope(ctx, uidsToExamine)
		if envErr != nil {
			return fmt.Errorf("imapimport: Gmail All Mail envelope fetch: %w", envErr)
		}

		// For each envelope: if Message-ID already in herold, skip.
		// Otherwise, collect UID for full body-fetch.
		var needBodyFetch []imap.UID
		minUID := uint64(0)
		for _, env := range envelopes {
			uid := uint64(env.UID)
			if minUID == 0 || uid < minUID {
				minUID = uid
			}
			if env.MessageID == "" {
				// No Message-ID: can't dedup cheaply; body-fetch to be safe.
				needBodyFetch = append(needBodyFetch, env.UID)
				continue
			}
			existing, lookupErr := w.opts.store.Meta().GetMessageByMessageIDHeader(ctx, principalID, env.MessageID)
			if lookupErr == nil {
				// Already mirrored by a label folder. Skip body download.
				log.Debug("imapimport: Gmail All Mail envelope dedup skip (already placed)",
					slog.String("account_id", accountID),
					slog.Uint64("uid", uid),
					slog.String("message_id", env.MessageID),
				)
				// Record the import state with the REAL herold ids of the
				// already-mirrored message so the write-back loop can address
				// this All Mail UID (D5). Writing 0/0 here left the row
				// unaddressable, so flag changes to archived mail never
				// propagated upstream. REQ-IMAP-IMP-34.
				sf := syncedFlagsFromIMAP(env.Flags)
				if msErr := w.opts.store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
					AccountID:       accountID,
					UpstreamFolder:  gmailAllMail,
					UpstreamUID:     uint32(env.UID),
					HeroldMessageID: existing.ID,
					HeroldMailboxID: existing.MailboxID,
					LastSyncedFlags: sf,
				}); msErr != nil {
					log.Warn("imapimport: Gmail All Mail UpsertIMAPImportMessageState (skip) failed",
						slog.String("account_id", accountID),
						slog.Uint64("uid", uid),
						slog.String("error", msErr.Error()),
					)
				}
				continue
			}
			// Not yet mirrored: needs body.
			needBodyFetch = append(needBodyFetch, env.UID)
		}

		// Update low-water for All Mail envelope pass.
		if minUID > 0 && (cursor.LowWaterUID == 0 || minUID < cursor.LowWaterUID) {
			cursor.LowWaterUID = minUID
		}

		// Contribute this folder's backfill-remaining to the per-pass
		// accumulator (REQ-IMAP-IMP-63 / D6): in-horizon All Mail UIDs still
		// below the low-water mark that have not been examined yet.
		w.backfillRemaining += int64(countUIDsBelow(allUIDs, cursor.LowWaterUID))

		// Body-fetch and ingest into Archive for unlabeled messages.
		if len(needBodyFetch) > 0 {
			archiveMBName := "Archive"
			// All Mail captures archived/unlabeled mail into Archive; it is a
			// backfill catch-all, never a live INBOX arrival, so the
			// categoriser is not run (categorise=false). REQ-IMAP-IMP-31 / D1.
			n, _, fetchErr := w.fetchAndIngest(ctx, conn, needBodyFetch, gmailAllMail, archiveMBName, false /* categorise */)
			if fetchErr != nil {
				return fmt.Errorf("imapimport: Gmail All Mail archive body-fetch: %w", fetchErr)
			}
			observe.IMAPImportMessagesFetchedTotal.WithLabelValues(accountID).Add(float64(n))
			w.status.incFetched(int64(n))
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

	observe.IMAPImportFetchDurationSeconds.WithLabelValues(accountID).Observe(
		w.opts.clk.Now().Sub(start).Seconds(),
	)
	// backfill_remaining is accumulated into w.backfillRemaining above and
	// published by syncAllFoldersGmail for the whole pass (REQ-IMAP-IMP-63).

	log.Debug("imapimport: Gmail All Mail envelope-dedup sync complete",
		slog.String("account_id", accountID),
	)
	return nil
}
