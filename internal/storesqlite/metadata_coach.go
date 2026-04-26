package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 3 Wave 3.10 ShortcutCoachStat store
// methods (REQ-PROTO-110..112) against SQLite. Schema lives in
// migrations/0020_coach.sql; the Postgres mirror is
// storepg/metadata_coach.go.

func (m *metadata) AppendCoachEvents(ctx context.Context, events []store.CoachEvent) error {
	if len(events) == 0 {
		return nil
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		for _, ev := range events {
			if ev.Count <= 0 {
				ev.Count = 1
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO coach_events
					(principal_id, action, input_method, event_count, occurred_at, recorded_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				int64(ev.PrincipalID),
				ev.Action,
				string(ev.Method),
				ev.Count,
				usMicros(ev.OccurredAt),
				usMicros(now),
			)
			if err != nil {
				return fmt.Errorf("storesqlite: insert coach_event: %w", mapErr(err))
			}
		}
		return nil
	})
}

func (m *metadata) GetCoachStat(ctx context.Context, principalID store.PrincipalID, action string, now time.Time) (store.CoachStat, error) {
	w14 := usMicros(now.Add(-14 * 24 * time.Hour))
	w90 := usMicros(now.Add(-90 * 24 * time.Hour))
	nowUs := usMicros(now)

	var (
		kb14, ms14, kb90, ms90 int64
		lastKbUs, lastMsUs     sql.NullInt64
	)
	err := m.s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  MAX(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN occurred_at ELSE NULL END),
		  MAX(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN occurred_at ELSE NULL END)
		FROM coach_events
		WHERE principal_id = ? AND action = ? AND occurred_at >= ? AND occurred_at <= ?`,
		w14, w14, w90, w90, w90, w90,
		int64(principalID), action, w90, nowUs,
	).Scan(&kb14, &ms14, &kb90, &ms90, &lastKbUs, &lastMsUs)
	if err != nil {
		return store.CoachStat{}, fmt.Errorf("storesqlite: get coach events: %w", mapErr(err))
	}

	stat := store.CoachStat{
		Action:           action,
		KeyboardCount14d: int(kb14),
		MouseCount14d:    int(ms14),
		KeyboardCount90d: int(kb90),
		MouseCount90d:    int(ms90),
	}
	if lastKbUs.Valid {
		t := fromMicros(lastKbUs.Int64)
		stat.LastKeyboardAt = &t
	}
	if lastMsUs.Valid {
		t := fromMicros(lastMsUs.Int64)
		stat.LastMouseAt = &t
	}

	// Merge dismissal row (may not exist).
	var dismissCount int64
	var dismissUntilUs sql.NullInt64
	err = m.s.db.QueryRowContext(ctx, `
		SELECT dismiss_count, dismiss_until
		  FROM coach_dismiss
		 WHERE principal_id = ? AND action = ?`,
		int64(principalID), action,
	).Scan(&dismissCount, &dismissUntilUs)
	if err != nil && err != sql.ErrNoRows {
		return store.CoachStat{}, fmt.Errorf("storesqlite: get coach_dismiss: %w", mapErr(err))
	}
	stat.DismissCount = int(dismissCount)
	if dismissUntilUs.Valid {
		t := fromMicros(dismissUntilUs.Int64)
		stat.DismissUntil = &t
	}

	return stat, nil
}

func (m *metadata) ListCoachStats(ctx context.Context, principalID store.PrincipalID, now time.Time) ([]store.CoachStat, error) {
	w14 := usMicros(now.Add(-14 * 24 * time.Hour))
	w90 := usMicros(now.Add(-90 * 24 * time.Hour))
	nowUs := usMicros(now)

	// Collect distinct actions from both tables.
	actionSet := make(map[string]struct{})

	rowsE, err := m.s.db.QueryContext(ctx, `
		SELECT DISTINCT action FROM coach_events
		 WHERE principal_id = ? AND occurred_at >= ? AND occurred_at <= ?`,
		int64(principalID), w90, nowUs)
	if err != nil {
		return nil, fmt.Errorf("storesqlite: list coach actions (events): %w", mapErr(err))
	}
	defer rowsE.Close()
	for rowsE.Next() {
		var a string
		if err := rowsE.Scan(&a); err != nil {
			return nil, fmt.Errorf("storesqlite: scan coach action: %w", err)
		}
		actionSet[a] = struct{}{}
	}
	if err := rowsE.Err(); err != nil {
		return nil, fmt.Errorf("storesqlite: list coach actions (events): %w", err)
	}

	rowsD, err := m.s.db.QueryContext(ctx, `
		SELECT DISTINCT action FROM coach_dismiss WHERE principal_id = ?`,
		int64(principalID))
	if err != nil {
		return nil, fmt.Errorf("storesqlite: list coach actions (dismiss): %w", mapErr(err))
	}
	defer rowsD.Close()
	for rowsD.Next() {
		var a string
		if err := rowsD.Scan(&a); err != nil {
			return nil, fmt.Errorf("storesqlite: scan coach dismiss action: %w", err)
		}
		actionSet[a] = struct{}{}
	}
	if err := rowsD.Err(); err != nil {
		return nil, fmt.Errorf("storesqlite: list coach actions (dismiss): %w", err)
	}

	if len(actionSet) == 0 {
		return nil, nil
	}

	// Aggregate events per action.
	type eventRow struct {
		action             string
		kb14, ms14         int64
		kb90, ms90         int64
		lastKbUs, lastMsUs sql.NullInt64
	}
	evMap := make(map[string]eventRow)
	rowsAgg, err := m.s.db.QueryContext(ctx, `
		SELECT
		  action,
		  COALESCE(SUM(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN event_count ELSE 0 END), 0),
		  MAX(CASE WHEN input_method='keyboard' AND occurred_at >= ? THEN occurred_at ELSE NULL END),
		  MAX(CASE WHEN input_method='mouse'    AND occurred_at >= ? THEN occurred_at ELSE NULL END)
		FROM coach_events
		WHERE principal_id = ? AND occurred_at >= ? AND occurred_at <= ?
		GROUP BY action`,
		w14, w14, w90, w90, w90, w90,
		int64(principalID), w90, nowUs)
	if err != nil {
		return nil, fmt.Errorf("storesqlite: agg coach events: %w", mapErr(err))
	}
	defer rowsAgg.Close()
	for rowsAgg.Next() {
		var r eventRow
		if err := rowsAgg.Scan(&r.action, &r.kb14, &r.ms14, &r.kb90, &r.ms90, &r.lastKbUs, &r.lastMsUs); err != nil {
			return nil, fmt.Errorf("storesqlite: scan coach agg: %w", err)
		}
		evMap[r.action] = r
	}
	if err := rowsAgg.Err(); err != nil {
		return nil, fmt.Errorf("storesqlite: agg coach events: %w", err)
	}

	// Load all dismissal rows.
	type dismissRow struct {
		dismissCount int64
		dismissUntil sql.NullInt64
	}
	dmMap := make(map[string]dismissRow)
	rowsDmAll, err := m.s.db.QueryContext(ctx, `
		SELECT action, dismiss_count, dismiss_until
		  FROM coach_dismiss WHERE principal_id = ?`,
		int64(principalID))
	if err != nil {
		return nil, fmt.Errorf("storesqlite: list coach_dismiss: %w", mapErr(err))
	}
	defer rowsDmAll.Close()
	for rowsDmAll.Next() {
		var a string
		var d dismissRow
		if err := rowsDmAll.Scan(&a, &d.dismissCount, &d.dismissUntil); err != nil {
			return nil, fmt.Errorf("storesqlite: scan coach_dismiss: %w", err)
		}
		dmMap[a] = d
	}
	if err := rowsDmAll.Err(); err != nil {
		return nil, fmt.Errorf("storesqlite: list coach_dismiss: %w", err)
	}

	// Assemble sorted slice.
	actions := make([]string, 0, len(actionSet))
	for a := range actionSet {
		actions = append(actions, a)
	}
	sortStrings(actions)

	out := make([]store.CoachStat, 0, len(actions))
	for _, a := range actions {
		stat := store.CoachStat{Action: a}
		if ev, ok := evMap[a]; ok {
			stat.KeyboardCount14d = int(ev.kb14)
			stat.MouseCount14d = int(ev.ms14)
			stat.KeyboardCount90d = int(ev.kb90)
			stat.MouseCount90d = int(ev.ms90)
			if ev.lastKbUs.Valid {
				t := fromMicros(ev.lastKbUs.Int64)
				stat.LastKeyboardAt = &t
			}
			if ev.lastMsUs.Valid {
				t := fromMicros(ev.lastMsUs.Int64)
				stat.LastMouseAt = &t
			}
		}
		if dm, ok := dmMap[a]; ok {
			stat.DismissCount = int(dm.dismissCount)
			if dm.dismissUntil.Valid {
				t := fromMicros(dm.dismissUntil.Int64)
				stat.DismissUntil = &t
			}
		}
		out = append(out, stat)
	}
	return out, nil
}

func (m *metadata) UpsertCoachDismiss(ctx context.Context, d store.CoachDismiss) error {
	now := m.s.clock.Now().UTC()
	var dismissUntilArg any
	if d.DismissUntil != nil {
		dismissUntilArg = usMicros(*d.DismissUntil)
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO coach_dismiss (principal_id, action, dismiss_count, dismiss_until, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(principal_id, action) DO UPDATE SET
				dismiss_count = excluded.dismiss_count,
				dismiss_until = excluded.dismiss_until,
				updated_at    = excluded.updated_at`,
			int64(d.PrincipalID), d.Action, d.DismissCount, dismissUntilArg, usMicros(now),
		)
		return mapErr(err)
	})
}

