package storepg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata session methods (REQ-OPS-208,
// REQ-CLOG-06) for the Postgres backend.  Schema commentary lives in
// migrations/0039_sessions.sql.

func (m *metadata) UpsertSession(ctx context.Context, s store.SessionRow) error {
	var livetailUs *int64
	if s.ClientlogLivetailUntil != nil {
		v := usMicros(*s.ClientlogLivetailUntil)
		livetailUs = &v
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions
			  (session_id, principal_id, created_at_us, expires_at_us,
			   clientlog_telemetry_enabled, clientlog_livetail_until_us)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (session_id) DO UPDATE SET
			  principal_id                = EXCLUDED.principal_id,
			  expires_at_us               = EXCLUDED.expires_at_us,
			  clientlog_telemetry_enabled = EXCLUDED.clientlog_telemetry_enabled,
			  clientlog_livetail_until_us = EXCLUDED.clientlog_livetail_until_us`,
			s.SessionID,
			int64(s.PrincipalID),
			usMicros(s.CreatedAt),
			usMicros(s.ExpiresAt),
			s.ClientlogTelemetryEnabled,
			livetailUs,
		)
		return mapErr(err)
	})
}

func (m *metadata) GetSession(ctx context.Context, sessionID string) (store.SessionRow, error) {
	var s store.SessionRow
	var principalID int64
	var createdUs, expiresUs int64
	var livetailUs *int64
	err := m.s.pool.QueryRow(ctx, `
		SELECT session_id, principal_id, created_at_us, expires_at_us,
		       clientlog_telemetry_enabled, clientlog_livetail_until_us
		  FROM sessions
		 WHERE session_id = $1`, sessionID).
		Scan(&s.SessionID, &principalID, &createdUs, &expiresUs,
			&s.ClientlogTelemetryEnabled, &livetailUs)
	if err != nil {
		return store.SessionRow{}, mapErr(err)
	}
	s.PrincipalID = store.PrincipalID(principalID)
	s.CreatedAt = fromMicros(createdUs)
	s.ExpiresAt = fromMicros(expiresUs)
	if livetailUs != nil {
		t := fromMicros(*livetailUs)
		s.ClientlogLivetailUntil = &t
	}
	return s, nil
}

func (m *metadata) DeleteSession(ctx context.Context, sessionID string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM sessions WHERE session_id = $1`, sessionID)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) UpdateSessionTelemetry(ctx context.Context, sessionID string, enabled bool) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET clientlog_telemetry_enabled = $1
			 WHERE session_id = $2`,
			enabled, sessionID)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) EvictExpiredSessions(ctx context.Context, nowMicros int64) (int, error) {
	var deleted int
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM sessions WHERE expires_at_us <= $1`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		deleted = int(res.RowsAffected())
		return nil
	})
	return deleted, err
}

func (m *metadata) ClearExpiredLivetail(ctx context.Context, nowMicros int64) (int, error) {
	var cleared int
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET clientlog_livetail_until_us = NULL
			 WHERE clientlog_livetail_until_us IS NOT NULL
			   AND clientlog_livetail_until_us <= $1`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		cleared = int(res.RowsAffected())
		return nil
	})
	return cleared, err
}
