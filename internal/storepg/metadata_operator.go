package storepg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata delegated-operator methods
// (REQ-ADM-307, re #145). Schema commentary lives in
// migrations/0072_principal_managed_domains.sql.

func (m *metadata) AssignManagedDomain(ctx context.Context, principalID store.PrincipalID, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO principal_managed_domains (principal_id, domain)
			 VALUES ($1, $2)
			 ON CONFLICT (principal_id, domain) DO NOTHING`,
			int64(principalID), domain)
		return mapErr(err)
	})
}

func (m *metadata) RevokeManagedDomain(ctx context.Context, principalID store.PrincipalID, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`DELETE FROM principal_managed_domains WHERE principal_id = $1 AND domain = $2`,
			int64(principalID), domain)
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListManagedDomains(ctx context.Context, principalID store.PrincipalID) ([]string, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT domain FROM principal_managed_domains
		 WHERE principal_id = $1
		 ORDER BY domain`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var domains []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, mapErr(err)
		}
		domains = append(domains, d)
	}
	return domains, mapErr(rows.Err())
}

// pgFlagAdmin and pgFlagSuperAdmin are the decimal values of
// PrincipalFlagAdmin and PrincipalFlagSuperAdmin stored in the flags column.
// Using literals here avoids an import cycle; the values must stay in sync
// with store/types.go.
const (
	pgFlagAdmin      = int64(4)  // PrincipalFlagAdmin = 1 << 2
	pgFlagSuperAdmin = int64(32) // PrincipalFlagSuperAdmin = 1 << 5
)

func (m *metadata) ListDomainOperators(ctx context.Context) ([]store.Principal, error) {
	// Domain-scoped operators have PrincipalFlagAdmin set but NOT
	// PrincipalFlagSuperAdmin.
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, kind, canonical_email, display_name, password_hash, totp_secret,
		       quota_bytes, flags, created_at_us, updated_at_us
		  FROM principals
		 WHERE (flags & $1) != 0
		   AND (flags & $2) = 0
		 ORDER BY id`,
		pgFlagAdmin, pgFlagSuperAdmin)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Principal
	for rows.Next() {
		var p store.Principal
		var id int64
		var kind int32
		var flags int64
		var createdUs, updatedUs int64
		var totp []byte
		if err := rows.Scan(&id, &kind, &p.CanonicalEmail, &p.DisplayName,
			&p.PasswordHash, &totp, &p.QuotaBytes, &flags,
			&createdUs, &updatedUs); err != nil {
			return nil, mapErr(err)
		}
		p.ID = store.PrincipalID(id)
		p.Kind = store.PrincipalKind(kind)
		p.Flags = store.PrincipalFlags(flags)
		p.CreatedAt = fromMicros(createdUs)
		p.UpdatedAt = fromMicros(updatedUs)
		if len(totp) > 0 {
			p.TOTPSecret = totp
		}
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}
