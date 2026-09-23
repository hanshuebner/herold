// Package orphanblobs implements the recovery path behind `herold diag
// orphan-blobs list|restore` (re #487): the IMAP-import upstream-expunge
// reconcile can delete a message row while its content-addressed blob
// survives on disk (the blob store is never garbage-collected on message
// deletion), so a mass or partial deletion leaves the mail recoverable from
// its blob alone.
//
// List is a dry run: it enumerates every blob the configured blob store
// holds that no live message currently references, parses each orphan's own
// RFC 5322 headers directly from the blob (the row that would have carried
// them is gone), and reports whether the orphan's Message-ID also belongs to
// a live message (a dedup/sent-copy artifact, not a loss) or is referenced
// by a live message's In-Reply-To/References (a thread the orphan's reply
// belongs to). Writes nothing.
//
// Restore re-inserts one named blob as a message for a principal, through
// the same store.Metadata.InsertMessage path live ingest uses, so it is
// deduplicated, threaded (including #485's late-ancestor merge), and
// change-fed exactly like a freshly-arrived message. It refuses when a live
// message already carries the blob's Message-ID.
package orphanblobs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// OrphanBlob is one entry in the List result: a blob on disk that no live
// message references, plus the headers parsed directly from the blob and
// its duplicate/reference status against the store's live messages.
type OrphanBlob struct {
	// Hash is the blob's content-addressed BLAKE3 hex hash.
	Hash string
	// Size is the blob's size in bytes.
	Size int64
	// Date is the parsed Date header, zero if absent or unparsable.
	Date time.Time
	// From is the raw From header value.
	From string
	// Subject is the decoded Subject header.
	Subject string
	// MessageID is the raw (angle-bracketed) Message-ID header value,
	// empty if the blob has none.
	MessageID string
	// DuplicateOfLiveMessageID is non-zero when a live message already
	// carries MessageID -- the mirror's own sent-copy dedup, or any other
	// reason a live copy with the same Message-ID exists. Restoring this
	// blob is refused (see Restore).
	DuplicateOfLiveMessageID store.MessageID
	// ReferencedByLiveThread is true when a live message's In-Reply-To or
	// References header names MessageID: a thread this orphan belongs to
	// survived even though the orphan's own row did not.
	ReferencedByLiveThread bool
	// ParseError is non-empty when the blob's headers could not be
	// parsed; every other field except Hash and Size is then zero.
	ParseError string
}

// List enumerates every blob st's blob store holds that no live message
// (across every principal) currently references. Read-only.
func List(ctx context.Context, st store.Store) ([]OrphanBlob, error) {
	blobs, err := st.Blobs().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("orphanblobs: Blobs.List: %w", err)
	}
	live, err := st.Meta().ListAllMessageBlobHashes(ctx)
	if err != nil {
		return nil, fmt.Errorf("orphanblobs: ListAllMessageBlobHashes: %w", err)
	}

	out := make([]OrphanBlob, 0, len(blobs))
	for _, b := range blobs {
		if live[b.Hash] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, describeOrphan(ctx, st, b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out, nil
}

// describeOrphan reads b's blob, parses its headers, and looks up its
// duplicate/reference status. A read or parse failure is recorded on
// ParseError rather than failing the whole List call -- one unreadable or
// malformed blob must not hide every other orphan from the operator.
func describeOrphan(ctx context.Context, st store.Store, b store.BlobRef) OrphanBlob {
	ob := OrphanBlob{Hash: b.Hash, Size: b.Size}
	msg, err := parseBlob(ctx, st, b.Hash)
	if err != nil {
		ob.ParseError = err.Error()
		return ob
	}
	ob.Date = parseDate(msg.Envelope.Date)
	ob.From = joinAddrs(msg.Envelope.From)
	ob.Subject = msg.Envelope.Subject
	ob.MessageID = msg.Envelope.MessageID
	if ob.MessageID == "" {
		return ob
	}
	normID := mailparse.NormalizeMessageID(ob.MessageID)

	if hits, serr := st.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: normID, Limit: 1}); serr == nil && len(hits) > 0 {
		ob.DuplicateOfLiveMessageID = hits[0].MessageID
	}
	if hits, serr := st.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{ReferencesMessageID: normID, Limit: 1}); serr == nil && len(hits) > 0 {
		ob.ReferencedByLiveThread = true
	}
	return ob
}

