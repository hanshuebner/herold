package protojmap

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanshuebner/herold/internal/store"
)

// ErrThreadNotFound reports a thread id that does not resolve to any
// exportable message for the requesting principal (REQ-TEXP-12): the
// id is malformed, the thread does not exist, or ACL filtering left
// nothing visible.
var ErrThreadNotFound = errors.New("protojmap: thread not found")

// ErrExportTooLarge reports that streaming a thread export crossed the
// caller-supplied byte bound (REQ-TEXP-04).
var ErrExportTooLarge = errors.New("protojmap: thread export exceeds byte limit")

// ThreadExportMessage is one message of a resolved thread export.
type ThreadExportMessage struct {
	// Index is the 1-based position in export order.
	Index int
	// MessageID is the store id of the exported message.
	MessageID store.MessageID
	// ReceivedAt is the message's receivedAt instant.
	ReceivedAt time.Time
	// Subject is the decoded Subject header from the cached envelope.
	Subject string
	// Blob references the raw RFC 5322 bytes in the blob store.
	Blob store.BlobRef
}

// ThreadExport is the resolved message set of one thread, in ascending
// receivedAt order with duplicates by blob hash removed (REQ-TEXP-01..03).
type ThreadExport struct {
	// OwnerPID is the principal owning the addressed account.
	OwnerPID store.PrincipalID
	// ThreadKey is the numeric thread key ("t<key>" on the wire).
	ThreadKey uint64
	// Messages is the export in order; never empty.
	Messages []ThreadExportMessage
}

// TotalSize returns the sum of the exported blobs' sizes, so a caller
// with a byte bound can reject an oversized thread before streaming.
func (e *ThreadExport) TotalSize() int64 {
	var n int64
	for _, m := range e.Messages {
		n += m.Blob.Size
	}
	return n
}

// ThreadExporter resolves threads to their exportable messages and
// streams their raw bytes from the blob store. It is the single
// implementation behind GET /jmap/download-thread (REQ-TEXP-10) and
// app invocation (REQ-APP-40).
type ThreadExporter struct {
	st store.Store
}

// NewThreadExporter binds an exporter to st.
func NewThreadExporter(st store.Store) *ThreadExporter {
	return &ThreadExporter{st: st}
}

// ParseThreadID decodes the "t<n>" JMAP thread id wire form (the bare
// numeric form is accepted too). It returns false on malformed input or
// a zero key.
func ParseThreadID(s Id) (uint64, bool) {
	if len(s) > 1 && s[0] == 't' {
		s = s[1:]
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}

// AccessibleMailboxes returns the mailboxes of ownerPID that callerPID
// can see: every mailbox when callerPID has owner access
// (HasOwnerAccess), otherwise the ACL-shared subset (REQ-PROTO-33).
func AccessibleMailboxes(ctx context.Context, meta store.Metadata, callerPID, ownerPID store.PrincipalID) ([]store.Mailbox, error) {
	if HasOwnerAccess(ctx, meta, callerPID, ownerPID) {
		return meta.ListMailboxes(ctx, ownerPID)
	}
	shared, err := meta.ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return nil, err
	}
	out := make([]store.Mailbox, 0, len(shared))
	for _, mb := range shared {
		if mb.PrincipalID == ownerPID {
			out = append(out, mb)
		}
	}
	return out, nil
}

// VisibleMemberships returns the mailbox memberships of m that lie in
// visible. A message is visible to a caller iff the result is non-empty.
func VisibleMemberships(m store.Message, visible map[store.MailboxID]store.Mailbox) []store.Mailbox {
	var out []store.Mailbox
	for _, mm := range m.Mailboxes {
		if mb, ok := visible[mm.MailboxID]; ok {
			out = append(out, mb)
		}
	}
	if len(out) == 0 && m.MailboxID != 0 {
		if mb, ok := visible[m.MailboxID]; ok {
			out = append(out, mb)
		}
	}
	return out
}

// Resolve returns the export of threadID in ownerPID's account as seen
// by callerPID (REQ-TEXP-01..03). Messages are visible under the same
// ACL rule Thread/get applies. Messages whose every visible mailbox has
// the trash or junk role are dropped unless that leaves nothing; the
// rest are ordered by receivedAt (MessageID tiebreak) and de-duplicated
// by blob hash, keeping the earliest copy. Returns ErrThreadNotFound
// when nothing is exportable.
func (x *ThreadExporter) Resolve(ctx context.Context, callerPID, ownerPID store.PrincipalID, threadID Id) (*ThreadExport, error) {
	key, ok := ParseThreadID(threadID)
	if !ok {
		return nil, ErrThreadNotFound
	}
	rowsByKey, err := x.st.Meta().ListThreadsByKeys(ctx, ownerPID, []uint64{key})
	if err != nil {
		return nil, fmt.Errorf("protojmap: thread export: list thread: %w", err)
	}
	rows := rowsByKey[key]
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	mboxes, err := AccessibleMailboxes(ctx, x.st.Meta(), callerPID, ownerPID)
	if err != nil {
		return nil, fmt.Errorf("protojmap: thread export: list mailboxes: %w", err)
	}
	visible := make(map[store.MailboxID]store.Mailbox, len(mboxes))
	for _, mb := range mboxes {
		visible[mb.ID] = mb
	}

	var kept, binned []store.Message
	for _, r := range rows {
		m, err := x.st.Meta().GetMessage(ctx, r.MessageID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("protojmap: thread export: get message: %w", err)
		}
		in := VisibleMemberships(m, visible)
		if len(in) == 0 {
			continue
		}
		if allTrashOrJunk(in) {
			binned = append(binned, m)
		} else {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		kept = binned
	}
	if len(kept) == 0 {
		return nil, ErrThreadNotFound
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].ReceivedAt.Equal(kept[j].ReceivedAt) {
			return kept[i].ID < kept[j].ID
		}
		return kept[i].ReceivedAt.Before(kept[j].ReceivedAt)
	})
	out := &ThreadExport{OwnerPID: ownerPID, ThreadKey: key}
	seen := make(map[string]struct{}, len(kept))
	for _, m := range kept {
		if _, dup := seen[m.Blob.Hash]; dup {
			continue
		}
		seen[m.Blob.Hash] = struct{}{}
		out.Messages = append(out.Messages, ThreadExportMessage{
			Index:      len(out.Messages) + 1,
			MessageID:  m.ID,
			ReceivedAt: m.ReceivedAt,
			Subject:    m.Envelope.Subject,
			Blob:       m.Blob,
		})
	}
	return out, nil
}

