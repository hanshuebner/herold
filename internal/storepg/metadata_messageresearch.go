package storepg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata.SearchAdminMessages for the Postgres
// backend (REQ-ADM-306, re #143).
//
// The query joins messages, principals (for domain scope via split_part),
// and llm_classifications (LEFT JOIN for spam verdict). Every mailbox a
// returned message currently sits in is fetched in a second, batched
// query (loadAdminMessageMailboxes) and used to derive
// AdminMessageHit.Mailboxes / MailboxName / IsJunk (maintainer finding
// #1, 2026-09-09: a message can sit in several mailboxes at once, and a
// single name/flag pair misrepresents that). No body content or subject
// text is ever returned. The MailboxAttrJunk bit value (16) is 1 << 4 =
// store.MailboxAttrJunk.

const mailboxAttrJunkBitPG = 16 // store.MailboxAttrJunk = 1 << 4

func (m *metadata) SearchAdminMessages(ctx context.Context, filter store.AdminMessageFilter) ([]store.AdminMessageHit, error) {
	// Fail closed: non-nil empty Domains means no access (REQ-ADM-307).
	if filter.Domains != nil && len(filter.Domains) == 0 {
		return nil, nil
	}

	limit := filter.Limit
	switch {
	case limit <= 0:
		limit = 100
	case limit > 1000:
		limit = 1000
	}

	var conds []string
	var args []any
	p := 1

	addArg := func(cond string, val any) {
		conds = append(conds, fmt.Sprintf(cond, p))
		args = append(args, val)
		p++
	}

	if filter.BeforeReceivedUs != 0 {
		addArg("m.received_at_us < $%d", filter.BeforeReceivedUs)
	}
	if !filter.DateFrom.IsZero() {
		addArg("m.received_at_us >= $%d", usMicros(filter.DateFrom))
	}
	if !filter.DateTo.IsZero() {
		addArg("m.received_at_us < $%d", usMicros(filter.DateTo))
	}
	if filter.Sender != "" {
		addArg("lower(m.env_from) LIKE lower('%%'||$%d||'%%')", filter.Sender)
	}
	if filter.Recipient != "" {
		addArg("lower(m.env_to) LIKE lower('%%'||$%d||'%%')", filter.Recipient)
	}
	if filter.MessageID != "" {
		addArg("lower(m.env_message_id) = lower($%d)", filter.MessageID)
	}
	if len(filter.Domains) > 0 {
		placeholders := make([]string, len(filter.Domains))
		for i, d := range filter.Domains {
			placeholders[i] = fmt.Sprintf("$%d", p)
			args = append(args, strings.ToLower(d))
			p++
		}
		conds = append(conds,
			"lower(split_part(p.canonical_email, '@', 2)) IN ("+
				strings.Join(placeholders, ",")+")")
	}

	var where string
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	limitPH := fmt.Sprintf("$%d", p)
	args = append(args, limit)

	q := `
		SELECT
		    m.id,
		    m.principal_id,
		    m.received_at_us,
		    m.env_from,
		    m.env_to,
		    m.env_cc,
		    m.env_bcc,
		    m.env_reply_to,
		    m.env_message_id,
		    m.env_in_reply_to,
		    m.env_references,
		    m.env_date_us,
		    lc.spam_verdict,
		    lc.spam_confidence,
		    m.delivery_disposition,
		    m.ingest_source,
		    m.ingest_source_ref
		FROM messages m
		JOIN principals p ON p.id = m.principal_id
		LEFT JOIN llm_classifications lc ON lc.message_id = m.id` +
		where + `
		ORDER BY m.received_at_us DESC, m.id DESC
		LIMIT ` + limitPH

	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("storepg: SearchAdminMessages: %w", mapErr(err))
	}
	defer rows.Close()

	var out []store.AdminMessageHit
	for rows.Next() {
		hit, err := scanAdminMessageHitPG(rows)
		if err != nil {
			return nil, fmt.Errorf("storepg: SearchAdminMessages scan: %w", err)
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storepg: SearchAdminMessages rows: %w", mapErr(err))
	}

	if err := m.loadAdminMessageMailboxes(ctx, out); err != nil {
		return nil, fmt.Errorf("storepg: SearchAdminMessages mailboxes: %w", err)
	}
	return out, nil
}

