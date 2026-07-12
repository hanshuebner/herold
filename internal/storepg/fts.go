package storepg

import (
	"context"
	"strings"
	"sync"

	"github.com/hanshuebner/herold/internal/store"
)

// ftsStub is the substring-matching FTS stub for Postgres. The real
// Bleve-backed FTS ships as internal/storefts; this is only here so
// the Wave 1 protocol wiring can exercise the surface without a hard
// dependency on Bleve. See internal/storesqlite/fts.go for the SQLite
// twin.
//
// IndexMessage records the caller-supplied text in a per-message map so
// body: and text: queries can substring-match it, mirroring the SQLite
// stub's bodyText map -- without it, a body-only text: match is
// unrepresentable in tests against this backend at all (re #207: this
// gap is why the Email/setByQuery body-only-match regression had no
// Postgres-side test coverage to catch it). The map is in-memory only --
// the stub is for tests, not production (the real Bleve-backed
// storefts.Index replaces this via storefts.NewComposite in
// internal/admin/server.go).
type ftsStub struct {
	s *Store

	mu       sync.Mutex
	bodyText map[store.MessageID]string
}

func (f *ftsStub) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	if text == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bodyText == nil {
		f.bodyText = make(map[store.MessageID]string)
	}
	f.bodyText[msg.ID] = strings.ToLower(text)
	return nil
}

func (f *ftsStub) RemoveMessage(ctx context.Context, id store.MessageID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bodyText, id)
	return nil
}

// Query dispatches to queryFlat directly, or — when q.Or is populated
// (re #198: a JMAP `{operator: "OR", conditions: [...]}` Email/query
// filter, e.g. the suite's "all mail with this person" search across a
// contact's several e-mail addresses) — unions the results of querying
// each branch (with q's own direct-field terms ANDed in via
// mergeQueryBranch) so a message matching ANY branch is returned rather
// than only the first.
func (f *ftsStub) Query(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	if len(q.Or) == 0 {
		return f.queryFlat(ctx, principalID, q)
	}
	seen := make(map[store.MessageID]store.MessageRef)
	var order []store.MessageID
	for _, branch := range q.Or {
		merged := mergeQueryBranch(q, branch)
		merged.Limit = q.Limit
		hits, err := f.Query(ctx, principalID, merged)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if _, ok := seen[h.MessageID]; !ok {
				seen[h.MessageID] = h
				order = append(order, h.MessageID)
			}
		}
	}
	out := make([]store.MessageRef, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out, nil
}

// mergeQueryBranch ANDs outer's own direct-field terms into branch (a
// single Or alternative) before it is queried on its own. branch's own
// nested Or (if any) is preserved so Query's recursive call still sees it.
func mergeQueryBranch(outer, branch store.Query) store.Query {
	merged := branch
	if merged.MailboxID == 0 {
		merged.MailboxID = outer.MailboxID
	}
	if outer.Text != "" {
		if merged.Text == "" {
			merged.Text = outer.Text
		} else {
			merged.Text = merged.Text + " " + outer.Text
		}
	}
	merged.Subject = append(append([]string{}, outer.Subject...), merged.Subject...)
	merged.From = append(append([]string{}, outer.From...), merged.From...)
	merged.To = append(append([]string{}, outer.To...), merged.To...)
	merged.Cc = append(append([]string{}, outer.Cc...), merged.Cc...)
	merged.Body = append(append([]string{}, outer.Body...), merged.Body...)
	merged.AttachmentName = append(append([]string{}, outer.AttachmentName...), merged.AttachmentName...)
	return merged
}

