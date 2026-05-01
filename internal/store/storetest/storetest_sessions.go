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
