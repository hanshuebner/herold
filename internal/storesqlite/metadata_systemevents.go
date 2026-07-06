package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata system-events ring-buffer methods
// (REQ-ADM-304, re #142) for the SQLite backend.
// Schema commentary lives in migrations/0073_system_events.sql.

func (m *metadata) AppendSystemEvent(ctx context.Context, ev store.SystemEvent) error {
	metaJSON, err := encodeAuditMetadata(ev.Metadata)
	if err != nil {
		return fmt.Errorf("storesqlite: AppendSystemEvent: encode metadata: %w", err)
	}
	if metaJSON == "" {
		metaJSON = "{}"
	}
	at := ev.At
	if at.IsZero() {
		at = m.s.clock.Now().UTC()
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO system_events
			  (ts, action, actor_id, subject, remote_addr, outcome,
			   message, domain, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			usMicros(at),
			ev.Action,
			ev.ActorID,
			ev.Subject,
			ev.RemoteAddr,
			int64(ev.Outcome),
			ev.Message,
			ev.Domain,
			metaJSON,
		)
		return mapErr(err)
	})
}

func (m *metadata) ListSystemEvents(ctx context.Context, filter store.SystemEventFilter) ([]store.SystemEvent, error) {
	// Fail closed: non-nil empty Domains means no access (REQ-ADM-307).
	if filter.Domains != nil && len(filter.Domains) == 0 {
		return nil, nil
	}

	limit := filter.Limit
	switch {
	case limit <= 0:
		limit = 100
	case limit > 1000:
		limit = 1000
	}

	var args []any
	var conds []string

	if !filter.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, usMicros(filter.Until))
	}
	if filter.Action != "" {
		conds = append(conds, "action = ?")
		args = append(args, filter.Action)
	}
	if filter.ActorID != "" {
		conds = append(conds, "actor_id = ?")
		args = append(args, filter.ActorID)
	}
	if filter.BeforeID != 0 {
		conds = append(conds, "id < ?")
		args = append(args, int64(filter.BeforeID))
	}
	if len(filter.Domains) > 0 {
		placeholders := make([]string, len(filter.Domains))
		for i, d := range filter.Domains {
			placeholders[i] = "?"
			args = append(args, d)
		}
		conds = append(conds, "domain IN ("+strings.Join(placeholders, ",")+")")
	}

	var where string
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)

	q := `SELECT id, ts, action, actor_id, subject, remote_addr,
	             outcome, message, domain, metadata_json
	        FROM system_events` + where + `
	       ORDER BY id DESC
	       LIMIT ?`

	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []store.SystemEvent
	for rows.Next() {
		var (
			ev       store.SystemEvent
			id       int64
			tsUS     int64
			outcome  int64
			metaJSON string
		)
		if err := rows.Scan(&id, &tsUS, &ev.Action, &ev.ActorID, &ev.Subject,
			&ev.RemoteAddr, &outcome, &ev.Message, &ev.Domain, &metaJSON); err != nil {
			return nil, fmt.Errorf("storesqlite: ListSystemEvents scan: %w", err)
		}
		ev.ID = store.SystemEventID(id)
		ev.At = fromMicros(tsUS)
		ev.Outcome = store.AuditOutcome(outcome)
		if metaJSON != "" && metaJSON != "{}" {
			md, err := decodeAuditMetadata(metaJSON)
			if err != nil {
				return nil, err
			}
			ev.Metadata = md
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (m *metadata) EvictSystemEvents(ctx context.Context, opts store.SystemEventEvictOptions) (int, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	now := m.s.clock.Now().UTC()
	cutoff := usMicros(now.Add(-opts.MaxAge))

	var deleted int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var maxID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), -1) FROM system_events`).Scan(&maxID); err != nil {
			return mapErr(err)
		}
		capThreshold := maxID - int64(opts.CapRows)

		res, err := tx.ExecContext(ctx, `
			DELETE FROM system_events
			 WHERE id IN (
			   SELECT id FROM system_events
			    WHERE (ts < ? OR id <= ?)
			    ORDER BY id ASC
			    LIMIT ?
			 )`,
			cutoff, capThreshold, batchSize)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: EvictSystemEvents rows affected: %w", err)
		}
		deleted = int(n)
		return nil
	})
	return deleted, err
}
