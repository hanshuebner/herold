package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testSessionUpsertGetRoundtrip verifies that UpsertSession stores a row and
// GetSession returns it with all fields preserved.
func testSessionUpsertGetRoundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-rt@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-token-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}

	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.SessionID != row.SessionID {
		t.Errorf("SessionID = %q; want %q", got.SessionID, row.SessionID)
	}
	if got.PrincipalID != pid {
		t.Errorf("PrincipalID = %d; want %d", got.PrincipalID, pid)
	}
	if !got.CreatedAt.Equal(row.CreatedAt) {
		t.Errorf("CreatedAt = %v; want %v", got.CreatedAt, row.CreatedAt)
	}
	if !got.ExpiresAt.Equal(row.ExpiresAt) {
		t.Errorf("ExpiresAt = %v; want %v", got.ExpiresAt, row.ExpiresAt)
	}
	if !got.ClientlogTelemetryEnabled {
		t.Error("ClientlogTelemetryEnabled = false; want true")
	}
	if got.ClientlogLivetailUntil != nil {
		t.Errorf("ClientlogLivetailUntil = %v; want nil", got.ClientlogLivetailUntil)
	}
}

// testSessionUpsertUpdatesOnConflict verifies that calling UpsertSession a
// second time with the same session_id overwrites the mutable columns.
func testSessionUpsertUpdatesOnConflict(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-upsert@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-upsert-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: false,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession first: %v", err)
	}

	// Second upsert: flip telemetry flag and extend expiry.
	row.ExpiresAt = now.Add(48 * time.Hour)
	row.ClientlogTelemetryEnabled = true
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession second: %v", err)
	}

	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after upsert: %v", err)
	}
	if !got.ClientlogTelemetryEnabled {
		t.Error("ClientlogTelemetryEnabled = false after upsert; want true")
	}
	if !got.ExpiresAt.Equal(row.ExpiresAt) {
		t.Errorf("ExpiresAt = %v; want %v", got.ExpiresAt, row.ExpiresAt)
	}
}

// testSessionGetNotFound verifies ErrNotFound for an unknown session ID.
func testSessionGetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	_, err := s.Meta().GetSession(ctx, "no-such-session")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSession unknown: got %v; want ErrNotFound", err)
	}
}

// testSessionDeleteRemovesRow verifies that DeleteSession removes the row and
// subsequent GetSession returns ErrNotFound.
func testSessionDeleteRemovesRow(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-del@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-del-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	if err := s.Meta().DeleteSession(ctx, row.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err := s.Meta().GetSession(ctx, row.SessionID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSession after delete: got %v; want ErrNotFound", err)
	}
}

// testSessionDeleteNotFound verifies that DeleteSession returns ErrNotFound
// when the session does not exist.
func testSessionDeleteNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	err := s.Meta().DeleteSession(ctx, "no-such-session-del")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteSession unknown: got %v; want ErrNotFound", err)
	}
}

// testSessionUpdateTelemetry verifies that UpdateSessionTelemetry flips the
// effective flag on an existing session row.
func testSessionUpdateTelemetry(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-tel@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-tel-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	if err := s.Meta().UpdateSessionTelemetry(ctx, row.SessionID, false); err != nil {
		t.Fatalf("UpdateSessionTelemetry to false: %v", err)
	}
	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}
	if got.ClientlogTelemetryEnabled {
		t.Error("ClientlogTelemetryEnabled = true after flip to false; want false")
	}

	if err := s.Meta().UpdateSessionTelemetry(ctx, row.SessionID, true); err != nil {
		t.Fatalf("UpdateSessionTelemetry to true: %v", err)
	}
	got, err = s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after second update: %v", err)
	}
	if !got.ClientlogTelemetryEnabled {
		t.Error("ClientlogTelemetryEnabled = false after flip to true; want true")
	}
}

// testSessionUpdateTelemetryNotFound verifies that UpdateSessionTelemetry
// returns ErrNotFound for an unknown session.
func testSessionUpdateTelemetryNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	err := s.Meta().UpdateSessionTelemetry(ctx, "no-such-session-tel", true)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateSessionTelemetry unknown: got %v; want ErrNotFound", err)
	}
}

