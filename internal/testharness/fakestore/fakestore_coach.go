package fakestore

import (
	"context"
	"sort"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase 3 Wave 3.10 ShortcutCoachStat surface
// (REQ-PROTO-110..112) against the fakestore. Schema commentary lives in
// internal/storesqlite/migrations/0020_coach.sql; type definitions are in
// internal/store/types_coach.go.

// coachKey is the composite key for per-action event buckets.
type coachKey struct {
	Principal store.PrincipalID
	Action    string
}

// coachData groups the coach in-memory state. Attached to phase2Data.
type coachData struct {
	// events maps coachKey to a slice of stored events.
	events map[coachKey][]store.CoachEvent
	nextID int64
	// dismiss maps coachKey to the dismiss row.
	dismiss map[coachKey]store.CoachDismiss
}

// ensureCoach lazily initialises the coach in-memory state.
// Callers hold s.mu exclusively.
func (s *Store) ensureCoach() {
	s.ensurePhase2()
	if s.coach == nil {
		s.coach = &coachData{
			events:  make(map[coachKey][]store.CoachEvent),
			nextID:  1,
			dismiss: make(map[coachKey]store.CoachDismiss),
		}
	}
}

func (m *metaFace) AppendCoachEvents(ctx context.Context, events []store.CoachEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCoach()
	now := s.clk.Now()
	for _, ev := range events {
		if ev.Count <= 0 {
			ev.Count = 1
		}
		ev.ID = s.coach.nextID
		s.coach.nextID++
		ev.RecordedAt = now
		k := coachKey{Principal: ev.PrincipalID, Action: ev.Action}
		s.coach.events[k] = append(s.coach.events[k], ev)
	}
	return nil
}

func (m *metaFace) GetCoachStat(ctx context.Context, principalID store.PrincipalID, action string, now time.Time) (store.CoachStat, error) {
	if err := ctx.Err(); err != nil {
		return store.CoachStat{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()

	stat := store.CoachStat{Action: action}
	if s.coach == nil {
		return stat, nil
	}
	k := coachKey{Principal: principalID, Action: action}
	aggregateEvents(s.coach.events[k], now, &stat)
	if d, ok := s.coach.dismiss[k]; ok {
		stat.DismissCount = d.DismissCount
		if d.DismissUntil != nil {
			t := *d.DismissUntil
			stat.DismissUntil = &t
		}
	}
	return stat, nil
}

func (m *metaFace) ListCoachStats(ctx context.Context, principalID store.PrincipalID, now time.Time) ([]store.CoachStat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.coach == nil {
		return nil, nil
	}

	w90 := now.Add(-90 * 24 * time.Hour)
	actionSet := make(map[string]struct{})
	for k, evs := range s.coach.events {
		if k.Principal != principalID {
			continue
		}
		for _, ev := range evs {
			if !ev.OccurredAt.Before(w90) && !ev.OccurredAt.After(now) {
				actionSet[k.Action] = struct{}{}
				break
			}
		}
	}
	for k := range s.coach.dismiss {
		if k.Principal == principalID {
			actionSet[k.Action] = struct{}{}
		}
	}
	if len(actionSet) == 0 {
		return nil, nil
	}

	actions := make([]string, 0, len(actionSet))
	for a := range actionSet {
		actions = append(actions, a)
	}
	sort.Strings(actions)

	out := make([]store.CoachStat, 0, len(actions))
	for _, a := range actions {
		k := coachKey{Principal: principalID, Action: a}
		stat := store.CoachStat{Action: a}
		aggregateEvents(s.coach.events[k], now, &stat)
		if d, ok := s.coach.dismiss[k]; ok {
			stat.DismissCount = d.DismissCount
			if d.DismissUntil != nil {
				t := *d.DismissUntil
				stat.DismissUntil = &t
			}
		}
		out = append(out, stat)
	}
	return out, nil
}

// aggregateEvents populates the windowed fields of stat from evs relative to now.
func aggregateEvents(evs []store.CoachEvent, now time.Time, stat *store.CoachStat) {
	w14 := now.Add(-14 * 24 * time.Hour)
	w90 := now.Add(-90 * 24 * time.Hour)
	for _, ev := range evs {
		if ev.OccurredAt.Before(w90) || ev.OccurredAt.After(now) {
			continue
		}
		in14 := !ev.OccurredAt.Before(w14)
		switch ev.Method {
		case store.CoachInputMethodKeyboard:
			stat.KeyboardCount90d += ev.Count
			if in14 {
				stat.KeyboardCount14d += ev.Count
			}
			if stat.LastKeyboardAt == nil || ev.OccurredAt.After(*stat.LastKeyboardAt) {
				t := ev.OccurredAt
				stat.LastKeyboardAt = &t
			}
		case store.CoachInputMethodMouse:
			stat.MouseCount90d += ev.Count
			if in14 {
				stat.MouseCount14d += ev.Count
			}
			if stat.LastMouseAt == nil || ev.OccurredAt.After(*stat.LastMouseAt) {
				t := ev.OccurredAt
				stat.LastMouseAt = &t
			}
		}
	}
}

func (m *metaFace) UpsertCoachDismiss(ctx context.Context, d store.CoachDismiss) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCoach()
	now := s.clk.Now()
	k := coachKey{Principal: d.PrincipalID, Action: d.Action}
	d.UpdatedAt = now
	var dup store.CoachDismiss
	dup = d
	if d.DismissUntil != nil {
		t := *d.DismissUntil
		dup.DismissUntil = &t
	}
	s.coach.dismiss[k] = dup
	return nil
}

func (m *metaFace) DestroyCoachStat(ctx context.Context, principalID store.PrincipalID, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coach == nil {
		return nil
	}
	k := coachKey{Principal: principalID, Action: action}
	delete(s.coach.events, k)
	delete(s.coach.dismiss, k)
	return nil
}

func (m *metaFace) DestroyAllCoachStats(ctx context.Context, principalID store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coach == nil {
		return nil
	}
	for k := range s.coach.events {
		if k.Principal == principalID {
			delete(s.coach.events, k)
		}
	}
	for k := range s.coach.dismiss {
		if k.Principal == principalID {
			delete(s.coach.dismiss, k)
		}
	}
	return nil
}

func (m *metaFace) GCCoachEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.coach == nil {
		return 0, nil
	}
	var deleted int64
	for k, evs := range s.coach.events {
		var kept []store.CoachEvent
		for _, ev := range evs {
			if ev.OccurredAt.Before(cutoff) {
				deleted++
			} else {
				kept = append(kept, ev)
			}
		}
		if len(kept) == 0 {
			delete(s.coach.events, k)
		} else {
			s.coach.events[k] = kept
		}
	}
	return deleted, nil
}