func allTrashOrJunk(mboxes []store.Mailbox) bool {
	for _, mb := range mboxes {
		if mb.Attributes&(store.MailboxAttrTrash|store.MailboxAttrJunk) == 0 {
			return false
		}
	}
	return true
}

// Stream opens each exported message's blob in order and hands its
// bytes to fn. Blobs are read straight from the blob store, never
// buffered whole. When maxBytes > 0 the cumulative bytes read across
// all messages are bounded: the reader passed to fn fails with
// ErrExportTooLarge once the bound is crossed, and Stream returns an
// error wrapping it.
func (x *ThreadExporter) Stream(
	ctx context.Context,
	exp *ThreadExport,
	maxBytes int64,
	fn func(m ThreadExportMessage, r io.Reader) error,
) error {
	budget := &byteBudget{remaining: maxBytes, bounded: maxBytes > 0}
	for _, m := range exp.Messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		rc, err := x.st.Blobs().Get(ctx, m.Blob.Hash)
		if err != nil {
			return fmt.Errorf("protojmap: thread export: open blob %s: %w", m.Blob.Hash, err)
		}
		err = fn(m, &budgetReader{r: rc, b: budget})
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteZip streams exp to w as a zip archive with one Deflate entry per
// message, named by ZipEntryName (REQ-TEXP-11). maxBytes bounds the
// uncompressed bytes read as in Stream.
func (x *ThreadExporter) WriteZip(ctx context.Context, w io.Writer, exp *ThreadExport, maxBytes int64) error {
	zw := zip.NewWriter(w)
	n := len(exp.Messages)
	err := x.Stream(ctx, exp, maxBytes, func(m ThreadExportMessage, r io.Reader) error {
		hdr := &zip.FileHeader{
			Name:     ZipEntryName(m, n),
			Method:   zip.Deflate,
			Modified: m.ReceivedAt.UTC(),
		}
		ew, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("protojmap: thread export: zip entry: %w", err)
		}
		if _, err := io.Copy(ew, r); err != nil {
			return fmt.Errorf("protojmap: thread export: copy message %d: %w", m.MessageID, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("protojmap: thread export: close zip: %w", err)
	}
	return nil
}

// ZipEntryName returns the zip entry name for m in an export of count
// messages: "NN-<yyyy-mm-dd>-<sanitised-subject>.eml", NN zero-padded to
// the width of count, the date in UTC (REQ-TEXP-11).
func ZipEntryName(m ThreadExportMessage, count int) string {
	width := len(strconv.Itoa(count))
	return fmt.Sprintf("%0*d-%s-%s.eml", width, m.Index,
		m.ReceivedAt.UTC().Format("2006-01-02"), SanitiseSubjectFilename(m.Subject, "message"))
}

// SanitiseSubjectFilename turns a subject into a filename stem using the
// .eml download rules (web REQ-MAIL-141): path separators, characters
// forbidden on Windows and control characters become "_", whitespace
// runs collapse to one space, the result is trimmed and cut to 80
// characters. A subject that reduces to nothing but "_" and whitespace
// yields fallback.
func SanitiseSubjectFilename(subject, fallback string) string {
	var b strings.Builder
	for _, r := range subject {
		switch {
		case r == utf8.RuneError:
			b.WriteRune('_')
		case strings.ContainsRune(`\/:*?"<>|`, r), unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	collapsed := strings.Join(strings.Fields(b.String()), " ")
	if strings.Trim(collapsed, "_ ") == "" {
		return fallback
	}
	if runes := []rune(collapsed); len(runes) > 80 {
		collapsed = strings.TrimRightFunc(string(runes[:80]), unicode.IsSpace)
	}
	return collapsed
}

// byteBudget is the cumulative read allowance shared by every message
// reader of one Stream call.
type byteBudget struct {
	remaining int64
	bounded   bool
}

// budgetReader counts bytes against b and fails with ErrExportTooLarge
// as soon as the cumulative total exceeds the bound.
type budgetReader struct {
	r io.Reader
	b *byteBudget
}

func (br *budgetReader) Read(p []byte) (int, error) {
	if !br.b.bounded {
		return br.r.Read(p)
	}
	if br.b.remaining < 0 {
		return 0, ErrExportTooLarge
	}
	// Read one byte past the allowance so an export of exactly the
	// bound succeeds and one byte more fails.
	if int64(len(p)) > br.b.remaining+1 {
		p = p[:br.b.remaining+1]
	}
	n, err := br.r.Read(p)
	br.b.remaining -= int64(n)
	if br.b.remaining < 0 {
		return 0, ErrExportTooLarge
	}
	return n, err
}
