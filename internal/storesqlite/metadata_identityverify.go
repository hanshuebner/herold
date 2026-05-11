package storesqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Identity verification store methods for the
// SQLite backend (REQ-IDENT-01..91). The verification trio
// (verification_token_hash, verification_code_hash,
// verification_token_expires_at_us) lives on jmap_identities; the
// schema layout is defined in migrations/0048_identity_verification.sql.

// IssueIdentityVerificationToken writes the verification trio on the
// identity row in a single atomic UPDATE. Returns ErrConflict when a
// token is already live (the caller picks Reset for the resend path),
// and ErrNotFound when the row is missing.
func (m *metadata) IssueIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error {
	if err := validateVerificationInputs(tokenHash, codeHash, expiresAtUs); err != nil {
		return err
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var existingToken []byte
		err := tx.QueryRowContext(ctx,
			`SELECT verification_token_hash FROM jmap_identities WHERE id = ?`,
			identityID).Scan(&existingToken)
		if err != nil {
			return mapErr(err)
		}
		if len(existingToken) > 0 {
			return fmt.Errorf("jmap identity %q already has a live verification token: %w",
				identityID, store.ErrConflict)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = ?,
			       verification_code_hash = ?,
			       verification_token_expires_at_us = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			tokenHash, codeHash, expiresAtUs,
			usMicros(m.s.clock.Now().UTC()), identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ResetIdentityVerificationToken replaces the verification trio
// unconditionally. Used by the resend path (REQ-IDENT-37).
func (m *metadata) ResetIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error {
	if err := validateVerificationInputs(tokenHash, codeHash, expiresAtUs); err != nil {
		return err
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = ?,
			       verification_code_hash = ?,
			       verification_token_expires_at_us = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			tokenHash, codeHash, expiresAtUs,
			usMicros(m.s.clock.Now().UTC()), identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// MarkIdentityVerified sets verified_at_us = now and clears the
// verification trio. Idempotent at the wire layer; this method always
// writes verified_at_us and NULLs the token columns regardless of prior
// state.
func (m *metadata) MarkIdentityVerified(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		now := usMicros(m.s.clock.Now().UTC())
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verified_at_us = ?,
			       verification_token_hash = NULL,
			       verification_code_hash = NULL,
			       verification_token_expires_at_us = NULL,
			       updated_at_us = ?
			 WHERE id = ?`,
			now, now, identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ClearIdentityVerificationToken nulls the verification trio without
// touching verified_at_us. Used by the GC pass that purges expired
// tokens whose Identity rows are still inside the 7-day window.
func (m *metadata) ClearIdentityVerificationToken(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		now := usMicros(m.s.clock.Now().UTC())
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = NULL,
			       verification_code_hash = NULL,
			       verification_token_expires_at_us = NULL,
			       updated_at_us = ?
			 WHERE id = ?`,
			now, identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// GetIdentityByVerificationTokenHash returns the identity row whose
// stored verification_token_hash matches tokenHash. The match is global
// across all identities: the link callback URL only carries the raw
// token, not the identity id.
func (m *metadata) GetIdentityByVerificationTokenHash(ctx context.Context, tokenHash []byte) (store.JMAPIdentity, error) {
	if len(tokenHash) == 0 {
		return store.JMAPIdentity{}, fmt.Errorf("%w: empty verification token hash", store.ErrInvalidArgument)
	}
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+`
		   FROM jmap_identities
		  WHERE verification_token_hash = ?`,
		tokenHash)
	return scanJMAPIdentity(row)
}

// GetIdentityByVerificationCodeHash returns the identity row whose id
// is identityID and whose verification_code_hash matches codeHash. The
// scope is intentional: the 6-digit code may repeat across identities
// (the URL the suite calls carries the id, so the server need not
// disambiguate across rows).
func (m *metadata) GetIdentityByVerificationCodeHash(ctx context.Context, identityID string, codeHash []byte) (store.JMAPIdentity, error) {
	if len(codeHash) == 0 {
		return store.JMAPIdentity{}, fmt.Errorf("%w: empty verification code hash", store.ErrInvalidArgument)
	}
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+`
		   FROM jmap_identities
		  WHERE id = ? AND verification_code_hash = ?`,
		identityID, codeHash)
	out, err := scanJMAPIdentity(row)
	if err != nil {
		return store.JMAPIdentity{}, err
	}
	// Defence in depth: the (id, hash) predicate above is authoritative,
	// but the constant-time compare protects against any future code
	// path that loads the row first and then checks. The hashes are
	// already constant-length (sha256), so bytes.Equal is sufficient.
	if !bytes.Equal(out.VerificationCodeHash, codeHash) {
		return store.JMAPIdentity{}, store.ErrNotFound
	}
	return out, nil
}

// ListUnverifiedIdentitiesOlderThan returns identities whose
// verified_at_us IS NULL AND created_at_us < before, in ascending
// created_at_us order. The GC pass walks the returned slice and calls
// DeleteJMAPIdentity on each (REQ-IDENT-35).
func (m *metadata) ListUnverifiedIdentitiesOlderThan(ctx context.Context, before time.Time) ([]store.JMAPIdentity, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+`
		   FROM jmap_identities
		  WHERE verified_at_us IS NULL AND created_at_us < ?
		  ORDER BY created_at_us ASC, id ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListExpiredVerificationTokens returns identities whose token has
// expired but whose row may still be live (within the 7-day unverified-
// purge window). The GC pass calls ClearIdentityVerificationToken on
// each.
func (m *metadata) ListExpiredVerificationTokens(ctx context.Context, before time.Time) ([]store.JMAPIdentity, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+jmapIdentitySelectColumns+`
		   FROM jmap_identities
		  WHERE verification_token_expires_at_us IS NOT NULL
		    AND verification_token_expires_at_us < ?
		  ORDER BY verification_token_expires_at_us ASC, id ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// validateVerificationInputs rejects obviously-invalid inputs at the
// store boundary. The hashes must be the sha256 width (32 bytes) and
// the expiry must be a positive unix-micros value. Tests rely on these
// being checked before any database mutation.
func validateVerificationInputs(tokenHash, codeHash []byte, expiresAtUs int64) error {
	if len(tokenHash) != 32 {
		return fmt.Errorf("%w: verification token hash must be 32 bytes (got %d)", store.ErrInvalidArgument, len(tokenHash))
	}
	if len(codeHash) != 32 {
		return fmt.Errorf("%w: verification code hash must be 32 bytes (got %d)", store.ErrInvalidArgument, len(codeHash))
	}
	if expiresAtUs <= 0 {
		return fmt.Errorf("%w: verification token expiry must be positive", store.ErrInvalidArgument)
	}
	return nil
}