// loadAdminMessageMailboxes batch-fetches every mailbox each hit's
// message currently sits in and populates Mailboxes (ordered by name)
// plus the MailboxName/IsJunk compatibility fields derived from it.
func (m *metadata) loadAdminMessageMailboxes(ctx context.Context, hits []store.AdminMessageHit) error {
	if len(hits) == 0 {
		return nil
	}
	ids := make([]int64, len(hits))
	idx := make(map[store.MessageID]int, len(hits))
	for i, h := range hits {
		ids[i] = int64(h.MessageID)
		idx[h.MessageID] = i
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT mm.message_id, mb.name, mb.attributes
		  FROM message_mailboxes mm
		  JOIN mailboxes mb ON mb.id = mm.mailbox_id
		 WHERE mm.message_id = ANY($1)
		 ORDER BY mm.message_id, mb.name ASC`, ids)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var msgID, attrs int64
		var name string
		if err := rows.Scan(&msgID, &name, &attrs); err != nil {
			return mapErr(err)
		}
		i, ok := idx[store.MessageID(msgID)]
		if !ok {
			continue
		}
		hits[i].Mailboxes = append(hits[i].Mailboxes, store.AdminMessageMailbox{
			Name:   name,
			IsJunk: attrs&mailboxAttrJunkBitPG != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return mapErr(err)
	}
	for i := range hits {
		for _, mb := range hits[i].Mailboxes {
			if mb.IsJunk {
				hits[i].IsJunk = true
				break
			}
		}
		if len(hits[i].Mailboxes) > 0 {
			hits[i].MailboxName = hits[i].Mailboxes[0].Name
		}
	}
	return nil
}

// scanAdminMessageHitPG scans one row from the SearchAdminMessages query
// for the Postgres backend. pgx returns nil for NULL columns into *T
// pointers directly, so we use *string and *float64 for nullable fields.
// Subject is never scanned -- SearchAdminMessages does not select
// env_subject (REQ-ADM-306, maintainer finding #4).
func scanAdminMessageHitPG(row pgx.Row) (store.AdminMessageHit, error) {
	var hit store.AdminMessageHit
	var id, pid, rcvUs, envDateUs int64
	var spamVerdict *string
	var spamConfidence *float64
	var envCc, envBcc, envReplyTo, envInReplyTo, envReferences string
	var disposition string
	var ingestSource string
	err := row.Scan(
		&id, &pid, &rcvUs,
		&hit.Envelope.From,
		&hit.Envelope.To,
		&envCc, &envBcc, &envReplyTo,
		&hit.Envelope.MessageID,
		&envInReplyTo, &envReferences,
		&envDateUs,
		&spamVerdict,
		&spamConfidence,
		&disposition,
		&ingestSource,
		&hit.IngestSourceRef,
	)
	if err != nil {
		return store.AdminMessageHit{}, mapErr(err)
	}
	hit.MessageID = store.MessageID(id)
	hit.PrincipalID = store.PrincipalID(pid)
	hit.ReceivedAt = fromMicros(rcvUs)
	hit.Envelope.Cc = envCc
	hit.Envelope.Bcc = envBcc
	hit.Envelope.ReplyTo = envReplyTo
	hit.Envelope.InReplyTo = envInReplyTo
	hit.Envelope.References = envReferences
	hit.Envelope.Date = fromMicros(envDateUs)
	hit.Disposition = store.MessageDeliveryDisposition(disposition)
	hit.IngestSource = store.MessageIngestSource(ingestSource)
	hit.SpamVerdict = spamVerdict
	hit.SpamConfidence = spamConfidence
	return hit, nil
}