// RestoreResult reports what Restore did for one blob hash.
type RestoreResult struct {
	Hash      string
	MessageID store.MessageID
	// Refused is true when a live message already carries the blob's
	// Message-ID; Reason then explains which one.
	Refused bool
	Reason  string
}

// Restore re-inserts the blob named by hash as a message for principalID,
// into the principal's Archive mailbox (creating it if absent) plus
// labelMailboxID when non-zero, with the $seen flag set on every
// membership, through store.Metadata.InsertMessage -- the same path live
// ingest uses, so the message is deduplicated, threaded (including #485's
// late-ancestor merge), and emits the normal change-feed row. Refuses,
// without writing anything, when a live message already carries the blob's
// Message-ID header. labelMailboxID, when non-zero, must belong to
// principalID.
func Restore(ctx context.Context, st store.Store, principalID store.PrincipalID, hash string, labelMailboxID store.MailboxID) (RestoreResult, error) {
	res := RestoreResult{Hash: hash}

	msg, raw, err := parseBlobWithBytes(ctx, st, hash)
	if err != nil {
		return res, err
	}

	if msg.Envelope.MessageID != "" {
		normID := mailparse.NormalizeMessageID(msg.Envelope.MessageID)
		hits, serr := st.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: normID, Limit: 1})
		if serr != nil {
			return res, fmt.Errorf("orphanblobs: SearchAdminMessages: %w", serr)
		}
		if len(hits) > 0 {
			res.Refused = true
			res.Reason = fmt.Sprintf("a live message (id %d) already carries Message-ID %q", hits[0].MessageID, normID)
			return res, nil
		}
	}

	if labelMailboxID != 0 {
		mbs, lerr := st.Meta().ListMailboxes(ctx, principalID)
		if lerr != nil {
			return res, fmt.Errorf("orphanblobs: ListMailboxes: %w", lerr)
		}
		found := false
		for _, mb := range mbs {
			if mb.ID == labelMailboxID {
				found = true
				break
			}
		}
		if !found {
			return res, fmt.Errorf("orphanblobs: mailbox %d does not belong to principal %d", labelMailboxID, principalID)
		}
	}

	archiveMB, err := ensureArchiveMailbox(ctx, st, principalID)
	if err != nil {
		return res, fmt.Errorf("orphanblobs: ensure Archive mailbox: %w", err)
	}

	// Re-Put the already-canonical blob: idempotent (the store dedups by
	// content hash), and keeps this path independent of whatever bytes
	// Blobs.Get happened to return rather than assuming they are already
	// the exact stored form.
	blobRef, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return res, fmt.Errorf("orphanblobs: Blobs.Put: %w", err)
	}

	internalDate := parseDate(msg.Envelope.Date)
	if internalDate.IsZero() {
		internalDate = time.Now()
	}

	storeMsg := store.Message{
		PrincipalID:  principalID,
		Size:         int64(len(raw)),
		Blob:         blobRef,
		InternalDate: internalDate,
		ReceivedAt:   internalDate,
		Envelope:     envelopeFromParsed(msg),
		// re #487: distinguishes an operator-restored row from every live
		// ingest path in message research; IngestSourceRef carries the
		// blob hash it was restored from.
		IngestSource:    store.IngestSourceDiagRestore,
		IngestSourceRef: hash,
	}
	targets := []store.MessageMailbox{{
		MailboxID: archiveMB.ID,
		Flags:     store.MessageFlagSeen,
	}}
	if labelMailboxID != 0 {
		targets = append(targets, store.MessageMailbox{
			MailboxID: labelMailboxID,
			Flags:     store.MessageFlagSeen,
		})
	}

	insertedUID, _, err := st.Meta().InsertMessage(ctx, storeMsg, targets)
	if err != nil {
		return res, fmt.Errorf("orphanblobs: InsertMessage: %w", err)
	}
	msgID, err := st.Meta().GetMessageIDByMailboxUID(ctx, archiveMB.ID, insertedUID)
	if err != nil {
		return res, fmt.Errorf("orphanblobs: GetMessageIDByMailboxUID: %w", err)
	}
	res.MessageID = msgID
	return res, nil
}

