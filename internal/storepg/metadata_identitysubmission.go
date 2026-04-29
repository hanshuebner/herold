package storepg

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the identity_submission store methods
// (REQ-AUTH-EXT-SUBMIT-01..10) for the Postgres backend.
// Schema commentary lives in migrations/0032_identity_submission.sql.

const identitySubmissionSelectColsPG = `
	identity_id, submit_host, submit_port, submit_security, submit_auth_method,
	password_ct, oauth_access_ct, oauth_refresh_ct,
	oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
	oauth_expires_at_us, refresh_due_us,
	state, state_at_us, created_at_us, updated_at_us`

func scanIdentitySubmissionPG(row pgx.Row) (store.IdentitySubmission, error) {
	var (
		identityID, host, security, authMethod string
		port                                   int64
		passwordCT, accessCT, refreshCT        []byte
		tokenEndpoint, clientID                *string
		clientSecretCT                         []byte
		expiresUs, refreshDueUs                *int64
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
		PasswordCT:          nilSafeBytes(passwordCT),
		OAuthAccessCT:       nilSafeBytes(accessCT),
		OAuthRefreshCT:      nilSafeBytes(refreshCT),
		OAuthClientSecretCT: nilSafeBytes(clientSecretCT),
		State:               store.IdentitySubmissionState(state),
		StateAt:             fromMicros(stateAtUs),
		CreatedAt:           fromMicros(createdUs),
		UpdatedAt:           fromMicros(updatedUs),
	}
	if tokenEndpoint != nil {
		sub.OAuthTokenEndpoint = *tokenEndpoint
	}
	if clientID != nil {
		sub.OAuthClientID = *clientID
	}
	if expiresUs != nil {
		sub.OAuthExpiresAt = fromMicros(*expiresUs)
	}
	if refreshDueUs != nil {
		sub.RefreshDue = fromMicros(*refreshDueUs)
	}
	return sub, nil
}

// nilSafeBytes returns nil when b is nil or empty (pgx BYTEA NULL scans as
// nil []byte).
func nilSafeBytes(b []byte) []byte {
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		// Verify the identity row exists.
		var dummy string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM jmap_identities WHERE id = $1`, sub.IdentityID).Scan(&dummy); err != nil {
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO identity_submission
			  (identity_id, submit_host, submit_port, submit_security, submit_auth_method,
			   password_ct, oauth_access_ct, oauth_refresh_ct,
			   oauth_token_endpoint, oauth_client_id, oauth_client_secret_ct,
			   oauth_expires_at_us, refresh_due_us,
			   state, state_at_us, created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (identity_id) DO UPDATE SET
			  submit_host            = EXCLUDED.submit_host,
			  submit_port            = EXCLUDED.submit_port,
			  submit_security        = EXCLUDED.submit_security,
			  submit_auth_method     = EXCLUDED.submit_auth_method,
			  password_ct            = EXCLUDED.password_ct,
			  oauth_access_ct        = EXCLUDED.oauth_access_ct,
			  oauth_refresh_ct       = EXCLUDED.oauth_refresh_ct,
			  oauth_token_endpoint   = EXCLUDED.oauth_token_endpoint,
			  oauth_client_id        = EXCLUDED.oauth_client_id,
			  oauth_client_secret_ct = EXCLUDED.oauth_client_secret_ct,
			  oauth_expires_at_us    = EXCLUDED.oauth_expires_at_us,
			  refresh_due_us         = EXCLUDED.refresh_due_us,
			  state                  = EXCLUDED.state,
			  state_at_us            = EXCLUDED.state_at_us,
			  updated_at_us          = $17`,
			sub.IdentityID,
			sub.SubmitHost, sub.SubmitPort, sub.SubmitSecurity, sub.SubmitAuthMethod,
			pgNullBytes(sub.PasswordCT),
			pgNullBytes(sub.OAuthAccessCT),
			pgNullBytes(sub.OAuthRefreshCT),
			pgNullStr(sub.OAuthTokenEndpoint),
			pgNullStr(sub.OAuthClientID),
			pgNullBytes(sub.OAuthClientSecretCT),
			pgNullMicros(sub.OAuthExpiresAt),
			pgNullMicros(sub.RefreshDue),
			string(sub.State), usMicros(sub.StateAt),
			usMicros(sub.CreatedAt), nowUs,
		)
		return mapErr(err)
	})
}

