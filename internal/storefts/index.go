package storefts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Field names used in the Bleve document mapping. Centralised here so the
// index writer, query builder, and field-retrieval paths stay in sync;
// string-literal drift between the three is the usual FTS bug.
const (
	fieldPrincipalID    = "principal_id"
	fieldMailboxID      = "mailbox_id"
	fieldUID            = "uid"
	fieldMessageID      = "message_id"
	fieldFrom           = "from"
	fieldTo             = "to"
	fieldCc             = "cc"
	fieldSubject        = "subject"
	fieldBody           = "body"
	fieldAttachmentName = "attachment_name"
	fieldDate           = "date"
	fieldSize           = "size"
	fieldFlags          = "flags"
	fieldHasAttachments = "has_attachments"
)

// defaultQueryLimit caps a Query with q.Limit == 0 (REQ-STORE-64: backends
// MUST cap at a hard ceiling regardless of caller input).
const defaultQueryLimit = 1000

// hardQueryLimit is the absolute ceiling enforced regardless of caller
// input; the caller's Limit is min(Limit, hardQueryLimit).
const hardQueryLimit = 10000

// Index is a Bleve-backed implementation of store.FTS. One index covers all
// principals; queries filter by principal_id so a single index serves both
// the IMAP SEARCH and JMAP Email/query surfaces (REQ-PROTO-47).
//
// Writes use a pending batch that is flushed on Commit, on Delete, or when
// size ceiling is reached. The worker drives the cadence (size OR 500 ms)
// per docs/notes/spike-fts-cadence.md.
type Index struct {
	logger *slog.Logger
	clock  clock.Clock
	dir    string

	idx bleve.Index

	mu      sync.Mutex
	pending *bleve.Batch
}

// New opens (or creates) a Bleve index at dir/bleve. The directory is
// created if it does not exist. The clock is used for produced-at-style
// timestamps in tests; production uses clock.NewReal.
func New(dir string, logger *slog.Logger, clk clock.Clock) (*Index, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("storefts: mkdir %q: %w", dir, err)
	}
	path := filepath.Join(dir, "bleve")
	var idx bleve.Index
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created, cerr := bleve.New(path, buildMapping())
		if cerr != nil {
			return nil, fmt.Errorf("storefts: bleve.New %q: %w", path, cerr)
		}
		idx = created
	} else if err != nil {
		return nil, fmt.Errorf("storefts: stat %q: %w", path, err)
	} else {
		opened, oerr := bleve.Open(path)
		if oerr != nil {
			return nil, fmt.Errorf("storefts: bleve.Open %q: %w", path, oerr)
		}
		idx = opened
	}
	return &Index{
		logger: logger,
		clock:  clk,
		dir:    dir,
		idx:    idx,
	}, nil
}

// buildMapping defines the document mapping used by the index. Text fields
// use the standard analyzer; identifier and facet fields use the keyword
// analyzer so exact-match filters (principal_id, mailbox_id, flags) behave
// predictably. Retrieval-critical identifiers are stored so IMAP SEARCH
// can return MessageRef entries without a per-hit store round trip.
func buildMapping() mapping.IndexMapping {
	m := bleve.NewIndexMapping()

	textField := bleve.NewTextFieldMapping()
	textField.Analyzer = "standard"
	textField.Store = false
	textField.IncludeTermVectors = false
	textField.IncludeInAll = true

	keywordField := bleve.NewTextFieldMapping()
	keywordField.Analyzer = "keyword"
	keywordField.Store = false
	keywordField.IncludeInAll = false

	// Stored keyword fields: callers read these straight off the hit.
	storedKeywordField := bleve.NewTextFieldMapping()
	storedKeywordField.Analyzer = "keyword"
	storedKeywordField.Store = true
	storedKeywordField.IncludeInAll = false

	dateField := bleve.NewDateTimeFieldMapping()
	dateField.Store = true
	dateField.IncludeInAll = false

	numField := bleve.NewNumericFieldMapping()
	numField.Store = false
	numField.IncludeInAll = false

	boolField := bleve.NewBooleanFieldMapping()
	boolField.Store = false
	boolField.IncludeInAll = false

	doc := bleve.NewDocumentMapping()
	doc.AddFieldMappingsAt(fieldPrincipalID, storedKeywordField)
	doc.AddFieldMappingsAt(fieldMailboxID, storedKeywordField)
	doc.AddFieldMappingsAt(fieldUID, storedKeywordField)
	doc.AddFieldMappingsAt(fieldMessageID, storedKeywordField)
	doc.AddFieldMappingsAt(fieldFrom, textField)
	doc.AddFieldMappingsAt(fieldTo, textField)
	doc.AddFieldMappingsAt(fieldCc, textField)
	doc.AddFieldMappingsAt(fieldSubject, textField)
	doc.AddFieldMappingsAt(fieldBody, textField)
	doc.AddFieldMappingsAt(fieldAttachmentName, textField)
	doc.AddFieldMappingsAt(fieldDate, dateField)
	doc.AddFieldMappingsAt(fieldSize, numField)
	doc.AddFieldMappingsAt(fieldFlags, keywordField)
	doc.AddFieldMappingsAt(fieldHasAttachments, boolField)

	m.AddDocumentMapping("_default", doc)
	m.DefaultAnalyzer = "standard"
	return m
}

