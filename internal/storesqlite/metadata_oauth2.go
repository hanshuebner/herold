package storesqlite

// metadata_oauth2.go implements the OAuth2 native-client grant store
// methods for the SQLite backend (issue #199, REQ-AND-AUTH-01/02).
// Schema lives in migrations/0082_oauth2_native_grant.sql.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func (m *metadata) InsertOAuthAuthCode(ctx context.Context, c store.OAuthAuthCode) (store.OAuthAuthCode, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_auth_codes
			  (hash, client_id, principal_id, redirect_uri, code_challenge,
			   code_challenge_method, scope_json, family_id, created_at_us,
			   expires_at_us, used_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			c.Hash, c.ClientID, int64(c.PrincipalID), c.RedirectURI, c.CodeChallenge,
			c.CodeChallengeMethod, c.ScopeJSON, c.FamilyID, usMicros(now), usMicros(c.ExpiresAt))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.OAuthAuthCode{}, err
	}
	c.ID = store.OAuthAuthCodeID(id)
	c.CreatedAt = now
	return c, nil
}

func scanOAuthAuthCode(row rowLike) (store.OAuthAuthCode, error) {
	var (
		id, pid                                       int64
		hash, clientID, redirectURI                   string
		codeChallenge, codeChallengeMethod, scopeJSON string
		familyID                                      string
		createdUs, expiresUs                          int64
		usedUs                                        sql.NullInt64
	)
	if err := row.Scan(&id, &hash, &clientID, &pid, &redirectURI, &codeChallenge,
		&codeChallengeMethod, &scopeJSON, &familyID, &createdUs, &expiresUs, &usedUs); err != nil {
		return store.OAuthAuthCode{}, mapErr(err)
	}
	c := store.OAuthAuthCode{
		ID:                  store.OAuthAuthCodeID(id),
		Hash:                hash,
		ClientID:            clientID,
		PrincipalID:         store.PrincipalID(pid),
		RedirectURI:         redirectURI,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ScopeJSON:           scopeJSON,
		FamilyID:            familyID,
		CreatedAt:           fromMicros(createdUs),
		ExpiresAt:           fromMicros(expiresUs),
	}
	if usedUs.Valid {
		c.UsedAt = fromMicros(usedUs.Int64)
	}
	return c, nil
}

const oauthAuthCodeSelectCols = `
	id, hash, client_id, principal_id, redirect_uri, code_challenge,
	code_challenge_method, scope_json, family_id, created_at_us,
	expires_at_us, used_at_us`

func (m *metadata) GetOAuthAuthCodeByHash(ctx context.Context, hash string) (store.OAuthAuthCode, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+oauthAuthCodeSelectCols+` FROM oauth_auth_codes WHERE hash = ?`, hash)
	return scanOAuthAuthCode(row)
}

func (m *metadata) ConsumeOAuthAuthCode(ctx context.Context, id store.OAuthAuthCodeID, usedAt time.Time) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE oauth_auth_codes SET used_at_us = ? WHERE id = ? AND used_at_us IS NULL`,
			usMicros(usedAt), int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 1 {
			return nil
		}
		// n == 0: either the row does not exist, or it is already
		// consumed (a replay). Disambiguate with a lookup.
		var exists int64
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM oauth_auth_codes WHERE id = ?`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		return store.ErrConflict
	})
}

func (m *metadata) InsertOAuthRefreshToken(ctx context.Context, rt store.OAuthRefreshToken) (store.OAuthRefreshToken, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	var accessKeyID any
	if rt.AccessKeyID != 0 {
		accessKeyID = int64(rt.AccessKeyID)
	}
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_refresh_tokens
			  (hash, family_id, principal_id, client_id, scope_json, access_key_id,
			   created_at_us, expires_at_us, rotated_at_us, revoked_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
			rt.Hash, rt.FamilyID, int64(rt.PrincipalID), rt.ClientID, rt.ScopeJSON,
			accessKeyID, usMicros(now), usMicros(rt.ExpiresAt))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.OAuthRefreshToken{}, err
	}
	rt.ID = store.OAuthRefreshTokenID(id)
	rt.CreatedAt = now
	return rt, nil
}

