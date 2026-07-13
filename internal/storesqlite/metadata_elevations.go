package storesqlite

import (
	"context"
	"database/sql"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata step-up elevation methods
// (REQ-AUTH-74, issue #79, issue #225) for the SQLite backend.
// Schema commentary lives in migrations/0067_session_elevations.sql and
// migrations/0091_session_elevation_absolute_cap.sql.

func (m *metadata) UpsertElevation(ctx context.Context, e store.ElevationRow) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO session_elevations
			  (session_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
			  principal_id         = excluded.principal_id,
			  elevated_at_us       = excluded.elevated_at_us,
			  idle_deadline_us     = excluded.idle_deadline_us,
			  absolute_deadline_us = excluded.absolute_deadline_us`,
			e.SessionID,
			int64(e.PrincipalID),
			usMicros(e.ElevatedAt),
			usMicros(e.IdleDeadline),
			usMicros(e.AbsoluteDeadline),
		)
		return mapErr(err)
	})
}

func (m *metadata) GetActiveElevation(ctx context.Context, sessionID string, nowMicros int64) (store.ElevationRow, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT session_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us
		  FROM session_elevations
		 WHERE session_id = ?
		   AND idle_deadline_us > ?
		   AND absolute_deadline_us > ?`,
		sessionID, nowMicros, nowMicros)
	var e store.ElevationRow
	var principalID int64
	var elevatedUs, idleUs, absUs int64
	err := row.Scan(&e.SessionID, &principalID, &elevatedUs, &idleUs, &absUs)
	if err != nil {
		return store.ElevationRow{}, mapErr(err)
	}
	e.PrincipalID = store.PrincipalID(principalID)
	e.ElevatedAt = fromMicros(elevatedUs)
	e.IdleDeadline = fromMicros(idleUs)
	e.AbsoluteDeadline = fromMicros(absUs)
	return e, nil
}

func (m *metadata) ExtendElevation(ctx context.Context, sessionID string, nowMicros int64, idleTTLMicros int64) error {
	newIdle := nowMicros + idleTTLMicros
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_elevations
			   SET idle_deadline_us = MIN(?, absolute_deadline_us)
			 WHERE session_id = ?
			   AND idle_deadline_us > ?
			   AND absolute_deadline_us > ?`,
			newIdle, sessionID, nowMicros, nowMicros)
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
			`DELETE FROM session_elevations WHERE idle_deadline_us <= ? OR absolute_deadline_us <= ?`,
			nowMicros, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
		return nil
	})
	return deleted, err
}
