package storepg

import (
	"context"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// ftsStub is the substring-matching FTS stub for Postgres. The real
// Bleve-backed FTS ships as internal/storefts; this is only here so
// the Wave 1 protocol wiring can exercise the surface without a hard
// dependency on Bleve. See internal/storesqlite/fts.go for the SQLite
// twin.
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
	terms := collectTerms(q)
	where := []string{"m.mailbox_id IN (SELECT id FROM mailboxes WHERE principal_id = $1)"}
	args := []any{int64(principalID)}
	argIdx := 2
	if q.MailboxID != 0 {
		where = append(where, "m.mailbox_id = $"+itoa(argIdx))
		args = append(args, int64(q.MailboxID))
		argIdx++
	}
	for _, term := range terms {
		clause := "(" +
			"LOWER(m.env_subject) LIKE $" + itoa(argIdx) + " OR " +
			"LOWER(m.env_from) LIKE $" + itoa(argIdx) + " OR " +
			"LOWER(m.env_to) LIKE $" + itoa(argIdx) + " OR " +
			"LOWER(m.env_cc) LIKE $" + itoa(argIdx) + " OR " +
			"LOWER(m.env_bcc) LIKE $" + itoa(argIdx) + ")"
		where = append(where, clause)
		args = append(args, "%"+strings.ToLower(term)+"%")
		argIdx++
	}
	query := `SELECT m.id, m.mailbox_id FROM messages m WHERE ` +
		strings.Join(where, " AND ") +
		` ORDER BY m.received_at_us DESC LIMIT $` + itoa(argIdx)
	args = append(args, q.Limit)

	rows, err := f.s.pool.Query(ctx, query, args...)
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
	rows, err := f.s.pool.Query(ctx, `
		SELECT id, principal_id, kind, mailbox_id, message_id, produced_at_us
		  FROM state_changes
		 WHERE id > $1 ORDER BY id ASC LIMIT $2`, int64(cursor), max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FTSChange
	for rows.Next() {
		var id int64
		var pid int64
		var kind int32
		var mbox, mid, prodUs int64
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

func collectTerms(q store.Query) []string {
	var out []string
	if q.Text != "" {
		out = append(out, q.Text)
	}
	out = append(out, q.Subject...)
	out = append(out, q.From...)
	out = append(out, q.To...)
	out = append(out, q.Body...)
	out = append(out, q.AttachmentName...)
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

// itoa is a tiny int-to-string helper avoiding strconv allocations on
// the hot WHERE-clause assembly path. Argument indices are small
// single- or double-digit numbers.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
