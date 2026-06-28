package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testElevationUpsertGetRoundtrip verifies that UpsertElevation stores a row
// and GetActiveElevation returns it when the record has not yet expired.
func testElevationUpsertGetRoundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-rt@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-rt",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	elev := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  now,
		ExpiresAt:   now.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Query with nowMicros before the expiry.
	got, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, now.Add(1*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveElevation: %v", err)
	}
	if got.SessionID != elev.SessionID {
		t.Errorf("SessionID = %q; want %q", got.SessionID, elev.SessionID)
	}
	if got.PrincipalID != pid {
		t.Errorf("PrincipalID = %d; want %d", got.PrincipalID, pid)
	}
	if !got.ElevatedAt.Equal(elev.ElevatedAt) {
		t.Errorf("ElevatedAt = %v; want %v", got.ElevatedAt, elev.ElevatedAt)
	}
	if !got.ExpiresAt.Equal(elev.ExpiresAt) {
		t.Errorf("ExpiresAt = %v; want %v", got.ExpiresAt, elev.ExpiresAt)
	}
}

// testElevationGetExpiredReturnsNotFound verifies that GetActiveElevation
// returns ErrNotFound when the elevation record's expires_at is in the past
// relative to the supplied nowMicros.
func testElevationGetExpiredReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-expired@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-expired",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	elev := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  now,
		ExpiresAt:   now.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Query with nowMicros after the expiry.
	afterExpiry := now.Add(16 * time.Minute).UnixMicro()
	_, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, afterExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after expiry: got %v; want ErrNotFound", err)
	}
}

// testElevationGetNotFound verifies that GetActiveElevation returns ErrNotFound
// when no elevation row exists for the given session ID.
func testElevationGetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	_, err := s.Meta().GetActiveElevation(ctx, "no-such-session", now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation unknown session: got %v; want ErrNotFound", err)
	}
}

// testElevationUpsertRefreshesWindow verifies that a second UpsertElevation on
// the same session_id overwrites the record so the elevation window resets.
func testElevationUpsertRefreshesWindow(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-refresh@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-refresh",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	first := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  now,
		ExpiresAt:   now.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, first); err != nil {
		t.Fatalf("UpsertElevation first: %v", err)
	}

	// Re-elevate: window shifts forward.
	later := now.Add(10 * time.Minute)
	second := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  later,
		ExpiresAt:   later.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, second); err != nil {
		t.Fatalf("UpsertElevation second: %v", err)
	}

	// Original expiry (15 min from now) is still before the new expiry
	// (25 min from now); query between the two should still return active.
	midway := now.Add(20 * time.Minute).UnixMicro()
	got, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, midway)
	if err != nil {
		t.Fatalf("GetActiveElevation at midway: %v", err)
	}
	if !got.ElevatedAt.Equal(second.ElevatedAt) {
		t.Errorf("ElevatedAt = %v; want second elevation %v", got.ElevatedAt, second.ElevatedAt)
	}
	if !got.ExpiresAt.Equal(second.ExpiresAt) {
		t.Errorf("ExpiresAt = %v; want second elevation %v", got.ExpiresAt, second.ExpiresAt)
	}
}

// testElevationDeleteRemovesRow verifies that DeleteElevation removes the row
// and a subsequent GetActiveElevation returns ErrNotFound.
func testElevationDeleteRemovesRow(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-del@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-del",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	elev := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  now,
		ExpiresAt:   now.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	if err := s.Meta().DeleteElevation(ctx, sess.SessionID); err != nil {
		t.Fatalf("DeleteElevation: %v", err)
	}

	_, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after delete: got %v; want ErrNotFound", err)
	}
}

// testElevationDeleteNotFound verifies that DeleteElevation returns ErrNotFound
// when no elevation row exists for the session.
func testElevationDeleteNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	err := s.Meta().DeleteElevation(ctx, "no-such-session-del-elev")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteElevation unknown: got %v; want ErrNotFound", err)
	}
}

// testElevationCascadeOnSessionDelete verifies that deleting the parent session
// removes the elevation row automatically (ON DELETE CASCADE FK).
func testElevationCascadeOnSessionDelete(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-casc@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-casc",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	elev := store.ElevationRow{
		SessionID:   sess.SessionID,
		PrincipalID: pid,
		ElevatedAt:  now,
		ExpiresAt:   now.Add(15 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Delete the parent session; the elevation row must cascade.
	if err := s.Meta().DeleteSession(ctx, sess.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after session delete (cascade): got %v; want ErrNotFound", err)
	}
}

// testElevationEvictExpired verifies that EvictExpiredElevations removes rows
// whose expires_at is in the past and leaves unexpired rows intact.
func testElevationEvictExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-evict@example.test").ID

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := epoch.Add(-1 * time.Minute)
	future := epoch.Add(15 * time.Minute)

	// Parent sessions.
	sessExpired := store.SessionRow{
		SessionID:   "elev-sess-evict-expired",
		PrincipalID: pid,
		CreatedAt:   epoch.Add(-30 * time.Minute),
		ExpiresAt:   epoch.Add(7 * 24 * time.Hour),
	}
	sessAlive := store.SessionRow{
		SessionID:   "elev-sess-evict-alive",
		PrincipalID: pid,
		CreatedAt:   epoch.Add(-5 * time.Minute),
		ExpiresAt:   epoch.Add(7 * 24 * time.Hour),
	}
	for _, sr := range []store.SessionRow{sessExpired, sessAlive} {
		if err := s.Meta().UpsertSession(ctx, sr); err != nil {
			t.Fatalf("UpsertSession %q: %v", sr.SessionID, err)
		}
	}

	elevExpired := store.ElevationRow{
		SessionID:   sessExpired.SessionID,
		PrincipalID: pid,
		ElevatedAt:  epoch.Add(-16 * time.Minute),
		ExpiresAt:   past,
	}
	elevAlive := store.ElevationRow{
		SessionID:   sessAlive.SessionID,
		PrincipalID: pid,
		ElevatedAt:  epoch.Add(-1 * time.Minute),
		ExpiresAt:   future,
	}
	if err := s.Meta().UpsertElevation(ctx, elevExpired); err != nil {
		t.Fatalf("UpsertElevation expired: %v", err)
	}
	if err := s.Meta().UpsertElevation(ctx, elevAlive); err != nil {
		t.Fatalf("UpsertElevation alive: %v", err)
	}

	deleted, err := s.Meta().EvictExpiredElevations(ctx, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("EvictExpiredElevations: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d; want 1", deleted)
	}

	// Expired elevation must be gone.
	if _, err := s.Meta().GetActiveElevation(ctx, elevExpired.SessionID, epoch.Add(-2*time.Minute).UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation expired: got %v; want ErrNotFound", err)
	}
	// Alive elevation must still be present.
	if _, err := s.Meta().GetActiveElevation(ctx, elevAlive.SessionID, epoch.UnixMicro()); err != nil {
		t.Errorf("GetActiveElevation alive: %v", err)
	}
}
