package storesqlite

import (
	"context"
	"database/sql"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata Bearer-credential step-up
// elevation methods (REQ-AUTH-74, REQ-AUTH-78, issue #357) for the SQLite
// backend. Schema commentary lives in
// migrations/0110_api_key_elevations.sql. These methods mirror
// metadata_elevations.go exactly, keyed on api_key_id instead of
// session_id.

func (m *metadata) UpsertAPIKeyElevation(ctx context.Context, e store.APIKeyElevationRow) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api_key_elevations
			  (api_key_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id) DO UPDATE SET
			  principal_id         = excluded.principal_id,
			  elevated_at_us       = excluded.elevated_at_us,
			  idle_deadline_us     = excluded.idle_deadline_us,
			  absolute_deadline_us = excluded.absolute_deadline_us`,
			int64(e.APIKeyID),
			int64(e.PrincipalID),
			usMicros(e.ElevatedAt),
			usMicros(e.IdleDeadline),
			usMicros(e.AbsoluteDeadline),
		)
		return mapErr(err)
	})
}

func (m *metadata) GetActiveAPIKeyElevation(ctx context.Context, apiKeyID store.APIKeyID, nowMicros int64) (store.APIKeyElevationRow, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT api_key_id, principal_id, elevated_at_us, idle_deadline_us, absolute_deadline_us
		  FROM api_key_elevations
		 WHERE api_key_id = ?
		   AND idle_deadline_us > ?
		   AND absolute_deadline_us > ?`,
		int64(apiKeyID), nowMicros, nowMicros)
	var e store.APIKeyElevationRow
	var keyID, principalID int64
	var elevatedUs, idleUs, absUs int64
	err := row.Scan(&keyID, &principalID, &elevatedUs, &idleUs, &absUs)
	if err != nil {
		return store.APIKeyElevationRow{}, mapErr(err)
	}
	e.APIKeyID = store.APIKeyID(keyID)
	e.PrincipalID = store.PrincipalID(principalID)
	e.ElevatedAt = fromMicros(elevatedUs)
	e.IdleDeadline = fromMicros(idleUs)
	e.AbsoluteDeadline = fromMicros(absUs)
	return e, nil
}

func (m *metadata) ExtendAPIKeyElevation(ctx context.Context, apiKeyID store.APIKeyID, nowMicros int64, idleTTLMicros int64) error {
	newIdle := nowMicros + idleTTLMicros
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE api_key_elevations
			   SET idle_deadline_us = MIN(?, absolute_deadline_us)
			 WHERE api_key_id = ?
			   AND idle_deadline_us > ?
			   AND absolute_deadline_us > ?`,
			newIdle, int64(apiKeyID), nowMicros, nowMicros)
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

func (m *metadata) DeleteAPIKeyElevation(ctx context.Context, apiKeyID store.APIKeyID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM api_key_elevations WHERE api_key_id = ?`, int64(apiKeyID))
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

func (m *metadata) EvictExpiredAPIKeyElevations(ctx context.Context, nowMicros int64) (int, error) {
	var deleted int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM api_key_elevations WHERE idle_deadline_us <= ? OR absolute_deadline_us <= ?`,
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
