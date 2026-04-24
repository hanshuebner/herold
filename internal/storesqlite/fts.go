package storesqlite

import (
	"context"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// ftsStub is a minimal FTS implementation used when the real Bleve
// backend (internal/storefts) is not plugged in. It performs
// substring matching against cached envelope fields so that Wave 1
// protocol wiring can exercise the surface without taking a hard
// dependency on Bleve. Production deployments replace this via
// storefts.New and a thin composed Store; see internal/storefts/doc.go.
//
// Indexing is a no-op for IndexMessage / RemoveMessage: the stub reads
// directly from the messages table on Query.
type ftsStub struct {
	s *Store
}

func (f *ftsStub) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	return nil
}

func (f *ftsStub) RemoveMessage(ctx context.Context, id store.MessageID) error {
	return nil
}

func (f *ftsStub) Query(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 1000
	}
	// Build the query against envelope columns + keyword fields. All
	// matching is substring / case-insensitive on the indexed columns
	// that make sense for a stub: subject, from, to, message-id.
	terms := collectTerms(q)
	where := []string{"m.mailbox_id IN (SELECT id FROM mailboxes WHERE principal_id = ?)"}
	args := []any{int64(principalID)}
	if q.MailboxID != 0 {
		where = append(where, "m.mailbox_id = ?")
		args = append(args, int64(q.MailboxID))
	}
	for _, term := range terms {
		where = append(where, `(
			LOWER(m.env_subject) LIKE ? OR
			LOWER(m.env_from) LIKE ? OR
			LOWER(m.env_to) LIKE ? OR
			LOWER(m.env_cc) LIKE ? OR
			LOWER(m.env_bcc) LIKE ?)`)
		pat := "%" + strings.ToLower(term) + "%"
		args = append(args, pat, pat, pat, pat, pat)
	}
	query := `SELECT m.id, m.mailbox_id FROM messages m WHERE ` +
		strings.Join(where, " AND ") +
		` ORDER BY m.received_at_us DESC LIMIT ?`
	args = append(args, q.Limit)

	rows, err := f.s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MessageRef
	for rows.Next() {
		var id, mbox int64
		if err := rows.Scan(&id, &mbox); err != nil {
			return nil, err
		}
		out = append(out, store.MessageRef{
			MessageID: store.MessageID(id),
			MailboxID: store.MailboxID(mbox),
			Score:     1,
		})
	}
	return out, rows.Err()
}

func (f *ftsStub) ReadChangeFeedForFTS(ctx context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	if max <= 0 {
		max = 1000
	}
	rows, err := f.s.db.QueryContext(ctx, `
		SELECT id, principal_id, kind, mailbox_id, message_id, produced_at_us
		  FROM state_changes
		 WHERE id > ? ORDER BY id ASC LIMIT ?`, int64(cursor), max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FTSChange
	for rows.Next() {
		var id int64
		var pid, kind, mbox, mid, prodUs int64
		if err := rows.Scan(&id, &pid, &kind, &mbox, &mid, &prodUs); err != nil {
			return nil, err
		}
		out = append(out, store.FTSChange{
			Seq:         uint64(id),
			PrincipalID: store.PrincipalID(pid),
			MailboxID:   store.MailboxID(mbox),
			MessageID:   store.MessageID(mid),
			Kind:        store.ChangeKind(kind),
			ProducedAt:  fromMicros(prodUs),
		})
	}
	return out, rows.Err()
}

func (f *ftsStub) Commit(ctx context.Context) error { return nil }

// collectTerms flattens the Query's text + per-field slices into a
// single deduplicated term list. The stub treats every term as an AND
// against the envelope columns.
func collectTerms(q store.Query) []string {
	var out []string
	if q.Text != "" {
		out = append(out, q.Text)
	}
	for _, s := range q.Subject {
		out = append(out, s)
	}
	for _, s := range q.From {
		out = append(out, s)
	}
	for _, s := range q.To {
		out = append(out, s)
	}
	for _, s := range q.Body {
		out = append(out, s)
	}
	for _, s := range q.AttachmentName {
		out = append(out, s)
	}
	// Deduplicate to keep WHERE clauses short.
	seen := map[string]struct{}{}
	uniq := out[:0]
	for _, s := range out {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		uniq = append(uniq, s)
	}
	return uniq
}
