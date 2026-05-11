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
//
// The first-issue path also initialises the resend bookkeeping
// (REQ-IDENT-36): verify_last_issued_at_us and verify_window_started_at_us
// are set to now, verify_window_count is set to 1. The Identity has
// burned one of its `resend_daily_cap` issuances; the next user-initiated
// resend (via RotateIdentityVerificationToken) increments from there.
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
		nowUs := usMicros(m.s.clock.Now().UTC())
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = ?,
			       verification_code_hash = ?,
			       verification_token_expires_at_us = ?,
			       verify_last_issued_at_us = ?,
			       verify_window_started_at_us = ?,
			       verify_window_count = 1,
			       updated_at_us = ?
			 WHERE id = ?`,
			tokenHash, codeHash, expiresAtUs,
			nowUs, nowUs, nowUs, identityID)
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

// UnmarkIdentityVerified clears verified_at_us on the identity row
// (REQ-IDENT-51). Used by the admin CLI to revert a verified Identity
// to unverified. Does not touch the verification token trio.
func (m *metadata) UnmarkIdentityVerified(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		now := usMicros(m.s.clock.Now().UTC())
		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verified_at_us = NULL,
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

// GetVerificationResendStats returns the per-Identity resend bookkeeping
// for the rate-limit gate (REQ-IDENT-36). Returns ErrNotFound when the
// identity row is missing.
func (m *metadata) GetVerificationResendStats(ctx context.Context, identityID string) (store.VerificationResendStats, error) {
	var (
		lastIssued    sql.NullInt64
		windowStarted sql.NullInt64
		windowCount   sql.NullInt64
	)
	err := m.s.db.QueryRowContext(ctx, `
		SELECT verify_last_issued_at_us,
		       verify_window_started_at_us,
		       verify_window_count
		  FROM jmap_identities WHERE id = ?`, identityID).Scan(
		&lastIssued, &windowStarted, &windowCount)
	if err != nil {
		return store.VerificationResendStats{}, mapErr(err)
	}
	out := store.VerificationResendStats{}
	if lastIssued.Valid {
		out.LastIssuedAtUs = lastIssued.Int64
	}
	if windowStarted.Valid {
		out.WindowStartedAtUs = windowStarted.Int64
	}
	if windowCount.Valid {
		out.WindowCount = int(windowCount.Int64)
	}
	return out, nil
}

// RotateIdentityVerificationToken replaces the verification trio AND
// updates the resend bookkeeping in a single transaction (REQ-IDENT-36,
// REQ-IDENT-37). The store-injected clock is the source of truth for
// "now"; the operator-tunable cooldown / daily cap thresholds flow in
// as arguments. The rate-limit checks run inside the transaction so the
// counter increment and the gate decision cannot race.
func (m *metadata) RotateIdentityVerificationToken(
	ctx context.Context,
	identityID string,
	tokenHash, codeHash []byte,
	expiresAtUs int64,
	cooldown time.Duration,
	dailyCap int,
) error {
	if err := validateVerificationInputs(tokenHash, codeHash, expiresAtUs); err != nil {
		return err
	}
	if cooldown < 0 {
		cooldown = 0
	}
	if dailyCap < 0 {
		dailyCap = 0
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			lastIssued    sql.NullInt64
			windowStarted sql.NullInt64
			windowCount   sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `
			SELECT verify_last_issued_at_us,
			       verify_window_started_at_us,
			       verify_window_count
			  FROM jmap_identities WHERE id = ?`, identityID).Scan(
			&lastIssued, &windowStarted, &windowCount)
		if err != nil {
			return mapErr(err)
		}

		// Cooldown gate: a resend within `cooldown` of the most recent
		// issue is rejected. The check is skipped when cooldown == 0
		// (operator-disabled or no prior issuance).
		if cooldown > 0 && lastIssued.Valid && lastIssued.Int64 > 0 {
			cooldownEnds := lastIssued.Int64 + cooldown.Microseconds()
			if nowUs < cooldownEnds {
				return fmt.Errorf("verification resend cooldown not yet elapsed: %w", store.ErrRateLimited)
			}
		}

		// Daily-cap gate: count issuances inside the trailing 24h
		// window. If the window started more than 24h ago, the
		// counter resets to 1 on this issuance; otherwise the counter
		// increments. The post-increment value is compared against
		// dailyCap.
		newWindowStartedUs := nowUs
		newWindowCount := int64(1)
		if windowStarted.Valid && windowStarted.Int64 > 0 {
			windowAge := now.Sub(time.UnixMicro(windowStarted.Int64))
			if windowAge < 24*time.Hour {
				newWindowStartedUs = windowStarted.Int64
				prior := int64(0)
				if windowCount.Valid {
					prior = windowCount.Int64
				}
				newWindowCount = prior + 1
			}
		}
		if dailyCap > 0 && newWindowCount > int64(dailyCap) {
			return fmt.Errorf("verification resend daily cap exhausted: %w", store.ErrRateLimited)
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE jmap_identities
			   SET verification_token_hash = ?,
			       verification_code_hash = ?,
			       verification_token_expires_at_us = ?,
			       verify_last_issued_at_us = ?,
			       verify_window_started_at_us = ?,
			       verify_window_count = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			tokenHash, codeHash, expiresAtUs,
			nowUs, newWindowStartedUs, newWindowCount,
			nowUs, identityID)
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
