package storepg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.7 store.Metadata methods for
// JMAP for Calendars (REQ-PROTO-54, RFC 8984 JSCalendar) against
// Postgres. The schema-side commentary lives in
// migrations/0011_calendars.sql.

// -- Calendar ---------------------------------------------------------

const calendarSelectColumnsPG = `
	id, principal_id, name, description, color_hex, sort_order,
	is_subscribed, is_default, is_visible, time_zone_id, rights_mask,
	created_at_us, updated_at_us, modseq`

func scanCalendarPG(row pgx.Row) (store.Calendar, error) {
	var (
		id, pid                          int64
		sortOrder, rightsMask            int64
		subscribed, isDefault, isVisible bool
		createdUs, updatedUs, modseq     int64
		name, description, timeZoneID    string
		color                            *string
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
		IsSubscribed: subscribed,
		IsDefault:    isDefault,
		IsVisible:    isVisible,
		TimeZoneID:   timeZoneID,
		RightsMask:   store.ACLRights(rightsMask),
		CreatedAt:    fromMicros(createdUs),
		UpdatedAt:    fromMicros(updatedUs),
		ModSeq:       store.ModSeq(modseq),
	}
	if color != nil {
		v := *color
		c.Color = &v
	}
	return c, nil
}

func (m *metadata) InsertCalendar(ctx context.Context, c store.Calendar) (store.CalendarID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if c.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE calendars SET is_default = FALSE, updated_at_us = $1
				   WHERE principal_id = $2 AND is_default = TRUE`,
				usMicros(now), int64(c.PrincipalID)); err != nil {
				return mapErr(err)
			}
		}
		var color *string
		if c.Color != nil {
			v := *c.Color
			color = &v
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO calendars (principal_id, name, description, color_hex,
			  sort_order, is_subscribed, is_default, is_visible, time_zone_id,
			  rights_mask, created_at_us, updated_at_us, modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1)
			RETURNING id`,
			int64(c.PrincipalID), c.Name, c.Description, color,
			int64(c.SortOrder), c.IsSubscribed, c.IsDefault, c.IsVisible,
			c.TimeZoneID, int64(c.RightsMask),
			usMicros(now), usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, c.PrincipalID,
			store.EntityKindCalendar, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.CalendarID(id), nil
}