// testSessionUpsertSetsLastSeen verifies that a freshly upserted session
// carries LastSeenAt = CreatedAt when the caller does not set the field
// explicitly (the store fills the gap so the idle clock starts at
// session birth, not at the SQL DEFAULT 0).
func testSessionUpsertSetsLastSeen(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-lastseen-fresh@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	row := store.SessionRow{
		SessionID:                 "csrf-lastseen-fresh",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(8 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.LastSeenAt.Equal(now) {
		t.Errorf("LastSeenAt = %v; want %v (= CreatedAt)", got.LastSeenAt, now)
	}
}

// testSessionUpdateLastSeenAdvances verifies that UpdateSessionLastSeen
// pushes the LastSeenAt column forward and that subsequent GetSession
// reads see the new value. Models the resolver's per-request touch.
func testSessionUpdateLastSeenAdvances(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-lastseen-advance@example.test").ID

	born := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	row := store.SessionRow{
		SessionID:                 "csrf-lastseen-advance",
		PrincipalID:               pid,
		CreatedAt:                 born,
		ExpiresAt:                 born.Add(8 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	later := born.Add(45 * time.Minute)
	if err := s.Meta().UpdateSessionLastSeen(ctx, row.SessionID, later.UnixMicro(), "10.0.0.2"); err != nil {
		t.Fatalf("UpdateSessionLastSeen: %v", err)
	}
	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after touch: %v", err)
	}
	if !got.LastSeenAt.Equal(later) {
		t.Errorf("LastSeenAt after touch = %v; want %v", got.LastSeenAt, later)
	}
	if !got.CreatedAt.Equal(born) {
		t.Errorf("CreatedAt should not have shifted; got %v want %v", got.CreatedAt, born)
	}
	if !got.ExpiresAt.Equal(born.Add(8 * time.Hour)) {
		t.Errorf("ExpiresAt should not have shifted; got %v", got.ExpiresAt)
	}
}

// testSessionUpdateLastSeenNotFound verifies that UpdateSessionLastSeen
// returns ErrNotFound when the session does not exist (already evicted
// or never created). The resolver depends on this signal to treat the
// session as logged out rather than silently swallowing the touch.
func testSessionUpdateLastSeenNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	err := s.Meta().UpdateSessionLastSeen(ctx, "no-such-session-lastseen", 0, "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateSessionLastSeen unknown: got %v; want ErrNotFound", err)
	}
}

// testSessionDeviceContextRoundtrip verifies that user_agent and last_seen_ip
// are stored at creation and that UpdateSessionLastSeen advances last_seen_ip.
func testSessionDeviceContextRoundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-devctx@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-devctx-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		UserAgent:                 "Mozilla/5.0 (test)",
		LastSeenIP:                "203.0.113.10",
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserAgent != row.UserAgent {
		t.Errorf("UserAgent = %q; want %q", got.UserAgent, row.UserAgent)
	}
	if got.LastSeenIP != row.LastSeenIP {
		t.Errorf("LastSeenIP = %q; want %q", got.LastSeenIP, row.LastSeenIP)
	}

	// UpdateSessionLastSeen should update last_seen_ip.
	newIP := "203.0.113.99"
	later := now.Add(10 * time.Minute)
	if err := s.Meta().UpdateSessionLastSeen(ctx, row.SessionID, later.UnixMicro(), newIP); err != nil {
		t.Fatalf("UpdateSessionLastSeen: %v", err)
	}
	got2, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after touch: %v", err)
	}
	if got2.LastSeenIP != newIP {
		t.Errorf("LastSeenIP after touch = %q; want %q", got2.LastSeenIP, newIP)
	}
	// user_agent must remain unchanged.
	if got2.UserAgent != row.UserAgent {
		t.Errorf("UserAgent changed on touch: got %q; want %q", got2.UserAgent, row.UserAgent)
	}
}

// testSessionEvictExpired verifies that EvictExpiredSessions removes rows
// whose ExpiresAt is in the past and leaves rows whose ExpiresAt is in the
// future.
func testSessionEvictExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-evict@example.test").ID

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := epoch.Add(-1 * time.Hour)
	future := epoch.Add(1 * time.Hour)

	expired := store.SessionRow{
		SessionID:                 "csrf-expired",
		PrincipalID:               pid,
		CreatedAt:                 past.Add(-24 * time.Hour),
		ExpiresAt:                 past,
		ClientlogTelemetryEnabled: true,
	}
	alive := store.SessionRow{
		SessionID:                 "csrf-alive",
		PrincipalID:               pid,
		CreatedAt:                 past,
		ExpiresAt:                 future,
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, expired); err != nil {
		t.Fatalf("UpsertSession expired: %v", err)
	}
	if err := s.Meta().UpsertSession(ctx, alive); err != nil {
		t.Fatalf("UpsertSession alive: %v", err)
	}

	deleted, err := s.Meta().EvictExpiredSessions(ctx, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("EvictExpiredSessions: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d; want 1", deleted)
	}

	// expired row must be gone
	if _, err := s.Meta().GetSession(ctx, expired.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSession expired: got %v; want ErrNotFound", err)
	}
	// alive row must still be there
	if _, err := s.Meta().GetSession(ctx, alive.SessionID); err != nil {
		t.Errorf("GetSession alive: %v", err)
	}
}

