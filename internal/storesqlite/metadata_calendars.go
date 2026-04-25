package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.7 store.Metadata methods for
// JMAP for Calendars (REQ-PROTO-54, RFC 8984 JSCalendar). The
// schema-side commentary lives in migrations/0011_calendars.sql;
// helpers reused from metadata.go (mapErr, runTx, usMicros, fromMicros,
// appendStateChange) keep the surface narrow.

// -- Calendar ---------------------------------------------------------

const calendarSelectColumns = `
	id, principal_id, name, description, color_hex, sort_order,
	is_subscribed, is_default, is_visible, time_zone_id, rights_mask,
	created_at_us, updated_at_us, modseq`

func scanCalendar(row rowLike) (store.Calendar, error) {
	var (
		id, pid                          int64
		sortOrder, rightsMask            int64
		subscribed, isDefault, isVisible int64
		createdUs, updatedUs, modseq     int64
		name, description, timeZoneID    string
		color                            sql.NullString
	)
	err := row.Scan(&id, &pid, &name, &description, &color, &sortOrder,
		&subscribed, &isDefault, &isVisible, &timeZoneID, &rightsMask,
		&createdUs, &updatedUs, &modseq)
	if err != nil {
		return store.Calendar{}, mapErr(err)
	}
	c := store.Calendar{
		ID:           store.CalendarID(id),
		PrincipalID:  store.PrincipalID(pid),
		Name:         name,
		Description:  description,
		SortOrder:    int(sortOrder),
		IsSubscribed: subscribed != 0,
		IsDefault:    isDefault != 0,
		IsVisible:    isVisible != 0,
		TimeZoneID:   timeZoneID,
		RightsMask:   store.ACLRights(rightsMask),
		CreatedAt:    fromMicros(createdUs),
		UpdatedAt:    fromMicros(updatedUs),
		ModSeq:       store.ModSeq(modseq),
	}
	if color.Valid {
		v := color.String
		c.Color = &v
	}
	return c, nil
}

