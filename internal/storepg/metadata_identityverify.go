package storepg

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Identity verification store methods for the
// Postgres backend (REQ-IDENT-01..91). Mirrors
// internal/storesqlite/metadata_identityverify.go. Schema layout lives
// in migrations/0048_identity_verification.sql.

func (m *metadata) IssueIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error {
	if err := validateVerificationInputs(tokenHash, codeHash, expiresAtUs); err != nil {
		return err
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var existingToken []byte
		err := tx.QueryRow(ctx,
			`SELECT verification_token_hash FROM jmap_identities WHERE id = $1`,
			identityID).Scan(&existingToken)
		if err != nil {
			return mapErr(err)
		}
		if len(existingToken) > 0 {
			return fmt.Errorf("jmap identity %q already has a live verification token: %w",
				identityID, store.ErrConflict)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = $1,
			       verification_code_hash = $2,
			       verification_token_expires_at_us = $3,
			       updated_at_us = $4
			 WHERE id = $5`,
			tokenHash, codeHash, expiresAtUs,
			usMicros(m.s.clock.Now().UTC()), identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ResetIdentityVerificationToken(ctx context.Context, identityID string, tokenHash, codeHash []byte, expiresAtUs int64) error {
	if err := validateVerificationInputs(tokenHash, codeHash, expiresAtUs); err != nil {
		return err
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = $1,
			       verification_code_hash = $2,
			       verification_token_expires_at_us = $3,
			       updated_at_us = $4
			 WHERE id = $5`,
			tokenHash, codeHash, expiresAtUs,
			usMicros(m.s.clock.Now().UTC()), identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) MarkIdentityVerified(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		now := usMicros(m.s.clock.Now().UTC())
		tag, err := tx.Exec(ctx, `
			UPDATE jmap_identities
			   SET verified_at_us = $1,
			       verification_token_hash = NULL,
			       verification_code_hash = NULL,
			       verification_token_expires_at_us = NULL,
			       updated_at_us = $2
			 WHERE id = $3`,
			now, now, identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ClearIdentityVerificationToken(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		now := usMicros(m.s.clock.Now().UTC())
		tag, err := tx.Exec(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = NULL,
			       verification_code_hash = NULL,
			       verification_token_expires_at_us = NULL,
			       updated_at_us = $1
			 WHERE id = $2`,
			now, identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) GetIdentityByVerificationTokenHash(ctx context.Context, tokenHash []byte) (store.JMAPIdentity, error) {
	if len(tokenHash) == 0 {
		return store.JMAPIdentity{}, fmt.Errorf("%w: empty verification token hash", store.ErrInvalidArgument)
	}
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+`
		   FROM jmap_identities
		  WHERE verification_token_hash = $1`,
		tokenHash)
	return scanJMAPIdentityPG(row)
}

func (m *metadata) GetIdentityByVerificationCodeHash(ctx context.Context, identityID string, codeHash []byte) (store.JMAPIdentity, error) {
	if len(codeHash) == 0 {
		return store.JMAPIdentity{}, fmt.Errorf("%w: empty verification code hash", store.ErrInvalidArgument)
	}
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+`
		   FROM jmap_identities
		  WHERE id = $1 AND verification_code_hash = $2`,
		identityID, codeHash)
	out, err := scanJMAPIdentityPG(row)
	if err != nil {
		return store.JMAPIdentity{}, err
	}
	if !bytes.Equal(out.VerificationCodeHash, codeHash) {
		return store.JMAPIdentity{}, store.ErrNotFound
	}
	return out, nil
}

func (m *metadata) ListUnverifiedIdentitiesOlderThan(ctx context.Context, before time.Time) ([]store.JMAPIdentity, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+`
		   FROM jmap_identities
		  WHERE verified_at_us IS NULL AND created_at_us < $1
		  ORDER BY created_at_us ASC, id ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentityPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) ListExpiredVerificationTokens(ctx context.Context, before time.Time) ([]store.JMAPIdentity, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+jmapIdentitySelectColumnsPG+`
		   FROM jmap_identities
		  WHERE verification_token_expires_at_us IS NOT NULL
		    AND verification_token_expires_at_us < $1
		  ORDER BY verification_token_expires_at_us ASC, id ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.JMAPIdentity
	for rows.Next() {
		r, err := scanJMAPIdentityPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// validateVerificationInputs rejects obviously-invalid inputs at the
// store boundary. Mirrors the sqlite helper.
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