// testSessionClearExpiredLivetail verifies that ClearExpiredLivetail sets
// clientlog_livetail_until to NULL on rows whose timestamp is in the past and
// leaves rows with a future timestamp untouched.
func testSessionClearExpiredLivetail(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-clt@example.test").ID

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := epoch.Add(-1 * time.Hour)
	future := epoch.Add(1 * time.Hour)

	// expired livetail
	expiredRow := store.SessionRow{
		SessionID:                 "csrf-lt-expired",
		PrincipalID:               pid,
		CreatedAt:                 past.Add(-24 * time.Hour),
		ExpiresAt:                 future,
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &past,
	}
	// active livetail
	activeRow := store.SessionRow{
		SessionID:                 "csrf-lt-active",
		PrincipalID:               pid,
		CreatedAt:                 past.Add(-24 * time.Hour),
		ExpiresAt:                 future.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &future,
	}
	// no livetail
	noLivetailRow := store.SessionRow{
		SessionID:                 "csrf-lt-none",
		PrincipalID:               pid,
		CreatedAt:                 past.Add(-24 * time.Hour),
		ExpiresAt:                 future,
		ClientlogTelemetryEnabled: true,
	}

	if err := s.Meta().UpsertSession(ctx, expiredRow); err != nil {
		t.Fatalf("UpsertSession expired: %v", err)
	}
	if err := s.Meta().UpsertSession(ctx, activeRow); err != nil {
		t.Fatalf("UpsertSession active: %v", err)
	}
	if err := s.Meta().UpsertSession(ctx, noLivetailRow); err != nil {
		t.Fatalf("UpsertSession no-livetail: %v", err)
	}

	cleared, err := s.Meta().ClearExpiredLivetail(ctx, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("ClearExpiredLivetail: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d; want 1", cleared)
	}

	// expired row should have livetail cleared
	got, err := s.Meta().GetSession(ctx, expiredRow.SessionID)
	if err != nil {
		t.Fatalf("GetSession expired: %v", err)
	}
	if got.ClientlogLivetailUntil != nil {
		t.Errorf("expired livetail should be nil after sweep; got %v", got.ClientlogLivetailUntil)
	}

	// active row should still have its livetail
	got, err = s.Meta().GetSession(ctx, activeRow.SessionID)
	if err != nil {
		t.Fatalf("GetSession active: %v", err)
	}
	if got.ClientlogLivetailUntil == nil {
		t.Error("active livetail should still be set after sweep; got nil")
	}

	// no-livetail row should be unchanged
	got, err = s.Meta().GetSession(ctx, noLivetailRow.SessionID)
	if err != nil {
		t.Fatalf("GetSession no-livetail: %v", err)
	}
	if got.ClientlogLivetailUntil != nil {
		t.Errorf("no-livetail row should remain nil; got %v", got.ClientlogLivetailUntil)
	}
}

// testSessionListByPrincipal verifies that ListSessionsByPrincipal returns
// only active (non-expired, non-tombstoned) rows for the requested principal.
func testSessionListByPrincipal(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-list@example.test").ID
	other := mustInsertPrincipal(t, s, "session-list-other@example.test").ID

	epoch := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	future := epoch.Add(24 * time.Hour)
	past := epoch.Add(-1 * time.Hour)

	// two active sessions for pid
	a1 := store.SessionRow{SessionID: "list-a1", PrincipalID: pid,
		CreatedAt: epoch.Add(-2 * time.Hour), ExpiresAt: future}
	a2 := store.SessionRow{SessionID: "list-a2", PrincipalID: pid,
		CreatedAt: epoch.Add(-1 * time.Hour), ExpiresAt: future}
	// expired session for pid — must not appear
	expired := store.SessionRow{SessionID: "list-exp", PrincipalID: pid,
		CreatedAt: epoch.Add(-25 * time.Hour), ExpiresAt: past}
	// session for another principal — must not appear
	foreign := store.SessionRow{SessionID: "list-foreign", PrincipalID: other,
		CreatedAt: epoch, ExpiresAt: future}

	for _, r := range []store.SessionRow{a1, a2, expired, foreign} {
		if err := s.Meta().UpsertSession(ctx, r); err != nil {
			t.Fatalf("UpsertSession %s: %v", r.SessionID, err)
		}
	}

	list, err := s.Meta().ListSessionsByPrincipal(ctx, pid, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("ListSessionsByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSessionsByPrincipal returned %d rows; want 2", len(list))
	}
	// Most-recent first (a2 created later than a1).
	if list[0].SessionID != "list-a2" {
		t.Errorf("list[0].SessionID = %q; want list-a2", list[0].SessionID)
	}
	if list[1].SessionID != "list-a1" {
		t.Errorf("list[1].SessionID = %q; want list-a1", list[1].SessionID)
	}
	if list[0].Tombstoned || list[1].Tombstoned {
		t.Error("active sessions should not be tombstoned")
	}
}