func (m *metadata) GetIdentitySubmission(ctx context.Context, identityID string) (store.IdentitySubmission, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+identitySubmissionSelectColsPG+` FROM identity_submission WHERE identity_id = $1`,
		identityID)
	return scanIdentitySubmissionPG(row)
}

func (m *metadata) DeleteIdentitySubmission(ctx context.Context, identityID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM identity_submission WHERE identity_id = $1`, identityID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListIdentitySubmissionsDue(ctx context.Context, before time.Time) ([]store.IdentitySubmission, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT `+identitySubmissionSelectColsPG+`
		  FROM identity_submission
		 WHERE refresh_due_us IS NOT NULL AND refresh_due_us <= $1
		 ORDER BY refresh_due_us ASC`,
		usMicros(before))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.IdentitySubmission
	for rows.Next() {
		sub, err := scanIdentitySubmissionPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// MaterializeDefaultIdentity ensures a persisted jmap_identities row exists
// for the principal's synthesised default identity (idempotent).
//
// Under concurrent load two sessions may both observe no existing row and race
// to INSERT. We guard against this TOCTOU with a single retry: if the INSERT
// did not affect a row the winning transaction already wrote it, so we
// re-SELECT and return whatever it wrote. A second failure (attempt == 1)
// means something genuinely unexpected happened and we surface the error.
func (m *metadata) MaterializeDefaultIdentity(ctx context.Context, principalID store.PrincipalID) (string, error) {
	var identityID string
	for attempt := 0; attempt < 2; attempt++ {
		err := m.runTx(ctx, func(tx pgx.Tx) error {
			// SELECT the existing row — the fast path on all calls after the first.
			var existing string
			err := tx.QueryRow(ctx, `
				SELECT id FROM jmap_identities
				 WHERE principal_id = $1 AND may_delete = false
				 LIMIT 1`, int64(principalID)).Scan(&existing)
			if err == nil {
				identityID = existing
				return nil
			}
			if err != pgx.ErrNoRows {
				return mapErr(err)
			}

			// No row yet — fetch principal details and attempt the INSERT.
			var email, displayName string
			if err := tx.QueryRow(ctx,
				`SELECT canonical_email, display_name FROM principals WHERE id = $1`,
				int64(principalID)).Scan(&email, &displayName); err != nil {
				return mapErr(err)
			}

			now := m.s.clock.Now().UTC()
			nowUs := usMicros(now)
			// Compute the next numeric id from the current max to avoid a
			// dedicated sequence object (jmap_identities.id is TEXT PK).
			var nextID int64
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(MAX(id::bigint), 0) + 1
				  FROM jmap_identities
				 WHERE id ~ '^[0-9]+$'`).Scan(&nextID); err != nil {
				return fmt.Errorf("storepg: MaterializeDefaultIdentity: compute next id: %w", err)
			}
			newID := strconv.FormatInt(nextID, 10)
			tag, err := tx.Exec(ctx, `
				INSERT INTO jmap_identities
				  (id, principal_id, name, email, reply_to_json, bcc_json,
				   text_signature, html_signature, may_delete, created_at_us, updated_at_us)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT DO NOTHING`,
				newID, int64(principalID), displayName, email,
				[]byte{}, []byte{}, "", "", false,
				nowUs, nowUs)
			if err != nil {
				return mapErr(err)
			}
			if tag.RowsAffected() == 1 {
				identityID = newID
				return nil
			}
			// RowsAffected == 0: another transaction won the race. Signal
			// the outer loop to retry the SELECT on the next attempt.
			return nil
		})
		if err != nil {
			return "", err
		}
		if identityID != "" {
			return identityID, nil
		}
		if attempt == 1 {
			return "", fmt.Errorf("storepg: MaterializeDefaultIdentity: concurrent insert race unresolved for principal %d", principalID)
		}
		// attempt == 0, identityID still empty: loop and re-SELECT.
	}
	// Unreachable, but satisfies the compiler.
	return "", fmt.Errorf("storepg: MaterializeDefaultIdentity: unexpected loop exit for principal %d", principalID)
}

// pgNullBytes returns nil when b is empty; otherwise returns b.
func pgNullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// pgNullStr returns nil when s is empty; otherwise returns s.
func pgNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// pgNullMicros returns nil when t is zero; otherwise returns usMicros(t).
func pgNullMicros(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return usMicros(t)
}
