package admin

// imap_import_categoriser.go — bridges the imapimport.Categoriser seam
// (REQ-IMAP-IMP-31) to the categorise.Categoriser.
//
// The worker calls Categorise(ctx, principalID, messageID, mailboxName) after
// each new INBOX message is stored. This adapter:
//   1. Parses the numeric-string principalID / messageID back to their typed IDs.
//   2. Loads the stored message metadata (MailboxID + Blob hash) via GetMessage.
//   3. Streams the raw RFC 822 bytes from the blob store.
//   4. Parses the bytes with mailparse.Parse.
//   5. Calls categorise.Categoriser.CategoriseRich with spam.Unclassified (imported
//      mail has no spam verdict — REQ-IMAP-IMP-31 categorise clause).
//   6. If a non-empty category is assigned, applies the $category-<name> keyword
//      to the message via UpdateMessageFlags.
//
// Non-blocking semantics (REQ-FILT-230): any failure is logged at warn/debug
// and Categorise returns nil so the worker proceeds regardless.
// A nil *categorise.Categoriser is handled gracefully (CategoriseRich is a
// nil-safe method on that type, returns empty result).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// maxImportMessageBytes is the Phase-1 ceiling on how many bytes of a stored
// message we read into RAM during categorisation. Blobs larger than this
// truncate silently; mailparse only needs headers + initial body bytes to
// classify. Phase 2 will pipe the blob reader directly into mailparse.
const maxImportMessageBytes = 50 * 1024 * 1024 // 50 MiB

// imapImportCategoriserAdapter implements imapimport.Categoriser using a real
// categorise.Categoriser. Construct via newIMAPImportCategoriserAdapter.
type imapImportCategoriserAdapter struct {
	st     store.Store
	cat    *categorise.Categoriser
	logger *slog.Logger
}

// newIMAPImportCategoriserAdapter returns an adapter backed by cat. When cat
// is nil the adapter is still valid — CategoriseRich is nil-safe and returns
// an empty result, so every call is a no-op. This matches the behaviour
// documented on categorise.New when no LLM endpoint is configured.
func newIMAPImportCategoriserAdapter(st store.Store, cat *categorise.Categoriser, logger *slog.Logger) *imapImportCategoriserAdapter {
	return &imapImportCategoriserAdapter{st: st, cat: cat, logger: logger}
}

// Categorise implements imapimport.Categoriser. principalID and messageID are
// decimal-string representations of store.PrincipalID and store.MessageID
// respectively (produced by fmt.Sprint in the worker). mailboxName is the
// herold mailbox name (always "INBOX" at the call site — the worker gates on
// this itself).
//
// Failures are always swallowed: the method returns nil so the worker loop
// continues regardless. A hard error path (parse failure, store miss, blob
// miss) is logged at warn; a soft path (categorisation disabled or LLM
// returned "none") is logged at debug.
func (a *imapImportCategoriserAdapter) Categorise(ctx context.Context, principalID, messageID, mailboxName string) error {
	pid, pidErr := parsePrincipalID(principalID)
	if pidErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: parse principal_id",
			slog.String("principal_id", principalID),
			slog.String("err", pidErr.Error()),
		)
		return nil
	}
	mid, midErr := parseMessageID(messageID)
	if midErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: parse message_id",
			slog.String("message_id", messageID),
			slog.String("err", midErr.Error()),
		)
		return nil
	}
	if mid == 0 {
		// InsertMessage returned a zero ID (rare: dedup hit with no Message-ID).
		// Nothing to categorise.
		a.logger.DebugContext(ctx, "imap-import categorise: skipping zero message_id")
		return nil
	}

	// Step 1: load message metadata to get the MailboxID and blob hash.
	msg, getErr := a.st.Meta().GetMessage(ctx, mid)
	if getErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: GetMessage",
			slog.String("message_id", messageID),
			slog.String("err", getErr.Error()),
		)
		return nil
	}

	// Step 2: stream raw RFC 822 bytes from the blob store.
	rc, blobErr := a.st.Blobs().Get(ctx, msg.Blob.Hash)
	if blobErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: Blobs.Get",
			slog.String("message_id", messageID),
			slog.String("blob_hash", msg.Blob.Hash),
			slog.String("err", blobErr.Error()),
		)
		return nil
	}
	// Phase-1 floor: bound the read so a corrupt or abnormally large blob
	// cannot exhaust memory. TODO(REQ-STORE-19, Phase 2): stream the blob
	// reader directly into mailparse once mailparse accepts an io.Reader.
	rawBytes, readErr := io.ReadAll(io.LimitReader(rc, maxImportMessageBytes+1))
	_ = rc.Close()
	if readErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: read blob",
			slog.String("message_id", messageID),
			slog.String("err", readErr.Error()),
		)
		return nil
	}

	// Step 3: parse the RFC 822 bytes.
	parsed, parseErr := mailparse.Parse(bytes.NewReader(rawBytes), mailparse.NewLenientParseOptions())
	if parseErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: mailparse.Parse",
			slog.String("message_id", messageID),
			slog.String("err", parseErr.Error()),
		)
		return nil
	}

	// Step 4: call the LLM categoriser. Imported mail has no spam verdict;
	// pass spam.Unclassified so the prompt receives no spam context
	// (REQ-IMAP-IMP-31 categorise clause).
	result, catErr := a.cat.CategoriseRich(ctx, pid, parsed, nil /* authResults */, spam.Unclassified)
	if catErr != nil {
		// CategoriseRich already suppresses errors internally and returns nil;
		// log defensively in case behaviour changes.
		a.logger.WarnContext(ctx, "imap-import categorise: CategoriseRich",
			slog.String("message_id", messageID),
			slog.String("err", catErr.Error()),
		)
		return nil
	}
	if result.Category == "" {
		a.logger.DebugContext(ctx, "imap-import categorise: no category assigned",
			slog.String("message_id", messageID),
			slog.String("mailbox", mailboxName),
		)
		return nil
	}

	// Step 5: apply $category-<name> keyword to the stored message.
	keyword := fmt.Sprintf("$category-%s", result.Category)
	_, flagErr := a.st.Meta().UpdateMessageFlags(
		ctx,
		mid,
		msg.MailboxID,
		0,                 // flagAdd: no system-flag changes
		0,                 // flagClear: no system-flag changes
		[]string{keyword}, // keywordAdd
		nil,               // keywordClear
		0,                 // unchangedSince: no CONDSTORE guard
	)
	if flagErr != nil {
		a.logger.WarnContext(ctx, "imap-import categorise: UpdateMessageFlags",
			slog.String("message_id", messageID),
			slog.String("keyword", keyword),
			slog.String("err", flagErr.Error()),
		)
		return nil
	}

	a.logger.DebugContext(ctx, "imap-import categorise: applied category",
		slog.String("message_id", messageID),
		slog.String("category", result.Category),
		slog.String("keyword", keyword),
	)
	return nil
}

// parsePrincipalID parses the decimal-string representation of a
// store.PrincipalID produced by fmt.Sprint.
func parsePrincipalID(s string) (store.PrincipalID, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsePrincipalID(%q): %w", s, err)
	}
	return store.PrincipalID(n), nil
}

// parseMessageID parses the decimal-string representation of a
// store.MessageID produced by fmt.Sprint.
func parseMessageID(s string) (store.MessageID, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parseMessageID(%q): %w", s, err)
	}
	return store.MessageID(n), nil
}
