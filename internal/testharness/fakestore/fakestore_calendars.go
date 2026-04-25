package fakestore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.7 JMAP for Calendars surface
// (REQ-PROTO-54) against the fakestore. The schema-side commentary
// lives in internal/storesqlite/migrations/0011_calendars.sql; the
// type definitions are in internal/store/types_calendars.go.

// -- Calendar ---------------------------------------------------------

func (m *metaFace) InsertCalendar(ctx context.Context, c store.Calendar) (store.CalendarID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	// (principal_id, name) uniqueness — JMAP allows duplicate names, but
	// the storage surface mirrors mailbox / address-book name uniqueness
	// for v1 consistency. Tests can always pick distinct names; the
	// unique constraint is a backstop, not a restriction the JMAP layer
	// exposes to clients.
	for _, e := range s.phase2.calendars {
		if e.PrincipalID == c.PrincipalID && e.Name == c.Name {
			return 0, fmt.Errorf("calendar %q for principal %d: %w", c.Name, c.PrincipalID, store.ErrConflict)
		}
	}
	if c.IsDefault {
		for id, e := range s.phase2.calendars {
			if e.PrincipalID == c.PrincipalID && e.IsDefault {
				e.IsDefault = false
				e.UpdatedAt = now
				s.phase2.calendars[id] = e
			}
		}
	}
	c.ID = s.phase2.nextCalendar
	s.phase2.nextCalendar++
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.ModSeq = 1
	if c.Color != nil {
		v := *c.Color
		c.Color = &v
	}
	s.phase2.calendars[c.ID] = c
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: c.PrincipalID,
		Kind:        store.EntityKindCalendar,
		EntityID:    uint64(c.ID),
		Op:          store.ChangeOpCreated,
		ProducedAt:  now,
	})
	return c.ID, nil
}

