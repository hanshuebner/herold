package storepg

// metadata_oauthclient.go implements the OAuth2 native-client registry
// store methods for the Postgres backend (issue #199, REQ-AND-AUTH-01/02).
// Mirrors storesqlite/metadata_oauthclient.go. Schema lives in
// migrations/0092_oauth_clients.sql.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

func (m *metadata) InsertOAuthClient(ctx context.Context, c store.OAuthClient) (store.OAuthClient, error) {
	redirectJSON, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: encode redirect_uris: %w", err)
	}
	scopesJSON, err := json.Marshal(c.Scopes)
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: encode scopes: %w", err)
	}
	now := m.s.clock.Now().UTC()
	var secretHash any
	if c.ClientSecretHash != "" {
		secretHash = c.ClientSecretHash
	}
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO oauth_clients
			  (client_id, name, redirect_uris_json, scopes_json, public,
			   client_secret_hash, created_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			c.ClientID, c.Name, string(redirectJSON), string(scopesJSON),
			c.Public, secretHash, usMicros(now))
		return err
	})
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("OAuth2 client %q: %w", c.ClientID, mapErr(err))
	}
	c.CreatedAt = now
	return c, nil
}

const oauthClientSelectCols = `
	client_id, name, redirect_uris_json, scopes_json, public,
	client_secret_hash, created_at_us`

func scanOAuthClient(row rowLike) (store.OAuthClient, error) {
	var (
		clientID, name, redirectJSON, scopesJSON string
		public                                   bool
		secretHash                               *string
		createdUs                                int64
	)
	if err := row.Scan(&clientID, &name, &redirectJSON, &scopesJSON, &public,
		&secretHash, &createdUs); err != nil {
		return store.OAuthClient{}, mapErr(err)
	}
	c := store.OAuthClient{
		ClientID:  clientID,
		Name:      name,
		Public:    public,
		CreatedAt: fromMicros(createdUs),
	}
	if secretHash != nil {
		c.ClientSecretHash = *secretHash
	}
	if err := json.Unmarshal([]byte(redirectJSON), &c.RedirectURIs); err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: decode redirect_uris for %q: %w", clientID, err)
	}
	if err := json.Unmarshal([]byte(scopesJSON), &c.Scopes); err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: decode scopes for %q: %w", clientID, err)
	}
	return c, nil
}

func (m *metadata) GetOAuthClient(ctx context.Context, clientID string) (store.OAuthClient, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+oauthClientSelectCols+` FROM oauth_clients WHERE client_id = $1`, clientID)
	return scanOAuthClient(row)
}

func (m *metadata) ListOAuthClients(ctx context.Context) ([]store.OAuthClient, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+oauthClientSelectCols+` FROM oauth_clients ORDER BY client_id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.OAuthClient
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (m *metadata) UpdateOAuthClient(ctx context.Context, c store.OAuthClient) (store.OAuthClient, error) {
	redirectJSON, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: encode redirect_uris: %w", err)
	}
	scopesJSON, err := json.Marshal(c.Scopes)
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("storepg: encode scopes: %w", err)
	}
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE oauth_clients SET name = $1, redirect_uris_json = $2, scopes_json = $3
			  WHERE client_id = $4`,
			c.Name, string(redirectJSON), string(scopesJSON), c.ClientID)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return store.OAuthClient{}, err
	}
	return m.GetOAuthClient(ctx, c.ClientID)
}

func (m *metadata) DeleteOAuthClient(ctx context.Context, clientID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM oauth_clients WHERE client_id = $1`, clientID)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