// IndexMessage writes (or replaces) the FTS document for msg. The text
// argument is the pre-extracted plain text (body + attachment text); the
// worker performs the extraction upstream so this method does not touch
// the blob store. The batch is accumulated in memory; call Commit to
// flush, or let the worker's size/time trigger drive it.
func (i *Index) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A message does not carry its owning principal directly; the worker
	// carries it alongside the message in the FTSChange. For call sites
	// that do have the principal, they can pass it via msg.MailboxID -> mb
	// -> principal. Here we accept a caller-supplied principal in a
	// per-index wrapper; the worker populates via IndexMessageFull below.
	return i.IndexMessageFull(ctx, 0, msg, text)
}

// IndexMessageFull is the worker-facing variant that takes the principal
// explicitly — the worker has it from the FTSChange and passes it in so
// the index can filter by owner. Kept separate from IndexMessage to stay
// bug-compatible with the store.FTS interface signature.
func (i *Index) IndexMessageFull(
	ctx context.Context,
	principalID store.PrincipalID,
	msg store.Message,
	text string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	doc := map[string]interface{}{
		fieldPrincipalID:    strconv.FormatUint(uint64(principalID), 10),
		fieldMailboxID:      strconv.FormatUint(uint64(msg.MailboxID), 10),
		fieldUID:            strconv.FormatUint(uint64(msg.UID), 10),
		fieldMessageID:      strconv.FormatUint(uint64(msg.ID), 10),
		fieldFrom:           msg.Envelope.From,
		fieldTo:             msg.Envelope.To,
		fieldCc:             msg.Envelope.Cc,
		fieldSubject:        msg.Envelope.Subject,
		fieldBody:           text,
		fieldSize:           float64(msg.Size),
		fieldFlags:          flagsToTokens(msg.Flags, msg.Keywords),
		fieldHasAttachments: false,
	}
	if !msg.InternalDate.IsZero() {
		doc[fieldDate] = msg.InternalDate
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pending == nil {
		i.pending = i.idx.NewBatch()
	}
	if err := i.pending.Index(docIDFor(msg.ID), doc); err != nil {
		return fmt.Errorf("storefts: batch.Index: %w", err)
	}
	return nil
}

// RemoveMessage deletes the document identified by id. The deletion is
// accumulated in the pending batch; Commit flushes it.
func (i *Index) RemoveMessage(ctx context.Context, id store.MessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pending == nil {
		i.pending = i.idx.NewBatch()
	}
	i.pending.Delete(docIDFor(id))
	return nil
}

// Delete is an alias for RemoveMessage that flushes immediately. Used by
// call sites that want "gone now" semantics (e.g. a reindex rebuild
// clearing a stale doc). Callers inside the worker use RemoveMessage +
// Commit to keep deletions batched with creates.
func (i *Index) Delete(ctx context.Context, id store.MessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.RemoveMessage(ctx, id); err != nil {
		return err
	}
	return i.Commit(ctx)
}

// Commit flushes the pending batch to the index backend. Safe to call on
// an empty batch (returns nil). Callers trigger it on size OR time per
// the spike recommendations.
func (i *Index) Commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.mu.Lock()
	batch := i.pending
	i.pending = nil
	i.mu.Unlock()
	if batch == nil || batch.Size() == 0 {
		return nil
	}
	if err := i.idx.Batch(batch); err != nil {
		return fmt.Errorf("storefts: idx.Batch: %w", err)
	}
	return nil
}

// PendingSize returns the current accumulated batch size. The worker uses
// it to decide whether to flush on the size ceiling.
func (i *Index) PendingSize() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pending == nil {
		return 0
	}
	return i.pending.Size()
}