func (m *metaFace) GetCalendar(ctx context.Context, id store.CalendarID) (store.Calendar, error) {
	if err := ctx.Err(); err != nil {
		return store.Calendar{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.Calendar{}, fmt.Errorf("calendar %d: %w", id, store.ErrNotFound)
	}
	c, ok := s.phase2.calendars[id]
	if !ok {
		return store.Calendar{}, fmt.Errorf("calendar %d: %w", id, store.ErrNotFound)
	}
	if c.Color != nil {
		v := *c.Color
		c.Color = &v
	}
	return c, nil
}

func (m *metaFace) ListCalendars(ctx context.Context, filter store.CalendarFilter) ([]store.Calendar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.Calendar
	for _, c := range s.phase2.calendars {
		if filter.PrincipalID != nil && c.PrincipalID != *filter.PrincipalID {
			continue
		}
		if filter.AfterModSeq != 0 && c.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && c.ID <= filter.AfterID {
			continue
		}
		if c.Color != nil {
			v := *c.Color
			c.Color = &v
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateCalendar(ctx context.Context, c store.Calendar) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.calendars[c.ID]
	if !ok {
		return fmt.Errorf("calendar %d: %w", c.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	if c.IsDefault && !cur.IsDefault {
		for id, e := range s.phase2.calendars {
			if id == c.ID || e.PrincipalID != cur.PrincipalID {
				continue
			}
			if e.IsDefault {
				e.IsDefault = false
				e.UpdatedAt = now
				s.phase2.calendars[id] = e
			}
		}
	}
	cur.Name = c.Name
	cur.Description = c.Description
	if c.Color != nil {
		v := *c.Color
		cur.Color = &v
	} else {
		cur.Color = nil
	}
	cur.SortOrder = c.SortOrder
	cur.IsSubscribed = c.IsSubscribed
	cur.IsDefault = c.IsDefault
	cur.IsVisible = c.IsVisible
	cur.TimeZoneID = c.TimeZoneID
	cur.RightsMask = c.RightsMask
	cur.UpdatedAt = now
	cur.ModSeq++
	s.phase2.calendars[c.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindCalendar,
		EntityID:    uint64(c.ID),
		Op:          store.ChangeOpUpdated,
		ProducedAt:  now,
	})
	return nil
}

func (m *metaFace) DeleteCalendar(ctx context.Context, id store.CalendarID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.calendars[id]
	if !ok {
		return fmt.Errorf("calendar %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Cascade: remove every event owned by this calendar and append
	// per-row destroyed state-change entries.
	var eventIDs []store.CalendarEventID
	for eid, e := range s.phase2.calendarEvents {
		if e.CalendarID == id {
			eventIDs = append(eventIDs, eid)
		}
	}
	sort.Slice(eventIDs, func(i, j int) bool { return eventIDs[i] < eventIDs[j] })
	for _, eid := range eventIDs {
		e := s.phase2.calendarEvents[eid]
		delete(s.phase2.calendarEvents, eid)
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID:    e.PrincipalID,
			Kind:           store.EntityKindCalendarEvent,
			EntityID:       uint64(eid),
			ParentEntityID: uint64(id),
			Op:             store.ChangeOpDestroyed,
			ProducedAt:     now,
		})
	}
	delete(s.phase2.calendars, id)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: cur.PrincipalID,
		Kind:        store.EntityKindCalendar,
		EntityID:    uint64(id),
		Op:          store.ChangeOpDestroyed,
		ProducedAt:  now,
	})
	return nil
}

func (m *metaFace) DefaultCalendar(ctx context.Context, principalID store.PrincipalID) (store.Calendar, error) {
	if err := ctx.Err(); err != nil {
		return store.Calendar{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.Calendar{}, fmt.Errorf("default calendar for %d: %w", principalID, store.ErrNotFound)
	}
	for _, c := range s.phase2.calendars {
		if c.PrincipalID == principalID && c.IsDefault {
			if c.Color != nil {
				v := *c.Color
				c.Color = &v
			}
			return c, nil
		}
	}
	return store.Calendar{}, fmt.Errorf("default calendar for %d: %w", principalID, store.ErrNotFound)
}

// -- CalendarEvent ----------------------------------------------------

func (m *metaFace) InsertCalendarEvent(ctx context.Context, e store.CalendarEvent) (store.CalendarEventID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for _, ex := range s.phase2.calendarEvents {
		if ex.CalendarID == e.CalendarID && ex.UID == e.UID {
			return 0, fmt.Errorf("event %q in calendar %d: %w", e.UID, e.CalendarID, store.ErrConflict)
		}
	}
	now := s.clk.Now()
	e.ID = s.phase2.nextCalendarEvent
	s.phase2.nextCalendarEvent++
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	e.ModSeq = 1
	e.JSCalendarJSON = cloneBytes(e.JSCalendarJSON)
	e.RRuleJSON = cloneBytes(e.RRuleJSON)
	e.OrganizerEmail = strings.ToLower(e.OrganizerEmail)
	e.Status = strings.ToLower(e.Status)
	e.Start = e.Start.UTC()
	e.End = e.End.UTC()
	s.phase2.calendarEvents[e.ID] = e
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    e.PrincipalID,
		Kind:           store.EntityKindCalendarEvent,
		EntityID:       uint64(e.ID),
		ParentEntityID: uint64(e.CalendarID),
		Op:             store.ChangeOpCreated,
		ProducedAt:     now,
	})
	return e.ID, nil
}

func (m *metaFace) GetCalendarEvent(ctx context.Context, id store.CalendarEventID) (store.CalendarEvent, error) {
	if err := ctx.Err(); err != nil {
		return store.CalendarEvent{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.CalendarEvent{}, fmt.Errorf("calendar event %d: %w", id, store.ErrNotFound)
	}
	e, ok := s.phase2.calendarEvents[id]
	if !ok {
		return store.CalendarEvent{}, fmt.Errorf("calendar event %d: %w", id, store.ErrNotFound)
	}
	e.JSCalendarJSON = cloneBytes(e.JSCalendarJSON)
	e.RRuleJSON = cloneBytes(e.RRuleJSON)
	return e, nil
}

func (m *metaFace) GetCalendarEventByUID(ctx context.Context, calendarID store.CalendarID, uid string) (store.CalendarEvent, error) {
	if err := ctx.Err(); err != nil {
		return store.CalendarEvent{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.CalendarEvent{}, fmt.Errorf("calendar event uid %q in calendar %d: %w", uid, calendarID, store.ErrNotFound)
	}
	for _, e := range s.phase2.calendarEvents {
		if e.CalendarID == calendarID && e.UID == uid {
			e.JSCalendarJSON = cloneBytes(e.JSCalendarJSON)
			e.RRuleJSON = cloneBytes(e.RRuleJSON)
			return e, nil
		}
	}
	return store.CalendarEvent{}, fmt.Errorf("calendar event uid %q in calendar %d: %w", uid, calendarID, store.ErrNotFound)
}

func (m *metaFace) ListCalendarEvents(ctx context.Context, filter store.CalendarEventFilter) ([]store.CalendarEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	text := strings.ToLower(filter.Text)
	var status string
	if filter.Status != nil {
		status = strings.ToLower(*filter.Status)
	}
	var out []store.CalendarEvent
	for _, e := range s.phase2.calendarEvents {
		if filter.CalendarID != nil && e.CalendarID != *filter.CalendarID {
			continue
		}
		if filter.PrincipalID != nil && e.PrincipalID != *filter.PrincipalID {
			continue
		}
		if filter.UID != nil && e.UID != *filter.UID {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.Summary), text) {
			continue
		}
		if filter.StartAfter != nil && e.Start.Before(filter.StartAfter.UTC()) {
			continue
		}
		if filter.StartBefore != nil && !e.Start.Before(filter.StartBefore.UTC()) {
			continue
		}
		if filter.Status != nil && e.Status != status {
			continue
		}
		if filter.AfterModSeq != 0 && e.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && e.ID <= filter.AfterID {
			continue
		}
		e.JSCalendarJSON = cloneBytes(e.JSCalendarJSON)
		e.RRuleJSON = cloneBytes(e.RRuleJSON)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateCalendarEvent(ctx context.Context, e store.CalendarEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.calendarEvents[e.ID]
	if !ok {
		return fmt.Errorf("calendar event %d: %w", e.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	cur.JSCalendarJSON = cloneBytes(e.JSCalendarJSON)
	cur.RRuleJSON = cloneBytes(e.RRuleJSON)
	cur.Start = e.Start.UTC()
	cur.End = e.End.UTC()
	cur.IsRecurring = e.IsRecurring
	cur.Summary = e.Summary
	cur.OrganizerEmail = strings.ToLower(e.OrganizerEmail)
	cur.Status = strings.ToLower(e.Status)
	cur.UpdatedAt = now
	cur.ModSeq++
	s.phase2.calendarEvents[e.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    cur.PrincipalID,
		Kind:           store.EntityKindCalendarEvent,
		EntityID:       uint64(cur.ID),
		ParentEntityID: uint64(cur.CalendarID),
		Op:             store.ChangeOpUpdated,
		ProducedAt:     now,
	})
	return nil
}

func (m *metaFace) DeleteCalendarEvent(ctx context.Context, id store.CalendarEventID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.calendarEvents[id]
	if !ok {
		return fmt.Errorf("calendar event %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	delete(s.phase2.calendarEvents, id)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    cur.PrincipalID,
		Kind:           store.EntityKindCalendarEvent,
		EntityID:       uint64(id),
		ParentEntityID: uint64(cur.CalendarID),
		Op:             store.ChangeOpDestroyed,
		ProducedAt:     now,
	})
	return nil
}