func (m *metadata) InsertCalendar(ctx context.Context, c store.Calendar) (store.CalendarID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// IsDefault enforcement: when the caller asks for default, flip
		// the previous default off in the same tx. The partial unique
		// index would reject the second is_default=1 otherwise.
		if c.IsDefault {
			if _, err := tx.ExecContext(ctx,
				`UPDATE calendars SET is_default = 0, updated_at_us = ?
				   WHERE principal_id = ? AND is_default = 1`,
				usMicros(now), int64(c.PrincipalID)); err != nil {
				return mapErr(err)
			}
		}
		var color any
		if c.Color != nil {
			color = *c.Color
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO calendars (principal_id, name, description, color_hex,
			  sort_order, is_subscribed, is_default, is_visible, time_zone_id,
			  rights_mask, created_at_us, updated_at_us, modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(c.PrincipalID), c.Name, c.Description, color,
			int64(c.SortOrder), boolToInt(c.IsSubscribed), boolToInt(c.IsDefault),
			boolToInt(c.IsVisible), c.TimeZoneID, int64(c.RightsMask),
			usMicros(now), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, c.PrincipalID,
			store.EntityKindCalendar, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.CalendarID(id), nil
}

func (m *metadata) GetCalendar(ctx context.Context, id store.CalendarID) (store.Calendar, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+calendarSelectColumns+` FROM calendars WHERE id = ?`,
		int64(id))
	return scanCalendar(row)
}

func (m *metadata) ListCalendars(ctx context.Context, filter store.CalendarFilter) ([]store.Calendar, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	if filter.PrincipalID != nil {
		clauses = append(clauses, "principal_id = ?")
		args = append(args, int64(*filter.PrincipalID))
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + calendarSelectColumns + ` FROM calendars`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Calendar
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateCalendar(ctx context.Context, c store.Calendar) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Lookup the current row to discover the owning principal (for
		// the state-change feed) and to decide whether IsDefault changed.
		var (
			pid        int64
			wasDefault int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, is_default FROM calendars WHERE id = ?`,
			int64(c.ID)).Scan(&pid, &wasDefault)
		if err != nil {
			return mapErr(err)
		}
		if c.IsDefault && wasDefault == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE calendars SET is_default = 0, updated_at_us = ?
				   WHERE principal_id = ? AND is_default = 1 AND id <> ?`,
				usMicros(now), pid, int64(c.ID)); err != nil {
				return mapErr(err)
			}
		}
		var color any
		if c.Color != nil {
			color = *c.Color
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE calendars SET
			  name = ?, description = ?, color_hex = ?, sort_order = ?,
			  is_subscribed = ?, is_default = ?, is_visible = ?, time_zone_id = ?,
			  rights_mask = ?, updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`,
			c.Name, c.Description, color, int64(c.SortOrder),
			boolToInt(c.IsSubscribed), boolToInt(c.IsDefault), boolToInt(c.IsVisible),
			c.TimeZoneID, int64(c.RightsMask),
			usMicros(now), int64(c.ID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendar, uint64(c.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteCalendar(ctx context.Context, id store.CalendarID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM calendars WHERE id = ?`,
			int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		// Capture the event IDs before the FK cascade wipes them so we
		// can append per-event destroyed rows.
		eventRows, err := tx.QueryContext(ctx,
			`SELECT id FROM calendar_events WHERE calendar_id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		var eventIDs []int64
		for eventRows.Next() {
			var eid int64
			if err := eventRows.Scan(&eid); err != nil {
				eventRows.Close()
				return mapErr(err)
			}
			eventIDs = append(eventIDs, eid)
		}
		eventRows.Close()
		res, err := tx.ExecContext(ctx,
			`DELETE FROM calendars WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		// Per-event destroyed rows first, then the calendar row.
		for _, eid := range eventIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindCalendarEvent, uint64(eid), uint64(id), store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendar, uint64(id), 0, store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) DefaultCalendar(ctx context.Context, principalID store.PrincipalID) (store.Calendar, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+calendarSelectColumns+`
		   FROM calendars WHERE principal_id = ? AND is_default = 1
		  LIMIT 1`,
		int64(principalID))
	return scanCalendar(row)
}

// -- CalendarEvent ----------------------------------------------------

const calendarEventSelectColumns = `
	id, calendar_id, principal_id, uid, jscalendar_json,
	start_us, end_us, is_recurring, rrule_json,
	summary, organizer_email, status,
	created_at_us, updated_at_us, modseq`

func scanCalendarEvent(row rowLike) (store.CalendarEvent, error) {
	var (
		id, calID, pid               int64
		uid, summary, status         string
		js                           []byte
		rrule                        []byte
		startUs, endUs               int64
		isRecurring                  int64
		organizer                    sql.NullString
		createdUs, updatedUs, modseq int64
	)
	err := row.Scan(&id, &calID, &pid, &uid, &js,
		&startUs, &endUs, &isRecurring, &rrule,
		&summary, &organizer, &status,
		&createdUs, &updatedUs, &modseq)
	if err != nil {
		return store.CalendarEvent{}, mapErr(err)
	}
	e := store.CalendarEvent{
		ID:             store.CalendarEventID(id),
		CalendarID:     store.CalendarID(calID),
		PrincipalID:    store.PrincipalID(pid),
		UID:            uid,
		JSCalendarJSON: js,
		Start:          fromMicros(startUs),
		End:            fromMicros(endUs),
		IsRecurring:    isRecurring != 0,
		RRuleJSON:      rrule,
		Summary:        summary,
		Status:         status,
		CreatedAt:      fromMicros(createdUs),
		UpdatedAt:      fromMicros(updatedUs),
		ModSeq:         store.ModSeq(modseq),
	}
	if organizer.Valid {
		e.OrganizerEmail = organizer.String
	}
	return e, nil
}

func (m *metadata) InsertCalendarEvent(ctx context.Context, e store.CalendarEvent) (store.CalendarEventID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var organizer any
		if e.OrganizerEmail != "" {
			organizer = strings.ToLower(e.OrganizerEmail)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO calendar_events (calendar_id, principal_id, uid, jscalendar_json,
			  start_us, end_us, is_recurring, rrule_json,
			  summary, organizer_email, status,
			  created_at_us, updated_at_us, modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(e.CalendarID), int64(e.PrincipalID), e.UID, e.JSCalendarJSON,
			usMicros(e.Start.UTC()), usMicros(e.End.UTC()),
			boolToInt(e.IsRecurring), e.RRuleJSON,
			e.Summary, organizer, strings.ToLower(e.Status),
			usMicros(now), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, e.PrincipalID,
			store.EntityKindCalendarEvent, uint64(id), uint64(e.CalendarID),
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.CalendarEventID(id), nil
}

func (m *metadata) GetCalendarEvent(ctx context.Context, id store.CalendarEventID) (store.CalendarEvent, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+calendarEventSelectColumns+` FROM calendar_events WHERE id = ?`,
		int64(id))
	return scanCalendarEvent(row)
}

func (m *metadata) GetCalendarEventByUID(ctx context.Context, calendarID store.CalendarID, uid string) (store.CalendarEvent, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+calendarEventSelectColumns+`
		   FROM calendar_events WHERE calendar_id = ? AND uid = ?`,
		int64(calendarID), uid)
	return scanCalendarEvent(row)
}

func (m *metadata) ListCalendarEvents(ctx context.Context, filter store.CalendarEventFilter) ([]store.CalendarEvent, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	if filter.CalendarID != nil {
		clauses = append(clauses, "calendar_id = ?")
		args = append(args, int64(*filter.CalendarID))
	}
	if filter.PrincipalID != nil {
		clauses = append(clauses, "principal_id = ?")
		args = append(args, int64(*filter.PrincipalID))
	}
	if filter.UID != nil {
		clauses = append(clauses, "uid = ?")
		args = append(args, *filter.UID)
	}
	if filter.Text != "" {
		clauses = append(clauses, "LOWER(summary) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.Text)+"%")
	}
	if filter.StartAfter != nil {
		clauses = append(clauses, "start_us >= ?")
		args = append(args, usMicros(filter.StartAfter.UTC()))
	}
	if filter.StartBefore != nil {
		clauses = append(clauses, "start_us < ?")
		args = append(args, usMicros(filter.StartBefore.UTC()))
	}
	if filter.Status != nil {
		clauses = append(clauses, "status = ?")
		args = append(args, strings.ToLower(*filter.Status))
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + calendarEventSelectColumns + ` FROM calendar_events`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.CalendarEvent
	for rows.Next() {
		e, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateCalendarEvent(ctx context.Context, e store.CalendarEvent) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			pid int64
			cal int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, calendar_id FROM calendar_events WHERE id = ?`,
			int64(e.ID)).Scan(&pid, &cal)
		if err != nil {
			return mapErr(err)
		}
		var organizer any
		if e.OrganizerEmail != "" {
			organizer = strings.ToLower(e.OrganizerEmail)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE calendar_events SET
			  jscalendar_json = ?, start_us = ?, end_us = ?,
			  is_recurring = ?, rrule_json = ?,
			  summary = ?, organizer_email = ?, status = ?,
			  updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`,
			e.JSCalendarJSON, usMicros(e.Start.UTC()), usMicros(e.End.UTC()),
			boolToInt(e.IsRecurring), e.RRuleJSON,
			e.Summary, organizer, strings.ToLower(e.Status),
			usMicros(now), int64(e.ID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendarEvent, uint64(e.ID), uint64(cal),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteCalendarEvent(ctx context.Context, id store.CalendarEventID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			pid int64
			cal int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, calendar_id FROM calendar_events WHERE id = ?`,
			int64(id)).Scan(&pid, &cal)
		if err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM calendar_events WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendarEvent, uint64(id), uint64(cal),
			store.ChangeOpDestroyed, now)
	})
}

// silence unused-import warnings in case time is removed by future
// edits.
var _ = time.Time{}
