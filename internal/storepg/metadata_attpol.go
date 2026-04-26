package storepg

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

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

	var pol, txt string
	err := m.s.pool.QueryRow(ctx,
		`SELECT policy, reject_text FROM inbound_attpol_recipient WHERE address = $1`,
		address).Scan(&pol, &txt)
	if err == nil {
		p := store.ParseInboundAttachmentPolicy(pol)
		if p == store.AttPolicyUnset {
			p = store.AttPolicyAccept
		}
		return store.InboundAttachmentPolicyRow{Policy: p, RejectText: txt}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.InboundAttachmentPolicyRow{}, mapErr(err)
	}

	err = m.s.pool.QueryRow(ctx,
		`SELECT policy, reject_text FROM inbound_attpol_domain WHERE domain = $1`,
		domain).Scan(&pol, &txt)
	if err == nil {
		p := store.ParseInboundAttachmentPolicy(pol)
		if p == store.AttPolicyUnset {
			p = store.AttPolicyAccept
		}
		return store.InboundAttachmentPolicyRow{Policy: p, RejectText: txt}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.InboundAttachmentPolicyRow{}, mapErr(err)
	}

	return store.InboundAttachmentPolicyRow{Policy: store.AttPolicyAccept}, nil
}

func (m *metadata) SetInboundAttachmentPolicyRecipient(
	ctx context.Context,
	address string,
	row store.InboundAttachmentPolicyRow,
) error {
	address = strings.TrimSpace(strings.ToLower(address))
	if address == "" {
		return errors.New("storepg: empty address")
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		if row.Policy == store.AttPolicyUnset {
			_, err := tx.Exec(ctx,
				`DELETE FROM inbound_attpol_recipient WHERE address = $1`, address)
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO inbound_attpol_recipient (address, policy, reject_text, updated_at_us)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (address) DO UPDATE SET
			    policy = EXCLUDED.policy,
			    reject_text = EXCLUDED.reject_text,
			    updated_at_us = EXCLUDED.updated_at_us`,
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
		return errors.New("storepg: empty domain")
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		if row.Policy == store.AttPolicyUnset {
			_, err := tx.Exec(ctx,
				`DELETE FROM inbound_attpol_domain WHERE domain = $1`, domain)
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO inbound_attpol_domain (domain, policy, reject_text, updated_at_us)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (domain) DO UPDATE SET
			    policy = EXCLUDED.policy,
			    reject_text = EXCLUDED.reject_text,
			    updated_at_us = EXCLUDED.updated_at_us`,
			domain, row.Policy.String(), row.RejectText, usMicros(now))
		return mapErr(err)
	})
}
