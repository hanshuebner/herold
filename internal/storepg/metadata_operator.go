package storepg

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata delegated-operator methods
// (REQ-ADM-307, re #145, re #237). A principal's managed-domain set is a
// domain:operator (or higher) grant on the domain resource (epic #182,
// metadata_grants.go); this file only lists the operator principals
// themselves. Migration 0099 dropped the legacy principal_managed_domains
// association table.

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