// ensureArchiveMailbox returns pid's Archive-attributed mailbox, creating it
// if absent. Matches restore_archive.go's attribute-based lookup (not
// name-based) so a renamed Archive mailbox is still found.
func ensureArchiveMailbox(ctx context.Context, st store.Store, pid store.PrincipalID) (store.Mailbox, error) {
	mbs, err := st.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mb := range mbs {
		if mb.Attributes&store.MailboxAttrArchive != 0 {
			return mb, nil
		}
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "Archive",
		Attributes:  store.MailboxAttrArchive,
	})
	if err != nil {
		return store.Mailbox{}, err
	}
	return mb, nil
}

// parseBlob reads and parses the blob named by hash, discarding the raw
// bytes. Used by List, which only needs the headers.
func parseBlob(ctx context.Context, st store.Store, hash string) (mailparse.Message, error) {
	msg, _, err := parseBlobWithBytes(ctx, st, hash)
	return msg, err
}

// parseBlobWithBytes reads the blob named by hash, parses it, and returns
// both the parsed message and the raw bytes (Restore needs the bytes to
// re-Put the blob and compute its size).
func parseBlobWithBytes(ctx context.Context, st store.Store, hash string) (mailparse.Message, []byte, error) {
	r, err := st.Blobs().Get(ctx, hash)
	if err != nil {
		return mailparse.Message{}, nil, fmt.Errorf("orphanblobs: Blobs.Get: %w", err)
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		return mailparse.Message{}, nil, fmt.Errorf("orphanblobs: read blob: %w", err)
	}

	// Lenient boundary/charset handling, matching imapimport's ingest
	// path (re #279): an orphan blob is exactly the kind of already-
	// mirrored, already-displayed mail that leniency exists for, and a
	// stricter parse here would refuse to restore a message the store
	// happily rendered before it was deleted.
	opts := mailparse.NewParseOptions()
	opts.StrictBoundary = false
	opts.StrictCharset = false
	msg, err := mailparse.Parse(bytes.NewReader(raw), opts)
	if err != nil {
		return mailparse.Message{}, nil, fmt.Errorf("orphanblobs: mailparse.Parse: %w", err)
	}
	return msg, raw, nil
}

// envelopeFromParsed converts a parsed mailparse.Message into the
// store.Envelope shape InsertMessage expects, matching
// imapimport's envelopeFromParsed (sync.go) field-for-field so a restored
// message threads and dedups exactly like a freshly-mirrored one.
func envelopeFromParsed(msg mailparse.Message) store.Envelope {
	var refs string
	if len(msg.Envelope.References) > 0 {
		parts := make([]string, len(msg.Envelope.References))
		copy(parts, msg.Envelope.References)
		refs = joinStrings(parts)
	}
	return store.Envelope{
		Subject:    msg.Envelope.Subject,
		From:       joinAddrs(msg.Envelope.From),
		To:         joinAddrs(msg.Envelope.To),
		Cc:         joinAddrs(msg.Envelope.Cc),
		Bcc:        joinAddrs(msg.Envelope.Bcc),
		MessageID:  msg.Envelope.MessageID,
		InReplyTo:  joinStrings(msg.Envelope.InReplyTo),
		References: refs,
		Date:       parseDate(msg.Envelope.Date),
	}
}

func joinAddrs(addrs []mail.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return joinStrings(parts)
}

func joinStrings(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// parseDate parses an RFC 5322 Date header value, returning the zero
// time.Time if s is empty or unparsable.
func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := mail.ParseDate(s)
	if err != nil {
		return time.Time{}
	}
	return t
}