// Query runs q against principalID's documents and returns hits in
// descending Score order. An empty Query matches all of the principal's
// messages (up to the limit). The principal filter is applied via a
// conjunction with a keyword term query on principal_id, so an empty
// free-text Query still scopes correctly.
func (i *Index) Query(
	ctx context.Context,
	principalID store.PrincipalID,
	q store.Query,
) ([]store.MessageRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queryStart := time.Now()
	defer func() {
		if observe.FTSQueryDuration != nil {
			observe.FTSQueryDuration.Observe(time.Since(queryStart).Seconds())
		}
	}()
	// Principal scope: mandatory.
	principalScope := bleve.NewTermQuery(strconv.FormatUint(uint64(principalID), 10))
	principalScope.SetField(fieldPrincipalID)

	conjuncts := []query.Query{principalScope}
	if q.MailboxID != 0 {
		mb := bleve.NewTermQuery(strconv.FormatUint(uint64(q.MailboxID), 10))
		mb.SetField(fieldMailboxID)
		conjuncts = append(conjuncts, mb)
	}
	if strings.TrimSpace(q.Text) != "" {
		conjuncts = append(conjuncts, bleve.NewQueryStringQuery(q.Text))
	}
	conjuncts = appendFieldQueries(conjuncts, fieldSubject, q.Subject)
	conjuncts = appendFieldQueries(conjuncts, fieldFrom, q.From)
	conjuncts = appendFieldQueries(conjuncts, fieldTo, q.To)
	conjuncts = appendFieldQueries(conjuncts, fieldBody, q.Body)
	conjuncts = appendFieldQueries(conjuncts, fieldAttachmentName, q.AttachmentName)

	bq := bleve.NewConjunctionQuery(conjuncts...)

	limit := q.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}
	if limit > hardQueryLimit {
		limit = hardQueryLimit
	}

	req := bleve.NewSearchRequest(bq)
	req.Size = limit
	req.Fields = []string{fieldMailboxID, fieldMessageID}

	// Ensure uncommitted writes are visible before the read; a miss here
	// would produce surprising "new mail not searchable" behaviour inside
	// a single test. Production callers never share the Index between a
	// writer and reader goroutine without the worker already having
	// committed, but the defensive flush is cheap on an empty batch.
	if err := i.Commit(ctx); err != nil {
		return nil, err
	}

	res, err := i.idx.SearchInContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("storefts: search: %w", err)
	}
	out := make([]store.MessageRef, 0, len(res.Hits))
	for _, h := range res.Hits {
		mailboxID, _ := parseStoredUint(h.Fields[fieldMailboxID])
		messageID, _ := parseStoredUint(h.Fields[fieldMessageID])
		out = append(out, store.MessageRef{
			MessageID: store.MessageID(messageID),
			MailboxID: store.MailboxID(mailboxID),
			Score:     h.Score,
		})
	}
	return out, nil
}

// appendFieldQueries turns a per-field term list into match queries scoped
// to the field. Empty strings are ignored.
func appendFieldQueries(dst []query.Query, field string, terms []string) []query.Query {
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		mq := bleve.NewMatchQuery(t)
		mq.SetField(field)
		dst = append(dst, mq)
	}
	return dst
}

// Close flushes any pending batch and closes the underlying Bleve index.
// Must be called exactly once on shutdown.
func (i *Index) Close() error {
	// Best-effort final flush; propagate the flush error if any, but still
	// close the underlying index so the directory handle is released.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	flushErr := i.Commit(ctx)
	closeErr := i.idx.Close()
	if flushErr != nil {
		return flushErr
	}
	if closeErr != nil {
		return fmt.Errorf("storefts: close: %w", closeErr)
	}
	return nil
}

// docIDFor renders a MessageID into the Bleve document ID form. The
// chosen representation (decimal string) keeps the ID human-readable for
// diagnostics.
func docIDFor(id store.MessageID) string {
	return strconv.FormatUint(uint64(id), 10)
}

// parseStoredUint extracts a stored keyword field's value as a uint64.
// Returns 0, false if the field is absent or not decodable.
func parseStoredUint(v interface{}) (uint64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// flagsToTokens renders the message flag bitfield + keyword strings into a
// token list suitable for the keyword analyzer. System flags are emitted
// with their IMAP backslash prefix so facet queries written against the
// IMAP vocabulary (`\Flagged`, `\Seen`) match directly.
func flagsToTokens(f store.MessageFlags, keywords []string) []string {
	out := make([]string, 0, 6+len(keywords))
	if f&store.MessageFlagSeen != 0 {
		out = append(out, `\Seen`)
	}
	if f&store.MessageFlagAnswered != 0 {
		out = append(out, `\Answered`)
	}
	if f&store.MessageFlagFlagged != 0 {
		out = append(out, `\Flagged`)
	}
	if f&store.MessageFlagDeleted != 0 {
		out = append(out, `\Deleted`)
	}
	if f&store.MessageFlagDraft != 0 {
		out = append(out, `\Draft`)
	}
	if f&store.MessageFlagRecent != 0 {
		out = append(out, `\Recent`)
	}
	for _, k := range keywords {
		k = strings.TrimSpace(k)
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}
