package storepg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata SRS-secret methods (issue #204).
// Mirrors storesqlite/metadata_srs.go. Schema commentary lives in
// migrations/0081_srs_secrets.sql.

func (m *metadata) InsertSRSSecret(ctx context.Context, secret []byte) (store.SRSSecret, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		return mapErr(tx.QueryRow(ctx, `
			INSERT INTO srs_secrets (secret, created_at_us) VALUES ($1, $2)
			RETURNING id`,
			secret, usMicros(now)).Scan(&id))
	})
	if err != nil {
		return store.SRSSecret{}, err
	}
	return store.SRSSecret{
		ID:        store.SRSSecretID(id),
		Secret:    secret,
		CreatedAt: now,
	}, nil
}

func (m *metadata) ListSRSSecrets(ctx context.Context) ([]store.SRSSecret, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, secret, created_at_us FROM srs_secrets ORDER BY id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.SRSSecret, 0)
	for rows.Next() {
		var id, createdUs int64
		var secret []byte
		if err := rows.Scan(&id, &secret, &createdUs); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.SRSSecret{
			ID:        store.SRSSecretID(id),
			Secret:    secret,
			CreatedAt: fromMicros(createdUs),
		})
	}
	return out, mapErr(rows.Err())
}
