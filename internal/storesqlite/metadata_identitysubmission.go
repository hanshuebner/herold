package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the identity_submission store methods
// (REQ-AUTH-EXT-SUBMIT-01..10) for the SQLite backend.
// Schema commentary lives in migrations/0032_identity_submission.sql.

const identitySubmissionSelectCols = `
	identity_id, submit_host, submit_port, submit_security, submit_auth_method,
	password_ct, oauth_access_ct, oauth_refresh_ct,
	oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
	oauth_expires_at_us, refresh_due_us,
	state, state_at_us, created_at_us, updated_at_us`

func scanIdentitySubmission(row rowLike) (store.IdentitySubmission, error) {
	var (
		identityID, host, security, authMethod string
		port                                   int64
		passwordCT, accessCT, refreshCT        []byte
		tokenEndpoint, clientID                sql.NullString
		clientSecretCT                         []byte
		expiresUs, refreshDueUs                sql.NullInt64
		state                                  string
		stateAtUs, createdUs, updatedUs        int64
	)

	err := row.Scan(
		&identityID, &host, &port, &security, &authMethod,
		&passwordCT, &accessCT, &refreshCT,
		&tokenEndpoint, &clientID, &clientSecretCT,
		&expiresUs, &refreshDueUs,
		&state, &stateAtUs, &createdUs, &updatedUs,
	)
	if err != nil {
		return store.IdentitySubmission{}, mapErr(err)
	}
	sub := store.IdentitySubmission{
		IdentityID:          identityID,
		SubmitHost:          host,
		SubmitPort:          int(port),
		SubmitSecurity:      security,
		SubmitAuthMethod:    authMethod,
		PasswordCT:          nullableBytes(passwordCT),
		OAuthAccessCT:       nullableBytes(accessCT),
		OAuthRefreshCT:      nullableBytes(refreshCT),
		OAuthClientSecretCT: nullableBytes(clientSecretCT),
		State:               store.IdentitySubmissionState(state),
		StateAt:             fromMicros(stateAtUs),
		CreatedAt:           fromMicros(createdUs),
		UpdatedAt:           fromMicros(updatedUs),
	}
	if tokenEndpoint.Valid {
		sub.OAuthTokenEndpoint = tokenEndpoint.String
	}
	if clientID.Valid {
		sub.OAuthClientID = clientID.String
	}
	if expiresUs.Valid {
		sub.OAuthExpiresAt = fromMicros(expiresUs.Int64)
	}
	if refreshDueUs.Valid {
		sub.RefreshDue = fromMicros(refreshDueUs.Int64)
	}
	return sub, nil
}

// nullableBytes returns nil when b is empty (SQLite NULLs scan as nil []byte).
func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (m *metadata) UpsertIdentitySubmission(ctx context.Context, sub store.IdentitySubmission) error {
	if err := store.ValidateIdentitySubmissionCTs(sub); err != nil {
		return err
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	if sub.StateAt.IsZero() {
		sub.StateAt = now
	}
	if sub.State == "" {
		sub.State = store.IdentitySubmissionStateOK
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Verify the identity row exists (FK is enforced by SQLite, but we
		// want a typed ErrNotFound rather than a generic constraint error).
		var dummy string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM jmap_identities WHERE id = ?`, sub.IdentityID).Scan(&dummy); err != nil {
			return mapErr(err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO identity_submission
			  (identity_id, submit_host, submit_port, submit_security, submit_auth_method,
			   password_ct, oauth_access_ct, oauth_refresh_ct,
			   oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
			   oauth_expires_at_us, refresh_due_us,
			   state, state_at_us, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(identity_id) DO UPDATE SET
			  submit_host            = excluded.submit_host,
			  submit_port            = excluded.submit_port,
			  submit_security        = excluded.submit_security,
			  submit_auth_method     = excluded.submit_auth_method,
			  password_ct            = excluded.password_ct,
			  oauth_access_ct        = excluded.oauth_access_ct,
			  oauth_refresh_ct       = excluded.oauth_refresh_ct,
			  oauth_token_endpoint   = excluded.oauth_token_endpoint,
			  oauth_client_id        = excluded.oauth_client_id,
			  oauth_client_secret_ct = excluded.oauth_client_secret_ct,
			  oauth_expires_at_us    = excluded.oauth_expires_at_us,
			  refresh_due_us         = excluded.refresh_due_us,
			  state                  = excluded.state,
			  state_at_us            = excluded.state_at_us,
			  updated_at_us          = ?`,
			sub.IdentityID,
			sub.SubmitHost, sub.SubmitPort, sub.SubmitSecurity, sub.SubmitAuthMethod,
			nullOrBytes(sub.PasswordCT),
			nullOrBytes(sub.OAuthAccessCT),
			nullOrBytes(sub.OAuthRefreshCT),
			nullOrString(sub.OAuthTokenEndpoint),
			nullOrString(sub.OAuthClientID),
			nullOrBytes(sub.OAuthClientSecretCT),
			nullOrMicros(sub.OAuthExpiresAt),
			nullOrMicros(sub.RefreshDue),
			string(sub.State), usMicros(sub.StateAt),
			usMicros(sub.CreatedAt), nowUs,
			// updated_at_us for the ON CONFLICT DO UPDATE SET clause:
			nowUs,
		)
		return mapErr(err)
	})
}

