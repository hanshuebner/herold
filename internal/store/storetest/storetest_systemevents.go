package storetest

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testSystemEvents_AppendList verifies the basic append/list lifecycle for
// the system_events ring-buffer (REQ-ADM-304, re #142).
func testSystemEvents_AppendList(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	ev1 := store.SystemEvent{
		At:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Action:  "smtp.accept",
		ActorID: "smtp",
		Subject: "message:abc123",
		Outcome: store.OutcomeSuccess,
		Message: "test accept",
		Domain:  "example.com",
		Metadata: map[string]string{
			"mode": "inbound",
		},
	}
	ev2 := store.SystemEvent{
		At:      time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
		Action:  "smtp.rcpt.resolve",
		ActorID: "smtp",
		Subject: "rcpt:alice@example.com",
		Outcome: store.OutcomeSuccess,
		Message: "resolved",
		Domain:  "example.com",
	}

	if err := s.Meta().AppendSystemEvent(ctx, ev1); err != nil {
		t.Fatalf("AppendSystemEvent ev1: %v", err)
	}
	if err := s.Meta().AppendSystemEvent(ctx, ev2); err != nil {
		t.Fatalf("AppendSystemEvent ev2: %v", err)
	}

	evs, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListSystemEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("len(events) = %d; want 2", len(evs))
	}
	// Newest-first ordering: ev2 (later) comes first.
	if evs[0].Action != "smtp.rcpt.resolve" {
		t.Errorf("events[0].Action = %q; want smtp.rcpt.resolve", evs[0].Action)
	}
	if evs[1].Action != "smtp.accept" {
		t.Errorf("events[1].Action = %q; want smtp.accept", evs[1].Action)
	}
	// Check metadata round-trip.
	if evs[1].Metadata["mode"] != "inbound" {
		t.Errorf("events[1].Metadata[mode] = %q; want inbound", evs[1].Metadata["mode"])
	}
	// Domain preserved.
	if evs[0].Domain != "example.com" {
		t.Errorf("events[0].Domain = %q; want example.com", evs[0].Domain)
	}
}

// testSystemEvents_NewestFirst verifies that ListSystemEvents returns rows in
// descending id order.
func testSystemEvents_NewestFirst(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// Insert 5 events with sequential timestamps.
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      base.Add(time.Duration(i) * time.Second),
			Action:  "test.event",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %d: %v", i, err)
		}
	}

	evs, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListSystemEvents: %v", err)
	}
	if len(evs) < 5 {
		t.Fatalf("len(events) = %d; want >= 5", len(evs))
	}
	// Verify descending id order.
	for i := 1; i < len(evs); i++ {
		if evs[i].ID >= evs[i-1].ID {
			t.Errorf("events not in descending order: events[%d].ID=%d >= events[%d].ID=%d",
				i, evs[i].ID, i-1, evs[i-1].ID)
		}
	}
}

// testSystemEvents_EvictByCap verifies that EvictSystemEvents removes rows
// exceeding the row-count cap.
func testSystemEvents_EvictByCap(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// Insert 5 events.
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      base.Add(time.Duration(i) * time.Second),
			Action:  "cap.test",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %d: %v", i, err)
		}
	}

	// Evict keeping at most 3 rows, with a large max age so only cap triggers.
	deleted, err := s.Meta().EvictSystemEvents(ctx, store.SystemEventEvictOptions{
		CapRows:   3,
		MaxAge:    24 * time.Hour * 365 * 100, // 100 years — age never triggers
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("EvictSystemEvents: %v", err)
	}
	if deleted < 2 {
		t.Errorf("EvictSystemEvents deleted %d; want >= 2", deleted)
	}

	evs, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action: "cap.test",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents after eviction: %v", err)
	}
	if len(evs) > 3 {
		t.Errorf("after cap eviction: len(events) = %d; want <= 3", len(evs))
	}
}

