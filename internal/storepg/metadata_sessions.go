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
	// last_seen_at defaults to CreatedAt for fresh inserts so a brand
	// new session starts the idle clock at its own birth.
	lastSeenAt := s.LastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = s.CreatedAt
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions
			  (session_id, principal_id, created_at_us, expires_at_us,
			   last_seen_at_us, user_agent, last_seen_ip,
			   clientlog_telemetry_enabled, clientlog_livetail_until_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (session_id) DO UPDATE SET
			  principal_id                = EXCLUDED.principal_id,
			  expires_at_us               = EXCLUDED.expires_at_us,
			  last_seen_at_us             = EXCLUDED.last_seen_at_us,
			  clientlog_telemetry_enabled = EXCLUDED.clientlog_telemetry_enabled,
			  clientlog_livetail_until_us = EXCLUDED.clientlog_livetail_until_us`,
			// user_agent and last_seen_ip intentionally excluded from the
			// ON CONFLICT SET: user_agent is set once at login; last_seen_ip
			// is updated via UpdateSessionLastSeen on each touch.
			s.SessionID,
			int64(s.PrincipalID),
			usMicros(s.CreatedAt),
			usMicros(s.ExpiresAt),
			usMicros(lastSeenAt),
			s.UserAgent,
			s.LastSeenIP,
			s.ClientlogTelemetryEnabled,
			livetailUs,
		)
		return mapErr(err)
	})
}

func (m *metadata) GetSession(ctx context.Context, sessionID string) (store.SessionRow, error) {
	var s store.SessionRow
	var principalID int64
	var createdUs, expiresUs, lastSeenUs int64
	var livetailUs, revokedUs *int64
	err := m.s.pool.QueryRow(ctx, `
		SELECT session_id, principal_id, created_at_us, expires_at_us,
		       last_seen_at_us, user_agent, last_seen_ip,
		       clientlog_telemetry_enabled, clientlog_livetail_until_us,
		       revoked_at_us
		  FROM sessions
		 WHERE session_id = $1`, sessionID).
		Scan(&s.SessionID, &principalID, &createdUs, &expiresUs,
			&lastSeenUs, &s.UserAgent, &s.LastSeenIP,
			&s.ClientlogTelemetryEnabled, &livetailUs, &revokedUs)
	if err != nil {
		return store.SessionRow{}, mapErr(err)
	}
	s.PrincipalID = store.PrincipalID(principalID)
	s.CreatedAt = fromMicros(createdUs)
	s.ExpiresAt = fromMicros(expiresUs)
	s.LastSeenAt = fromMicros(lastSeenUs)
	if livetailUs != nil {
		t := fromMicros(*livetailUs)
		s.ClientlogLivetailUntil = &t
	}
	s.Tombstoned = revokedUs != nil
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

func (m *metadata) UpdateSessionLastSeen(ctx context.Context, sessionID string, atMicros int64, ip string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET last_seen_at_us = $1,
			       last_seen_ip    = $2
			 WHERE session_id = $3`,
			atMicros, ip, sessionID)
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

func (m *metadata) ListSessionsByPrincipal(ctx context.Context, principalID store.PrincipalID, nowMicros int64) ([]store.SessionRow, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT session_id, principal_id, created_at_us, expires_at_us,
		       last_seen_at_us, user_agent, last_seen_ip,
		       clientlog_telemetry_enabled, clientlog_livetail_until_us,
		       revoked_at_us
		  FROM sessions
		 WHERE principal_id = $1
		   AND expires_at_us > $2
		   AND revoked_at_us IS NULL
		 ORDER BY created_at_us DESC`,
		int64(principalID), nowMicros)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var result []store.SessionRow
	for rows.Next() {
		var s store.SessionRow
		var pid int64
		var createdUs, expiresUs, lastSeenUs int64
		var livetailUs, revokedUs *int64
		if err := rows.Scan(&s.SessionID, &pid, &createdUs, &expiresUs,
			&lastSeenUs, &s.UserAgent, &s.LastSeenIP,
			&s.ClientlogTelemetryEnabled, &livetailUs, &revokedUs); err != nil {
			return nil, mapErr(err)
		}
		s.PrincipalID = store.PrincipalID(pid)
		s.CreatedAt = fromMicros(createdUs)
		s.ExpiresAt = fromMicros(expiresUs)
		s.LastSeenAt = fromMicros(lastSeenUs)
		if livetailUs != nil {
			t := fromMicros(*livetailUs)
			s.ClientlogLivetailUntil = &t
		}
		s.Tombstoned = revokedUs != nil
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return result, nil
}

func (m *metadata) TombstoneSession(ctx context.Context, sessionID string, principalID store.PrincipalID, nowMicros int64, tombstoneTTLMicros int64) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at_us = $1,
			       expires_at_us = $2
			 WHERE session_id   = $3
			   AND principal_id = $4
			   AND revoked_at_us IS NULL`,
			nowMicros, nowMicros+tombstoneTTLMicros, sessionID, int64(principalID))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