const oauthRefreshTokenSelectCols = `
	id, hash, family_id, principal_id, client_id, scope_json, access_key_id,
	created_at_us, expires_at_us, rotated_at_us, revoked_at_us`

func scanOAuthRefreshToken(row rowLike) (store.OAuthRefreshToken, error) {
	var (
		id, pid                         int64
		hash, familyID, clientID, scope string
		accessKeyID                     sql.NullInt64
		createdUs, expiresUs            int64
		rotatedUs, revokedUs            sql.NullInt64
	)
	if err := row.Scan(&id, &hash, &familyID, &pid, &clientID, &scope, &accessKeyID,
		&createdUs, &expiresUs, &rotatedUs, &revokedUs); err != nil {
		return store.OAuthRefreshToken{}, mapErr(err)
	}
	rt := store.OAuthRefreshToken{
		ID:          store.OAuthRefreshTokenID(id),
		Hash:        hash,
		FamilyID:    familyID,
		PrincipalID: store.PrincipalID(pid),
		ClientID:    clientID,
		ScopeJSON:   scope,
		CreatedAt:   fromMicros(createdUs),
		ExpiresAt:   fromMicros(expiresUs),
	}
	if accessKeyID.Valid {
		rt.AccessKeyID = store.APIKeyID(accessKeyID.Int64)
	}
	if rotatedUs.Valid {
		rt.RotatedAt = fromMicros(rotatedUs.Int64)
	}
	if revokedUs.Valid {
		rt.RevokedAt = fromMicros(revokedUs.Int64)
	}
	return rt, nil
}

func (m *metadata) GetOAuthRefreshTokenByHash(ctx context.Context, hash string) (store.OAuthRefreshToken, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+oauthRefreshTokenSelectCols+` FROM oauth_refresh_tokens WHERE hash = ?`, hash)
	return scanOAuthRefreshToken(row)
}

func (m *metadata) MarkOAuthRefreshTokenRotated(ctx context.Context, id store.OAuthRefreshTokenID, rotatedAt time.Time) (bool, error) {
	var alreadyRotated bool
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE oauth_refresh_tokens SET rotated_at_us = ?
			  WHERE id = ? AND rotated_at_us IS NULL AND revoked_at_us IS NULL`,
			usMicros(rotatedAt), int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 1 {
			return nil
		}
		var exists int64
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM oauth_refresh_tokens WHERE id = ?`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		alreadyRotated = true
		return nil
	})
	return alreadyRotated, err
}

func (m *metadata) RevokeOAuthRefreshTokenFamily(ctx context.Context, familyID string, revokedAt time.Time) (int64, error) {
	var n int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Collect any live access-token key ids in the family before
		// revoking, so the still-usable short-lived access token is
		// deleted immediately rather than left to expire on its own.
		rows, err := tx.QueryContext(ctx,
			`SELECT access_key_id FROM oauth_refresh_tokens
			  WHERE family_id = ? AND access_key_id IS NOT NULL`, familyID)
		if err != nil {
			return mapErr(err)
		}
		var keyIDs []int64
		for rows.Next() {
			var kid int64
			if err := rows.Scan(&kid); err != nil {
				rows.Close()
				return mapErr(err)
			}
			keyIDs = append(keyIDs, kid)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return mapErr(err)
		}
		rows.Close()

		res, err := tx.ExecContext(ctx,
			`UPDATE oauth_refresh_tokens SET revoked_at_us = ?
			  WHERE family_id = ? AND revoked_at_us IS NULL`,
			usMicros(revokedAt), familyID)
		if err != nil {
			return mapErr(err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}

		for _, kid := range keyIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, kid); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
	return n, err
}

// ListOAuthRefreshTokensByPrincipal returns the currently-active refresh
// token row of every live rotation family owned by pid (issue #224
// active-credentials view). See the store.Meta interface doc comment.
func (m *metadata) ListOAuthRefreshTokensByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.OAuthRefreshToken, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+oauthRefreshTokenSelectCols+` FROM oauth_refresh_tokens
		  WHERE principal_id = ? AND rotated_at_us IS NULL AND revoked_at_us IS NULL
		  ORDER BY id`, int64(pid))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.OAuthRefreshToken
	for rows.Next() {
		rt, err := scanOAuthRefreshToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