// testSystemEvents_EvictByAge verifies that EvictSystemEvents removes rows
// older than MaxAge.
func testSystemEvents_EvictByAge(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// The compliance suite opens the store with a fake clock frozen at
	// 2026-01-01 00:00:00 UTC. EvictSystemEvents computes the age cutoff as
	// clock.Now() - MaxAge, so all timestamps must be before the clock's
	// current value.
	//
	// Old events: 2025-01-01 — clearly older than (2026-01-01 - 30min).
	// Recent events: 2025-12-31 23:59:30 — within 30s of the clock; survive.
	oldAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recentAt := time.Date(2025, 12, 31, 23, 59, 30, 0, time.UTC)

	// Insert 3 old events and 2 recent events.
	for i := 0; i < 3; i++ {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      oldAt,
			Action:  "age.old",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent old %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      recentAt,
			Action:  "age.recent",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent recent %d: %v", i, err)
		}
	}

	// Evict rows older than 30 minutes relative to the frozen store clock
	// (2026-01-01 00:00:00). Cutoff = 2025-12-31 23:30:00.
	// Old events at 2025-01-01 are far before the cutoff -> deleted (3).
	// Recent events at 2025-12-31 23:59:30 are after the cutoff -> retained.
	deleted, err := s.Meta().EvictSystemEvents(ctx, store.SystemEventEvictOptions{
		CapRows:   1000,             // cap never triggers
		MaxAge:    30 * time.Minute, // cutoff = clock.Now() - 30min
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("EvictSystemEvents: %v", err)
	}
	if deleted < 3 {
		t.Errorf("EvictSystemEvents deleted %d; want >= 3", deleted)
	}
}

// testSystemEvents_DomainFilter verifies that ListSystemEvents with a Domains
// filter returns only matching rows, and that a non-nil empty Domains returns
// nothing (REQ-ADM-307 fail-closed).
func testSystemEvents_DomainFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Insert events for two different domains plus one with no domain.
	for _, d := range []string{"alpha.test", "beta.test", ""} {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      base,
			Action:  "domain.test",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
			Domain:  d,
		}); err != nil {
			t.Fatalf("AppendSystemEvent domain=%q: %v", d, err)
		}
	}

	// Filter to alpha.test only.
	evs, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action:  "domain.test",
		Domains: []string{"alpha.test"},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents domain=alpha: %v", err)
	}
	if len(evs) != 1 || evs[0].Domain != "alpha.test" {
		t.Errorf("domain filter result = %+v; want 1 event with domain=alpha.test", evs)
	}

	// Multi-domain filter.
	evs, err = s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action:  "domain.test",
		Domains: []string{"alpha.test", "beta.test"},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents domain=alpha+beta: %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("multi-domain filter len = %d; want 2", len(evs))
	}

	// Non-nil empty Domains = fail-closed: no results.
	evs, err = s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action:  "domain.test",
		Domains: []string{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents empty domains: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("empty Domains filter: got %d results; want 0 (fail-closed)", len(evs))
	}

	// Nil Domains = unrestricted (super-admin): all three rows visible.
	evs, err = s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action:  "domain.test",
		Domains: nil,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents nil domains: %v", err)
	}
	if len(evs) != 3 {
		t.Errorf("nil Domains filter: got %d results; want 3 (unrestricted)", len(evs))
	}
}

// testSystemEvents_ActionFilter verifies that the Action filter narrows results.
func testSystemEvents_ActionFilter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// Insert events with different actions.
	for _, action := range []string{"a.accept", "b.reject", "a.accept"} {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      base,
			Action:  action,
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent action=%q: %v", action, err)
		}
	}

	evs, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
		Action: "a.accept",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents action=a.accept: %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("action filter result len = %d; want 2", len(evs))
	}
	for _, e := range evs {
		if e.Action != "a.accept" {
			t.Errorf("action filter returned action=%q; want a.accept", e.Action)
		}
	}
}

// testSystemEvents_CursorPagination verifies that before_id keyset pagination
// delivers all rows across pages.
func testSystemEvents_CursorPagination(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const total = 6
	for i := 0; i < total; i++ {
		if err := s.Meta().AppendSystemEvent(ctx, store.SystemEvent{
			At:      base.Add(time.Duration(i) * time.Second),
			Action:  "cursor.test",
			ActorID: "test",
			Outcome: store.OutcomeSuccess,
		}); err != nil {
			t.Fatalf("AppendSystemEvent %d: %v", i, err)
		}
	}

	// Paginate with page size 2.
	var allIDs []store.SystemEventID
	var beforeID store.SystemEventID
	for {
		page, err := s.Meta().ListSystemEvents(ctx, store.SystemEventFilter{
			Action:   "cursor.test",
			Limit:    2,
			BeforeID: beforeID,
		})
		if err != nil {
			t.Fatalf("ListSystemEvents page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			allIDs = append(allIDs, e.ID)
		}
		beforeID = page[len(page)-1].ID
	}
	if len(allIDs) != total {
		t.Errorf("pagination total = %d; want %d", len(allIDs), total)
	}
}
