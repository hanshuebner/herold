package storepg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata system-events ring-buffer methods
// (REQ-ADM-304, re #142) for the Postgres backend.
// Schema commentary lives in migrations/0073_system_events.sql.

func (m *metadata) AppendSystemEvent(ctx context.Context, ev store.SystemEvent) error {
	metaJSON, err := encodeAuditMetadata(ev.Metadata)
	if err != nil {
		return fmt.Errorf("storepg: AppendSystemEvent: encode metadata: %w", err)
	}
	if metaJSON == "" {
		metaJSON = "{}"
	}
	at := ev.At
	if at.IsZero() {
		at = m.s.clock.Now().UTC()
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO system_events
			  (ts, action, actor_id, subject, remote_addr, outcome,
			   message, domain, metadata_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			usMicros(at),
			ev.Action,
			ev.ActorID,
			ev.Subject,
			ev.RemoteAddr,
			int32(ev.Outcome),
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
	p := 1 // Postgres parameter counter

	addArg := func(cond string, val any) {
		conds = append(conds, fmt.Sprintf(cond, p))
		args = append(args, val)
		p++
	}

	if !filter.Since.IsZero() {
		addArg("ts >= $%d", usMicros(filter.Since))
	}
	if !filter.Until.IsZero() {
		addArg("ts < $%d", usMicros(filter.Until))
	}
	if filter.Action != "" {
		addArg("action = $%d", filter.Action)
	}
	if filter.ActorID != "" {
		addArg("actor_id = $%d", filter.ActorID)
	}
	if filter.BeforeID != 0 {
		addArg("id < $%d", int64(filter.BeforeID))
	}
	if len(filter.Domains) > 0 {
		placeholders := make([]string, len(filter.Domains))
		for i, d := range filter.Domains {
			placeholders[i] = fmt.Sprintf("$%d", p)
			args = append(args, d)
			p++
		}
		conds = append(conds, "domain IN ("+strings.Join(placeholders, ",")+")")
	}

	var where string
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	limitPlaceholder := fmt.Sprintf("$%d", p)
	args = append(args, limit)

	q := `SELECT id, ts, action, actor_id, subject, remote_addr,
	             outcome, message, domain, metadata_json
	        FROM system_events` + where + `
	       ORDER BY id DESC
	       LIMIT ` + limitPlaceholder

	rows, err := m.s.pool.Query(ctx, q, args...)
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
			outcome  int32
			metaJSON string
		)
		if err := rows.Scan(&id, &tsUS, &ev.Action, &ev.ActorID, &ev.Subject,
			&ev.RemoteAddr, &outcome, &ev.Message, &ev.Domain, &metaJSON); err != nil {
			return nil, fmt.Errorf("storepg: ListSystemEvents scan: %w", err)
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

	var deleted int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var maxID int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(id), -1) FROM system_events`).Scan(&maxID); err != nil {
			return mapErr(err)
		}
		capThreshold := maxID - int64(opts.CapRows)

		ct, err := tx.Exec(ctx, `
			DELETE FROM system_events
			 WHERE id IN (
			   SELECT id FROM system_events
			    WHERE (ts < $1 OR id <= $2)
			    ORDER BY id ASC
			    LIMIT $3
			 )`,
			cutoff, capThreshold, batchSize)
		if err != nil {
			return mapErr(err)
		}
		deleted = ct.RowsAffected()
		return nil
	})
	return int(deleted), err
}
