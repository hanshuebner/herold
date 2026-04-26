package storesqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// -- Phase 3 Wave 3.5c inbound attachment policy (REQ-FLOW-ATTPOL-01) --

func (m *metadata) GetInboundAttachmentPolicy(
	ctx context.Context,
	address string,
) (store.InboundAttachmentPolicyRow, error) {
	address = strings.TrimSpace(strings.ToLower(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return store.InboundAttachmentPolicyRow{Policy: store.AttPolicyAccept}, nil
	}
	domain := address[at+1:]

	// Per-recipient row first.
	var pol, txt string
	err := m.s.db.QueryRowContext(ctx,
		`SELECT policy, reject_text FROM inbound_attpol_recipient WHERE address = ?`,
		address).Scan(&pol, &txt)
	if err == nil {
		p := store.ParseInboundAttachmentPolicy(pol)
		if p == store.AttPolicyUnset {
			p = store.AttPolicyAccept
		}
		return store.InboundAttachmentPolicyRow{Policy: p, RejectText: txt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.InboundAttachmentPolicyRow{}, mapErr(err)
	}

	// Fall back to domain row.
	err = m.s.db.QueryRowContext(ctx,
		`SELECT policy, reject_text FROM inbound_attpol_domain WHERE domain = ?`,
		domain).Scan(&pol, &txt)
	if err == nil {
		p := store.ParseInboundAttachmentPolicy(pol)
		if p == store.AttPolicyUnset {
			p = store.AttPolicyAccept
		}
		return store.InboundAttachmentPolicyRow{Policy: p, RejectText: txt}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.InboundAttachmentPolicyRow{}, mapErr(err)
	}

	// Implicit default.
	return store.InboundAttachmentPolicyRow{Policy: store.AttPolicyAccept}, nil
}

func (m *metadata) SetInboundAttachmentPolicyRecipient(
	ctx context.Context,
	address string,
	row store.InboundAttachmentPolicyRow,
) error {
	address = strings.TrimSpace(strings.ToLower(address))
	if address == "" {
		return errors.New("storesqlite: empty address")
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if row.Policy == store.AttPolicyUnset {
			_, err := tx.ExecContext(ctx,
				`DELETE FROM inbound_attpol_recipient WHERE address = ?`, address)
			return mapErr(err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO inbound_attpol_recipient (address, policy, reject_text, updated_at_us)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(address) DO UPDATE SET
			    policy = excluded.policy,
			    reject_text = excluded.reject_text,
			    updated_at_us = excluded.updated_at_us`,
			address, row.Policy.String(), row.RejectText, usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) SetInboundAttachmentPolicyDomain(
	ctx context.Context,
	domain string,
	row store.InboundAttachmentPolicyRow,
) error {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return errors.New("storesqlite: empty domain")
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if row.Policy == store.AttPolicyUnset {
			_, err := tx.ExecContext(ctx,
				`DELETE FROM inbound_attpol_domain WHERE domain = ?`, domain)
			return mapErr(err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO inbound_attpol_domain (domain, policy, reject_text, updated_at_us)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(domain) DO UPDATE SET
			    policy = excluded.policy,
			    reject_text = excluded.reject_text,
			    updated_at_us = excluded.updated_at_us`,
			domain, row.Policy.String(), row.RejectText, usMicros(now))
		return mapErr(err)
	})
}
