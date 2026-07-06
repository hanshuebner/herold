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
// mailboxes (via correlated subqueries for mailbox name and junk detection),
// and llm_classifications (LEFT JOIN for spam verdict). No body content is
// returned. The MailboxAttrJunk bit value (16) is 1 << 4 = store.MailboxAttrJunk.

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
	if filter.Subject != "" {
		addArg("lower(m.env_subject) LIKE lower('%%'||$%d||'%%')", filter.Subject)
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
		    m.env_subject,
		    m.env_from,
		    m.env_to,
		    m.env_cc,
		    m.env_bcc,
		    m.env_reply_to,
		    m.env_message_id,
		    m.env_in_reply_to,
		    m.env_references,
		    m.env_date_us,
		    COALESCE((
		        SELECT mb.name
		          FROM message_mailboxes mm2
		          JOIN mailboxes mb ON mb.id = mm2.mailbox_id
		         WHERE mm2.message_id = m.id
		         ORDER BY mm2.mailbox_id ASC
		         LIMIT 1
		    ), '') AS mailbox_name,
		    EXISTS (
		        SELECT 1
		          FROM message_mailboxes mm2
		          JOIN mailboxes mb ON mb.id = mm2.mailbox_id
		         WHERE mm2.message_id = m.id
		           AND (mb.attributes & ` + fmt.Sprintf("%d", mailboxAttrJunkBitPG) + `) != 0
		    ) AS is_junk,
		    lc.spam_verdict,
		    lc.spam_confidence
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
	return out, nil
}

// scanAdminMessageHitPG scans one row from the SearchAdminMessages query
// for the Postgres backend. pgx returns nil for NULL columns into *T
// pointers directly, so we use *string and *float64 for nullable fields.
func scanAdminMessageHitPG(row pgx.Row) (store.AdminMessageHit, error) {
	var hit store.AdminMessageHit
	var id, pid, rcvUs, envDateUs int64
	var isJunk bool
	var spamVerdict *string
	var spamConfidence *float64
	var envCc, envBcc, envReplyTo, envInReplyTo, envReferences string
	err := row.Scan(
		&id, &pid, &rcvUs,
		&hit.Envelope.Subject,
		&hit.Envelope.From,
		&hit.Envelope.To,
		&envCc, &envBcc, &envReplyTo,
		&hit.Envelope.MessageID,
		&envInReplyTo, &envReferences,
		&envDateUs,
		&hit.MailboxName,
		&isJunk,
		&spamVerdict,
		&spamConfidence,
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
	hit.IsJunk = isJunk
	hit.SpamVerdict = spamVerdict
	hit.SpamConfidence = spamConfidence
	return hit, nil
}