// queryFlat is the original single-branch (no q.Or) implementation.
func (f *ftsStub) queryFlat(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 1000
	}
	// Multi-mailbox membership (migration 0024): mailbox_id moved off
	// the messages row onto message_mailboxes. The principal scope uses
	// the denorm column messages.principal_id; mailbox scoping (when
	// q.MailboxID is set) joins via message_mailboxes.
	terms := collectTerms(q)
	where := []string{"m.principal_id = $1"}
	args := []any{int64(principalID)}
	argIdx := 2
	if q.MailboxID != 0 {
		where = append(where, "EXISTS (SELECT 1 FROM message_mailboxes mm WHERE mm.message_id = m.id AND mm.mailbox_id = $"+itoa(argIdx)+")")
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
	query := `SELECT m.id FROM messages m WHERE ` +
		strings.Join(where, " AND ") +
		` ORDER BY m.received_at_us DESC LIMIT $` + itoa(argIdx)
	args = append(args, q.Limit)

	rows, err := f.s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envelopeHits []store.MessageRef
	envelopeHitSet := map[store.MessageID]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		mid := store.MessageID(id)
		envelopeHits = append(envelopeHits, store.MessageRef{
			MessageID: mid,
			Score:     1,
		})
		envelopeHitSet[mid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Augment with body-text matches recorded by IndexMessage. Production
	// FTS (Bleve) indexes body content with IncludeInAll=true so a body:
	// or text: filter sees body matches; the stub mirrors that for tests.
	bodyHits, err := f.queryBodyText(ctx, principalID, q, envelopeHitSet)
	if err != nil {
		return nil, err
	}
	return append(envelopeHits, bodyHits...), nil
}

// queryBodyText returns message IDs whose IndexMessage-supplied text
// contains every body-relevant query term. The match is conjunctive
// across q.Text, q.Body, and q.Subject/From/To/Cc terms, mirroring the
// envelope-side AND in Query. Skipped IDs already present in `seen` are
// not returned again.
func (f *ftsStub) queryBodyText(ctx context.Context, principalID store.PrincipalID, q store.Query, seen map[store.MessageID]struct{}) ([]store.MessageRef, error) {
	terms := collectTerms(q)
	if len(terms) == 0 {
		return nil, nil
	}
	f.mu.Lock()
	candidates := make(map[store.MessageID]string, len(f.bodyText))
	for k, v := range f.bodyText {
		candidates[k] = v
	}
	f.mu.Unlock()
	if len(candidates) == 0 {
		return nil, nil
	}
	// Filter to messages owned by principalID (and matching mailbox if set).
	where := []string{"principal_id = $1"}
	args := []any{int64(principalID)}
	if q.MailboxID != 0 {
		where = append(where, "EXISTS (SELECT 1 FROM message_mailboxes mm WHERE mm.message_id = id AND mm.mailbox_id = $2)")
		args = append(args, int64(q.MailboxID))
	}
	rows, err := f.s.pool.Query(ctx, "SELECT id FROM messages WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MessageRef
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		mid := store.MessageID(id)
		if _, ok := seen[mid]; ok {
			continue
		}
		text, ok := candidates[mid]
		if !ok {
			continue
		}
		matched := true
		for _, term := range terms {
			if !strings.Contains(text, strings.ToLower(term)) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, store.MessageRef{MessageID: mid, Score: 1})
		}
	}
	return out, rows.Err()
}

func (f *ftsStub) ReadChangeFeedForFTS(ctx context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	if max <= 0 {
		max = 1000
	}
	rows, err := f.s.pool.Query(ctx, `
		SELECT id, principal_id, entity_kind, entity_id, parent_entity_id, op, produced_at_us
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
		var kind string
		var op int16
		var eid, peid, prodUs int64
		if err := rows.Scan(&id, &pid, &kind, &eid, &peid, &op, &prodUs); err != nil {
			return nil, err
		}
		out = append(out, store.FTSChange{
			Seq:            uint64(id),
			PrincipalID:    store.PrincipalID(pid),
			Kind:           store.EntityKind(kind),
			EntityID:       uint64(eid),
			ParentEntityID: uint64(peid),
			Op:             store.ChangeOp(op),
			ProducedAt:     fromMicros(prodUs),
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
	out = append(out, q.Cc...)
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
