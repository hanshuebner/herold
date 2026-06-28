package storesqlite

import (
	"context"
	"database/sql"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata step-up elevation methods
// (REQ-AUTH-74, issue #79) for the SQLite backend.
// Schema commentary lives in migrations/0067_session_elevations.sql.

func (m *metadata) UpsertElevation(ctx context.Context, e store.ElevationRow) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO session_elevations
			  (session_id, principal_id, elevated_at_us, expires_at_us)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
			  principal_id   = excluded.principal_id,
			  elevated_at_us = excluded.elevated_at_us,
			  expires_at_us  = excluded.expires_at_us`,
			e.SessionID,
			int64(e.PrincipalID),
			usMicros(e.ElevatedAt),
			usMicros(e.ExpiresAt),
		)
		return mapErr(err)
	})
}

func (m *metadata) GetActiveElevation(ctx context.Context, sessionID string, nowMicros int64) (store.ElevationRow, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT session_id, principal_id, elevated_at_us, expires_at_us
		  FROM session_elevations
		 WHERE session_id = ?
		   AND expires_at_us > ?`,
		sessionID, nowMicros)
	var e store.ElevationRow
	var principalID int64
	var elevatedUs, expiresUs int64
	err := row.Scan(&e.SessionID, &principalID, &elevatedUs, &expiresUs)
	if err != nil {
		return store.ElevationRow{}, mapErr(err)
	}
	e.PrincipalID = store.PrincipalID(principalID)
	e.ElevatedAt = fromMicros(elevatedUs)
	e.ExpiresAt = fromMicros(expiresUs)
	return e, nil
}

func (m *metadata) DeleteElevation(ctx context.Context, sessionID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM session_elevations WHERE session_id = ?`, sessionID)
		if err != nil {
			return mapErr(err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) EvictExpiredElevations(ctx context.Context, nowMicros int64) (int, error) {
	var deleted int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM session_elevations WHERE expires_at_us <= ?`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
		return nil
	})
	return deleted, err
}
