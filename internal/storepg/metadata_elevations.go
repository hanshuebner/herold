package storepg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata step-up elevation methods
// (REQ-AUTH-74, issue #79, issue #225) for the Postgres backend.
// Schema commentary lives in migrations/0067_session_elevations.sql and
// migrations/0091_session_elevation_absolute_cap.sql.

func (m *metadata) UpsertElevation(ctx context.Context, e store.ElevationRow) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO session_elevations
			  (session_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (session_id) DO UPDATE SET
			  principal_id         = EXCLUDED.principal_id,
			  elevated_at_us       = EXCLUDED.elevated_at_us,
			  idle_deadline_us     = EXCLUDED.idle_deadline_us,
			  absolute_deadline_us = EXCLUDED.absolute_deadline_us`,
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
	var e store.ElevationRow
	var principalID int64
	var elevatedUs, idleUs, absUs int64
	err := m.s.pool.QueryRow(ctx, `
		SELECT session_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us
		  FROM session_elevations
		 WHERE session_id = $1
		   AND idle_deadline_us > $2
		   AND absolute_deadline_us > $2`,
		sessionID, nowMicros).
		Scan(&e.SessionID, &principalID, &elevatedUs, &idleUs, &absUs)
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE session_elevations
			   SET idle_deadline_us = LEAST($1, absolute_deadline_us)
			 WHERE session_id = $2
			   AND idle_deadline_us > $3
			   AND absolute_deadline_us > $3`,
			newIdle, sessionID, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
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
			`DELETE FROM session_elevations WHERE idle_deadline_us <= $1 OR absolute_deadline_us <= $1`,
			nowMicros)
		if err != nil {
			return mapErr(err)
		}
		deleted = int(res.RowsAffected())
		return nil
	})
	return deleted, err
}