// testSessionListByPrincipalEmpty verifies that ListSessionsByPrincipal
// returns an empty slice (not ErrNotFound) when the principal has no sessions.
func testSessionListByPrincipalEmpty(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-list-empty@example.test").ID

	list, err := s.Meta().ListSessionsByPrincipal(ctx, pid, time.Now().UnixMicro())
	if err != nil {
		t.Fatalf("ListSessionsByPrincipal: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListSessionsByPrincipal: got %d rows; want 0", len(list))
	}
}

// testSessionTombstone verifies that TombstoneSession marks revoked_at_us,
// that the session no longer appears in ListSessionsByPrincipal, and that
// GetSession still returns the row with Tombstoned = true until eviction.
func testSessionTombstone(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-tomb@example.test").ID

	epoch := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	row := store.SessionRow{SessionID: "tomb-1", PrincipalID: pid,
		CreatedAt: epoch.Add(-1 * time.Hour), ExpiresAt: epoch.Add(24 * time.Hour)}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	const ttl = int64(10 * 60 * 1e6) // 10 minutes in microseconds
	if err := s.Meta().TombstoneSession(ctx, row.SessionID, pid, epoch.UnixMicro(), ttl); err != nil {
		t.Fatalf("TombstoneSession: %v", err)
	}

	// GetSession should still find the row but with Tombstoned = true.
	got, err := s.Meta().GetSession(ctx, row.SessionID)
	if err != nil {
		t.Fatalf("GetSession after tombstone: %v", err)
	}
	if !got.Tombstoned {
		t.Error("Tombstoned = false after TombstoneSession; want true")
	}
	// expires_at should have been shortened to epoch + ttl.
	want := epoch.Add(10 * time.Minute)
	if !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt after tombstone = %v; want %v", got.ExpiresAt, want)
	}

	// The session must not appear in the active list.
	list, err := s.Meta().ListSessionsByPrincipal(ctx, pid, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("ListSessionsByPrincipal after tombstone: %v", err)
	}
	for _, r := range list {
		if r.SessionID == row.SessionID {
			t.Error("tombstoned session appeared in ListSessionsByPrincipal; want excluded")
		}
	}

	// After tombstone TTL elapses the row should be evicted.
	afterTTL := epoch.Add(11 * time.Minute).UnixMicro()
	deleted, err := s.Meta().EvictExpiredSessions(ctx, afterTTL)
	if err != nil {
		t.Fatalf("EvictExpiredSessions: %v", err)
	}
	if deleted == 0 {
		t.Error("EvictExpiredSessions: expected at least 1 row evicted (tombstoned session)")
	}
	if _, err := s.Meta().GetSession(ctx, row.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSession after eviction: got %v; want ErrNotFound", err)
	}
}

// testSessionTombstoneNotFound verifies that TombstoneSession returns
// ErrNotFound when the session_id does not exist, already belongs to a
// different principal, or has already been tombstoned.
func testSessionTombstoneNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-tomb-nf@example.test").ID
	other := mustInsertPrincipal(t, s, "session-tomb-nf-other@example.test").ID

	epoch := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	row := store.SessionRow{SessionID: "tomb-nf-1", PrincipalID: pid,
		CreatedAt: epoch.Add(-1 * time.Hour), ExpiresAt: epoch.Add(24 * time.Hour)}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	const ttl = int64(10 * 60 * 1e6)

	// Wrong principal.
	err := s.Meta().TombstoneSession(ctx, row.SessionID, other, epoch.UnixMicro(), ttl)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TombstoneSession wrong principal: got %v; want ErrNotFound", err)
	}

	// Nonexistent session.
	err = s.Meta().TombstoneSession(ctx, "no-such-session", pid, epoch.UnixMicro(), ttl)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TombstoneSession nonexistent: got %v; want ErrNotFound", err)
	}

	// Tombstone once, then attempt again — second call must return ErrNotFound.
	if err := s.Meta().TombstoneSession(ctx, row.SessionID, pid, epoch.UnixMicro(), ttl); err != nil {
		t.Fatalf("first TombstoneSession: %v", err)
	}
	err = s.Meta().TombstoneSession(ctx, row.SessionID, pid, epoch.UnixMicro(), ttl)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TombstoneSession already tombstoned: got %v; want ErrNotFound", err)
	}
}

// testSessionCascadeOnPrincipalDelete verifies that deleting a principal
// removes its session rows (ON DELETE CASCADE).
func testSessionCascadeOnPrincipalDelete(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "session-casc@example.test").ID

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{
		SessionID:                 "csrf-casc-1",
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}
	if err := s.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	if err := s.Meta().DeletePrincipal(ctx, pid); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}

	if _, err := s.Meta().GetSession(ctx, row.SessionID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetSession after principal delete: got %v; want ErrNotFound", err)
	}
}