func (m *metadata) GetIdentitySubmission(ctx context.Context, identityID string) (store.IdentitySubmission, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+identitySubmissionSelectCols+` FROM identity_submission WHERE identity_id = ?`,
		identityID)
	return scanIdentitySubmission(row)
}

func (m *metadata) DeleteIdentitySubmission(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM identity_submission WHERE identity_id = ?`, identityID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: DeleteIdentitySubmission rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListIdentitySubmissionsDue(ctx context.Context, before time.Time) ([]store.IdentitySubmission, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+identitySubmissionSelectCols+`
		  FROM identity_submission
		 WHERE refresh_due_us IS NOT NULL AND refresh_due_us <= ?
		 ORDER BY refresh_due_us ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.IdentitySubmission
	for rows.Next() {
		sub, err := scanIdentitySubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// MaterializeDefaultIdentity ensures a persisted jmap_identities row exists
// for the principal's synthesised default identity (idempotent).
// The default identity uses the principal's canonical email as the From
// address. The returned string is the row id (wire form used as identity_id
// in identity_submission).
//
// SQLite's single-writer model serialises concurrent calls by construction,
// so no TOCTOU retry is needed here (unlike the Postgres implementation).
func (m *metadata) MaterializeDefaultIdentity(ctx context.Context, principalID store.PrincipalID) (string, error) {
	var identityID string
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Check whether a row already exists whose email matches the principal's
		// canonical email and whose id is the synthesised "default" token OR any
		// numeric row already marked as this principal's default (may_delete=0).
		// The simplest deterministic approach: look for a row with may_delete=0
		// owned by this principal (there can be at most one such row per principal
		// because the default identity is unique).
		var existing string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM jmap_identities
			 WHERE principal_id = ? AND may_delete = 0
			 LIMIT 1`, int64(principalID)).Scan(&existing)
		if err == nil {
			// Row already exists; return it.
			identityID = existing
			return nil
		}
		if err != sql.ErrNoRows {
			return mapErr(err)
		}

		// No persisted default identity yet. Fetch the principal's canonical email.
		var email, displayName string
		if err := tx.QueryRowContext(ctx,
			`SELECT canonical_email, display_name FROM principals WHERE id = ?`,
			int64(principalID)).Scan(&email, &displayName); err != nil {
			return mapErr(err)
		}

		now := m.s.clock.Now().UTC()
		nowUs := usMicros(now)
		// Insert a new jmap_identities row for the default identity.
		// We need a text ID. SQLite assigns a ROWID; we read it back.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO jmap_identities
			  (id, principal_id, name, email, reply_to_json, bcc_json,
			   text_signature, html_signature, may_delete, created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, x'', x'', '', '', 0, ?, ?)`,
			// Use "default" as a sentinel ID that we replace with the ROWID
			// immediately after insert. We can't know the ROWID before insert;
			// use a two-step approach: insert with a placeholder, then update.
			// Actually SQLite doesn't support deferred self-referencing PKs.
			// Better approach: use the ROWID directly.
			// We first insert with a temporary id, get the rowid, then update id.
			"_default_placeholder_",
			int64(principalID), displayName, email,
			nowUs, nowUs)
		if err != nil {
			return mapErr(err)
		}
		rowID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: MaterializeDefaultIdentity: last insert id: %w", err)
		}
		// Now update the id column to the decimal rowID string.
		newID := strconv.FormatInt(rowID, 10)
		if _, err := tx.ExecContext(ctx,
			`UPDATE jmap_identities SET id = ? WHERE rowid = ?`, newID, rowID); err != nil {
			return mapErr(err)
		}
		identityID = newID
		return nil
	})
	if err != nil {
		return "", err
	}
	return identityID, nil
}

// nullOrBytes returns nil when b is nil or empty, otherwise b.
// Used for optional BLOB columns.
func nullOrBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// nullOrString returns nil when s is empty, otherwise s.
// Used for optional TEXT columns.
func nullOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullOrMicros returns nil when t is the zero value, otherwise usMicros(t).
// Used for optional timestamp columns.
func nullOrMicros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return usMicros(t)
}
