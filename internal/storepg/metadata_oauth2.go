package storepg

// metadata_oauth2.go implements the OAuth2 native-client grant store
// methods for the Postgres backend (issue #199, REQ-AND-AUTH-01/02).
// Mirrors storesqlite/metadata_oauth2.go. Schema lives in
// migrations/0082_oauth2_native_grant.sql.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

func (m *metadata) InsertOAuthAuthCode(ctx context.Context, c store.OAuthAuthCode) (store.OAuthAuthCode, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO oauth_auth_codes
			  (hash, client_id, principal_id, redirect_uri, code_challenge,
			   code_challenge_method, scope_json, family_id, created_at_us,
			   expires_at_us, used_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL) RETURNING id`,
			c.Hash, c.ClientID, int64(c.PrincipalID), c.RedirectURI, c.CodeChallenge,
			c.CodeChallengeMethod, c.ScopeJSON, c.FamilyID, usMicros(now), usMicros(c.ExpiresAt)).Scan(&id)
	})
	if err != nil {
		return store.OAuthAuthCode{}, mapErr(err)
	}
	c.ID = store.OAuthAuthCodeID(id)
	c.CreatedAt = now
	return c, nil
}

const oauthAuthCodeSelectCols = `
	id, hash, client_id, principal_id, redirect_uri, code_challenge,
	code_challenge_method, scope_json, family_id, created_at_us,
	expires_at_us, used_at_us`

func scanOAuthAuthCode(row rowLike) (store.OAuthAuthCode, error) {
	var (
		id, pid                                      int64
		hash, clientID, redirectURI                  string
		codeChallenge, codeChallengeMethod, scopeStr string
		familyID                                     string
		createdUs, expiresUs                         int64
		usedUs                                       *int64
	)
	if err := row.Scan(&id, &hash, &clientID, &pid, &redirectURI, &codeChallenge,
		&codeChallengeMethod, &scopeStr, &familyID, &createdUs, &expiresUs, &usedUs); err != nil {
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
		ScopeJSON:           scopeStr,
		FamilyID:            familyID,
		CreatedAt:           fromMicros(createdUs),
		ExpiresAt:           fromMicros(expiresUs),
	}
	if usedUs != nil {
		c.UsedAt = fromMicros(*usedUs)
	}
	return c, nil
}

func (m *metadata) GetOAuthAuthCodeByHash(ctx context.Context, hash string) (store.OAuthAuthCode, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+oauthAuthCodeSelectCols+` FROM oauth_auth_codes WHERE hash = $1`, hash)
	return scanOAuthAuthCode(row)
}

func (m *metadata) ConsumeOAuthAuthCode(ctx context.Context, id store.OAuthAuthCodeID, usedAt time.Time) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE oauth_auth_codes SET used_at_us = $1 WHERE id = $2 AND used_at_us IS NULL`,
			usMicros(usedAt), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 1 {
			return nil
		}
		var exists int64
		if err := tx.QueryRow(ctx,
			`SELECT 1 FROM oauth_auth_codes WHERE id = $1`, int64(id)).Scan(&exists); err != nil {
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO oauth_refresh_tokens
			  (hash, family_id, principal_id, client_id, scope_json, access_key_id,
			   created_at_us, expires_at_us, rotated_at_us, revoked_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, NULL) RETURNING id`,
			rt.Hash, rt.FamilyID, int64(rt.PrincipalID), rt.ClientID, rt.ScopeJSON,
			accessKeyID, usMicros(now), usMicros(rt.ExpiresAt)).Scan(&id)
	})
	if err != nil {
		return store.OAuthRefreshToken{}, mapErr(err)
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
		accessKeyID                     *int64
		createdUs, expiresUs            int64
		rotatedUs, revokedUs            *int64
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
	if accessKeyID != nil {
		rt.AccessKeyID = store.APIKeyID(*accessKeyID)
	}
	if rotatedUs != nil {
		rt.RotatedAt = fromMicros(*rotatedUs)
	}
	if revokedUs != nil {
		rt.RevokedAt = fromMicros(*revokedUs)
	}
	return rt, nil
}

func (m *metadata) GetOAuthRefreshTokenByHash(ctx context.Context, hash string) (store.OAuthRefreshToken, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+oauthRefreshTokenSelectCols+` FROM oauth_refresh_tokens WHERE hash = $1`, hash)
	return scanOAuthRefreshToken(row)
}

func (m *metadata) MarkOAuthRefreshTokenRotated(ctx context.Context, id store.OAuthRefreshTokenID, rotatedAt time.Time) (bool, error) {
	var alreadyRotated bool
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE oauth_refresh_tokens SET rotated_at_us = $1
			  WHERE id = $2 AND rotated_at_us IS NULL AND revoked_at_us IS NULL`,
			usMicros(rotatedAt), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 1 {
			return nil
		}
		var exists int64
		if err := tx.QueryRow(ctx,
			`SELECT 1 FROM oauth_refresh_tokens WHERE id = $1`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		alreadyRotated = true
		return nil
	})
	return alreadyRotated, err
}

func (m *metadata) RevokeOAuthRefreshTokenFamily(ctx context.Context, familyID string, revokedAt time.Time) (int64, error) {
	var n int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT access_key_id FROM oauth_refresh_tokens
			  WHERE family_id = $1 AND access_key_id IS NOT NULL`, familyID)
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

		res, err := tx.Exec(ctx,
			`UPDATE oauth_refresh_tokens SET revoked_at_us = $1
			  WHERE family_id = $2 AND revoked_at_us IS NULL`,
			usMicros(revokedAt), familyID)
		if err != nil {
			return mapErr(err)
		}
		n = res.RowsAffected()

		for _, kid := range keyIDs {
			if _, err := tx.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, kid); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("storepg: revoke oauth refresh token family: %w", err)
	}
	return n, nil
}

// ListOAuthRefreshTokensByPrincipal returns the currently-active refresh
// token row of every live rotation family owned by pid (issue #224
// active-credentials view). See the store.Meta interface doc comment.
func (m *metadata) ListOAuthRefreshTokensByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.OAuthRefreshToken, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+oauthRefreshTokenSelectCols+` FROM oauth_refresh_tokens
		  WHERE principal_id = $1 AND rotated_at_us IS NULL AND revoked_at_us IS NULL
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
