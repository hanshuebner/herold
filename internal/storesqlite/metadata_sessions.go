package storesqlite

import (
	"context"
	"database/sql"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata session methods (REQ-OPS-208,
// REQ-CLOG-06) for the SQLite backend.  Schema commentary lives in
// migrations/0039_sessions.sql.

func (m *metadata) UpsertSession(ctx context.Context, s store.SessionRow) error {
	var livetailUs *int64
	if s.ClientlogLivetailUntil != nil {
		v := usMicros(*s.ClientlogLivetailUntil)
		livetailUs = &v
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions
			  (session_id, principal_id, created_at_us, expires_at_us,
			   clientlog_telemetry_enabled, clientlog_livetail_until_us)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
			  principal_id                = excluded.principal_id,
			  expires_at_us               = excluded.expires_at_us,
			  clientlog_telemetry_enabled = excluded.clientlog_telemetry_enabled,
			  clientlog_livetail_until_us = excluded.clientlog_livetail_until_us`,
			s.SessionID,
			int64(s.PrincipalID),
			usMicros(s.CreatedAt),
			usMicros(s.ExpiresAt),
			boolToInt(s.ClientlogTelemetryEnabled),
			livetailUs,
		)
		return mapErr(err)
	})
}

func (m *metadata) GetSession(ctx context.Context, sessionID string) (store.SessionRow, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT session_id, principal_id, created_at_us, expires_at_us,
		       clientlog_telemetry_enabled, clientlog_livetail_until_us
		  FROM sessions
		 WHERE session_id = ?`, sessionID)
	var s store.SessionRow
	var principalID int64
	var createdUs, expiresUs int64
	var telemetryInt int64
	var livetailUs sql.NullInt64
	err := row.Scan(&s.SessionID, &principalID, &createdUs, &expiresUs,
		&telemetryInt, &livetailUs)
	if err != nil {
		return store.SessionRow{}, mapErr(err)
	}
	s.PrincipalID = store.PrincipalID(principalID)
	s.CreatedAt = fromMicros(createdUs)
	s.ExpiresAt = fromMicros(expiresUs)
	s.ClientlogTelemetryEnabled = telemetryInt != 0
	if livetailUs.Valid {
		t := fromMicros(livetailUs.Int64)
		s.ClientlogLivetailUntil = &t
	}
	return s, nil
}

func (m *metadata) DeleteSession(ctx context.Context, sessionID string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE session_id = ?`, sessionID)
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

func (m *metadata) UpdateSessionTelemetry(ctx context.Context, sessionID string, enabled bool) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions
			   SET clientlog_telemetry_enabled = ?
			 WHERE session_id = ?`,
			boolToInt(enabled), sessionID)
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

func (m *metadata) EvictExpiredSessions(ctx context.Context, nowMicros int64) (int, error) {
	var deleted int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE expires_at_us <= ?`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		n, _ := res.RowsAffected()
		deleted = int(n)
		return nil
	})
	return deleted, err
}

func (m *metadata) ClearExpiredLivetail(ctx context.Context, nowMicros int64) (int, error) {
	var cleared int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions
			   SET clientlog_livetail_until_us = NULL
			 WHERE clientlog_livetail_until_us IS NOT NULL
			   AND clientlog_livetail_until_us <= ?`, nowMicros)
		if err != nil {
			return mapErr(err)
		}
		n, _ := res.RowsAffected()
		cleared = int(n)
		return nil
	})
	return cleared, err
}