func (m *metadata) GetCalendar(ctx context.Context, id store.CalendarID) (store.Calendar, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+calendarSelectColumnsPG+` FROM calendars WHERE id = $1`,
		int64(id))
	return scanCalendarPG(row)
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
	idx := 1
	if filter.PrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("principal_id = $%d", idx))
		args = append(args, int64(*filter.PrincipalID))
		idx++
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, fmt.Sprintf("modseq > $%d", idx))
		args = append(args, int64(filter.AfterModSeq))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + calendarSelectColumnsPG + ` FROM calendars`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.Calendar
	for rows.Next() {
		c, err := scanCalendarPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateCalendar(ctx context.Context, c store.Calendar) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid        int64
			wasDefault bool
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, is_default FROM calendars WHERE id = $1`,
			int64(c.ID)).Scan(&pid, &wasDefault)
		if err != nil {
			return mapErr(err)
		}
		if c.IsDefault && !wasDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE calendars SET is_default = FALSE, updated_at_us = $1
				   WHERE principal_id = $2 AND is_default = TRUE AND id <> $3`,
				usMicros(now), pid, int64(c.ID)); err != nil {
				return mapErr(err)
			}
		}
		var color *string
		if c.Color != nil {
			v := *c.Color
			color = &v
		}
		tag, err := tx.Exec(ctx, `
			UPDATE calendars SET
			  name = $1, description = $2, color_hex = $3, sort_order = $4,
			  is_subscribed = $5, is_default = $6, is_visible = $7, time_zone_id = $8,
			  rights_mask = $9, updated_at_us = $10, modseq = modseq + 1
			 WHERE id = $11`,
			c.Name, c.Description, color, int64(c.SortOrder),
			c.IsSubscribed, c.IsDefault, c.IsVisible, c.TimeZoneID,
			int64(c.RightsMask), usMicros(now), int64(c.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendar, uint64(c.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteCalendar(ctx context.Context, id store.CalendarID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		err := tx.QueryRow(ctx,
			`SELECT principal_id FROM calendars WHERE id = $1`,
			int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		eventRows, err := tx.Query(ctx,
			`SELECT id FROM calendar_events WHERE calendar_id = $1`, int64(id))
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
		tag, err := tx.Exec(ctx,
			`DELETE FROM calendars WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		for _, eid := range eventIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindCalendarEvent, uint64(eid), uint64(id),
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendar, uint64(id), 0,
			store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) DefaultCalendar(ctx context.Context, principalID store.PrincipalID) (store.Calendar, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+calendarSelectColumnsPG+`
		   FROM calendars WHERE principal_id = $1 AND is_default = TRUE
		  LIMIT 1`,
		int64(principalID))
	return scanCalendarPG(row)
}

// -- CalendarEvent ----------------------------------------------------

const calendarEventSelectColumnsPG = `
	id, calendar_id, principal_id, uid, jscalendar_json,
	start_us, end_us, is_recurring, rrule_json,
	summary, organizer_email, status,
	created_at_us, updated_at_us, modseq`

func scanCalendarEventPG(row pgx.Row) (store.CalendarEvent, error) {
	var (
		id, calID, pid               int64
		uid, summary, status         string
		js                           []byte
		rrule                        []byte
		startUs, endUs               int64
		isRecurring                  bool
		organizer                    *string
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
		IsRecurring:    isRecurring,
		RRuleJSON:      rrule,
		Summary:        summary,
		Status:         status,
		CreatedAt:      fromMicros(createdUs),
		UpdatedAt:      fromMicros(updatedUs),
		ModSeq:         store.ModSeq(modseq),
	}
	if organizer != nil {
		e.OrganizerEmail = *organizer
	}
	return e, nil
}

func (m *metadata) InsertCalendarEvent(ctx context.Context, e store.CalendarEvent) (store.CalendarEventID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var organizer *string
		if e.OrganizerEmail != "" {
			v := strings.ToLower(e.OrganizerEmail)
			organizer = &v
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO calendar_events (calendar_id, principal_id, uid, jscalendar_json,
			  start_us, end_us, is_recurring, rrule_json,
			  summary, organizer_email, status,
			  created_at_us, updated_at_us, modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1)
			RETURNING id`,
			int64(e.CalendarID), int64(e.PrincipalID), e.UID, e.JSCalendarJSON,
			usMicros(e.Start.UTC()), usMicros(e.End.UTC()),
			e.IsRecurring, e.RRuleJSON,
			e.Summary, organizer, strings.ToLower(e.Status),
			usMicros(now), usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
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
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+calendarEventSelectColumnsPG+` FROM calendar_events WHERE id = $1`,
		int64(id))
	return scanCalendarEventPG(row)
}

func (m *metadata) GetCalendarEventByUID(ctx context.Context, calendarID store.CalendarID, uid string) (store.CalendarEvent, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+calendarEventSelectColumnsPG+`
		   FROM calendar_events WHERE calendar_id = $1 AND uid = $2`,
		int64(calendarID), uid)
	return scanCalendarEventPG(row)
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
	idx := 1
	if filter.CalendarID != nil {
		clauses = append(clauses, fmt.Sprintf("calendar_id = $%d", idx))
		args = append(args, int64(*filter.CalendarID))
		idx++
	}
	if filter.PrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("principal_id = $%d", idx))
		args = append(args, int64(*filter.PrincipalID))
		idx++
	}
	if filter.UID != nil {
		clauses = append(clauses, fmt.Sprintf("uid = $%d", idx))
		args = append(args, *filter.UID)
		idx++
	}
	if filter.Text != "" {
		clauses = append(clauses, fmt.Sprintf("LOWER(summary) LIKE $%d", idx))
		args = append(args, "%"+strings.ToLower(filter.Text)+"%")
		idx++
	}
	if filter.StartAfter != nil {
		clauses = append(clauses, fmt.Sprintf("start_us >= $%d", idx))
		args = append(args, usMicros(filter.StartAfter.UTC()))
		idx++
	}
	if filter.StartBefore != nil {
		clauses = append(clauses, fmt.Sprintf("start_us < $%d", idx))
		args = append(args, usMicros(filter.StartBefore.UTC()))
		idx++
	}
	if filter.Status != nil {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, strings.ToLower(*filter.Status))
		idx++
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, fmt.Sprintf("modseq > $%d", idx))
		args = append(args, int64(filter.AfterModSeq))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + calendarEventSelectColumnsPG + ` FROM calendar_events`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.CalendarEvent
	for rows.Next() {
		e, err := scanCalendarEventPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateCalendarEvent(ctx context.Context, e store.CalendarEvent) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid int64
			cal int64
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, calendar_id FROM calendar_events WHERE id = $1`,
			int64(e.ID)).Scan(&pid, &cal)
		if err != nil {
			return mapErr(err)
		}
		var organizer *string
		if e.OrganizerEmail != "" {
			v := strings.ToLower(e.OrganizerEmail)
			organizer = &v
		}
		tag, err := tx.Exec(ctx, `
			UPDATE calendar_events SET
			  jscalendar_json = $1, start_us = $2, end_us = $3,
			  is_recurring = $4, rrule_json = $5,
			  summary = $6, organizer_email = $7, status = $8,
			  updated_at_us = $9, modseq = modseq + 1
			 WHERE id = $10`,
			e.JSCalendarJSON, usMicros(e.Start.UTC()), usMicros(e.End.UTC()),
			e.IsRecurring, e.RRuleJSON,
			e.Summary, organizer, strings.ToLower(e.Status),
			usMicros(now), int64(e.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendarEvent, uint64(e.ID), uint64(cal),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteCalendarEvent(ctx context.Context, id store.CalendarEventID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid int64
			cal int64
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, calendar_id FROM calendar_events WHERE id = $1`,
			int64(id)).Scan(&pid, &cal)
		if err != nil {
			return mapErr(err)
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM calendar_events WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindCalendarEvent, uint64(id), uint64(cal),
			store.ChangeOpDestroyed, now)
	})
}
