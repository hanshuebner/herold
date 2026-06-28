package storepg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata step-up elevation methods
// (REQ-AUTH-74, issue #79) for the Postgres backend.
// Schema commentary lives in migrations/0067_session_elevations.sql.

func (m *metadata) UpsertElevation(ctx context.Context, e store.ElevationRow) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO session_elevations
			  (session_id, principal_id, elevated_at_us, expires_at_us)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (session_id) DO UPDATE SET
			  principal_id   = EXCLUDED.principal_id,
			  elevated_at_us = EXCLUDED.elevated_at_us,
			  expires_at_us  = EXCLUDED.expires_at_us`,
			e.SessionID,
			int64(e.PrincipalID),
			usMicros(e.ElevatedAt),
			usMicros(e.ExpiresAt),
		)
		return mapErr(err)
	})
}

func (m *metadata) GetActiveElevation(ctx context.Context, sessionID string, nowMicros int64) (store.ElevationRow, error) {
	var e store.ElevationRow
	var principalID int64
	var elevatedUs, expiresUs int64
	err := m.s.pool.QueryRow(ctx, `
		SELECT session_id, principal_id, elevated_at_us, expires_at_us
		  FROM session_elevations
		 WHERE session_id = $1
		   AND expires_at_us > $2`,
		sessionID, nowMicros).
		Scan(&e.SessionID, &principalID, &elevatedUs, &expiresUs)
	if err != nil {
		return store.ElevationRow{}, mapErr(err)
	}
	e.PrincipalID = store.PrincipalID(principalID)
	e.ElevatedAt = fromMicros(elevatedUs)
	e.ExpiresAt = fromMicros(expiresUs)
	return e, nil
}

func (m *metadata) DeleteElevation(ctx context.Context, sessionID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM session_elevations WHERE session_id = $1`, sessionID)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) EvictExpiredElevations(ctx context.Context, nowMicros int64) (int, error) {
	var deleted int
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM session_elevations WHERE expires_at_us <= $1`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		deleted = int(res.RowsAffected())
		return nil
	})
	return deleted, err
}