func (m *metadata) DestroyCoachStat(ctx context.Context, principalID store.PrincipalID, action string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM coach_events WHERE principal_id = ? AND action = ?`,
			int64(principalID), action); err != nil {
			return fmt.Errorf("storesqlite: delete coach_events: %w", mapErr(err))
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM coach_dismiss WHERE principal_id = ? AND action = ?`,
			int64(principalID), action); err != nil {
			return fmt.Errorf("storesqlite: delete coach_dismiss: %w", mapErr(err))
		}
		return nil
	})
}

func (m *metadata) DestroyAllCoachStats(ctx context.Context, principalID store.PrincipalID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM coach_events WHERE principal_id = ?`, int64(principalID)); err != nil {
			return fmt.Errorf("storesqlite: delete all coach_events: %w", mapErr(err))
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM coach_dismiss WHERE principal_id = ?`, int64(principalID)); err != nil {
			return fmt.Errorf("storesqlite: delete all coach_dismiss: %w", mapErr(err))
		}
		return nil
	})
}

func (m *metadata) GCCoachEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := m.s.db.ExecContext(ctx,
		`DELETE FROM coach_events WHERE occurred_at < ?`, usMicros(cutoff))
	if err != nil {
		return 0, fmt.Errorf("storesqlite: gc coach_events: %w", mapErr(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storesqlite: gc coach_events rows_affected: %w", err)
	}
	return n, nil
}
