package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testElevationUpsertGetRoundtrip verifies that UpsertElevation stores a row
// and GetActiveElevation returns it when neither deadline has passed.
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
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Query with nowMicros before either deadline.
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
	if !got.IdleDeadline.Equal(elev.IdleDeadline) {
		t.Errorf("IdleDeadline = %v; want %v", got.IdleDeadline, elev.IdleDeadline)
	}
	if !got.AbsoluteDeadline.Equal(elev.AbsoluteDeadline) {
		t.Errorf("AbsoluteDeadline = %v; want %v", got.AbsoluteDeadline, elev.AbsoluteDeadline)
	}
}

// testElevationGetExpiredReturnsNotFound verifies that GetActiveElevation
// returns ErrNotFound when the elevation record's idle deadline is in the
// past relative to the supplied nowMicros.
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
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Query with nowMicros after the idle deadline (but well before the
	// absolute deadline) -- idle expiry alone must reject the row.
	afterIdleExpiry := now.Add(16 * time.Minute).UnixMicro()
	_, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, afterIdleExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after idle expiry: got %v; want ErrNotFound", err)
	}
}

// testElevationGetExpiredByAbsoluteCapReturnsNotFound verifies that
// GetActiveElevation rejects a row whose absolute deadline has elapsed even
// though its idle deadline (as originally granted) has not -- the two
// bounds are independent (REQ-AUTH-74, issue #225).
func testElevationGetExpiredByAbsoluteCapReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-abs-expired@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-abs-expired",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	elev := store.ElevationRow{
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(30 * time.Minute),
		AbsoluteDeadline: now.Add(20 * time.Minute),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// nowMicros is before IdleDeadline but after AbsoluteDeadline.
	afterAbsExpiry := now.Add(25 * time.Minute).UnixMicro()
	_, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, afterAbsExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after absolute-cap expiry: got %v; want ErrNotFound", err)
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
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, first); err != nil {
		t.Fatalf("UpsertElevation first: %v", err)
	}

	// Re-elevate: window shifts forward.
	later := now.Add(10 * time.Minute)
	second := store.ElevationRow{
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       later,
		IdleDeadline:     later.Add(15 * time.Minute),
		AbsoluteDeadline: later.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, second); err != nil {
		t.Fatalf("UpsertElevation second: %v", err)
	}

	// Original idle deadline (15 min from now) is still before the new
	// deadline (25 min from now); query between the two should still be active.
	midway := now.Add(20 * time.Minute).UnixMicro()
	got, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, midway)
	if err != nil {
		t.Fatalf("GetActiveElevation at midway: %v", err)
	}
	if !got.ElevatedAt.Equal(second.ElevatedAt) {
		t.Errorf("ElevatedAt = %v; want second elevation %v", got.ElevatedAt, second.ElevatedAt)
	}
	if !got.IdleDeadline.Equal(second.IdleDeadline) {
		t.Errorf("IdleDeadline = %v; want second elevation %v", got.IdleDeadline, second.IdleDeadline)
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
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
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
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
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
// whose idle deadline OR absolute deadline is in the past and leaves rows
// that are active on both bounds intact (REQ-AUTH-74, issue #225).
func testElevationEvictExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-evict@example.test").ID

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := epoch.Add(-1 * time.Minute)
	future := epoch.Add(15 * time.Minute)
	farFuture := epoch.Add(8 * time.Hour)

	// Parent sessions.
	sessIdleExpired := store.SessionRow{
		SessionID:   "elev-sess-evict-idle-expired",
		PrincipalID: pid,
		CreatedAt:   epoch.Add(-30 * time.Minute),
		ExpiresAt:   epoch.Add(7 * 24 * time.Hour),
	}
	sessAbsExpired := store.SessionRow{
		SessionID:   "elev-sess-evict-abs-expired",
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
	for _, sr := range []store.SessionRow{sessIdleExpired, sessAbsExpired, sessAlive} {
		if err := s.Meta().UpsertSession(ctx, sr); err != nil {
			t.Fatalf("UpsertSession %q: %v", sr.SessionID, err)
		}
	}

	elevIdleExpired := store.ElevationRow{
		SessionID:        sessIdleExpired.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-16 * time.Minute),
		IdleDeadline:     past,
		AbsoluteDeadline: farFuture,
	}
	elevAbsExpired := store.ElevationRow{
		SessionID:        sessAbsExpired.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-16 * time.Minute),
		IdleDeadline:     future,
		AbsoluteDeadline: past,
	}
	elevAlive := store.ElevationRow{
		SessionID:        sessAlive.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-1 * time.Minute),
		IdleDeadline:     future,
		AbsoluteDeadline: farFuture,
	}
	if err := s.Meta().UpsertElevation(ctx, elevIdleExpired); err != nil {
		t.Fatalf("UpsertElevation idle-expired: %v", err)
	}
	if err := s.Meta().UpsertElevation(ctx, elevAbsExpired); err != nil {
		t.Fatalf("UpsertElevation abs-expired: %v", err)
	}
	if err := s.Meta().UpsertElevation(ctx, elevAlive); err != nil {
		t.Fatalf("UpsertElevation alive: %v", err)
	}

	deleted, err := s.Meta().EvictExpiredElevations(ctx, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("EvictExpiredElevations: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d; want 2", deleted)
	}

	// Both expired elevations must be gone.
	if _, err := s.Meta().GetActiveElevation(ctx, elevIdleExpired.SessionID, epoch.Add(-2*time.Minute).UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation idle-expired: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveElevation(ctx, elevAbsExpired.SessionID, epoch.Add(-2*time.Minute).UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation abs-expired: got %v; want ErrNotFound", err)
	}
	// Alive elevation must still be present.
	if _, err := s.Meta().GetActiveElevation(ctx, elevAlive.SessionID, epoch.UnixMicro()); err != nil {
		t.Errorf("GetActiveElevation alive: %v", err)
	}
}

// testElevationExtendSlidesIdleDeadline verifies that ExtendElevation moves
// idle_deadline_us forward to now+idleTTL, past the row's originally granted
// idle deadline, when the new deadline stays within the absolute cap
// (REQ-AUTH-74, issue #225 -- the core sliding-window fix).
func testElevationExtendSlidesIdleDeadline(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-extend@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-extend",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	idleTTL := 15 * time.Minute
	elev := store.ElevationRow{
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(idleTTL),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Simulate an authenticated admin request at now+10m (before the
	// original 15m idle deadline) that passes the active-elevation check
	// and extends it.
	activityAt := now.Add(10 * time.Minute)
	if err := s.Meta().ExtendElevation(ctx, sess.SessionID, activityAt.UnixMicro(), idleTTL.Microseconds()); err != nil {
		t.Fatalf("ExtendElevation: %v", err)
	}

	// A query at the ORIGINAL fixed idle deadline (now+15m) must now
	// succeed: this is the sliding-window behaviour the fix adds. Before
	// the fix, this exact query returned ErrNotFound.
	got, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, now.Add(15*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveElevation at original fixed deadline after extension: %v", err)
	}
	wantIdleDeadline := activityAt.Add(idleTTL)
	if !got.IdleDeadline.Equal(wantIdleDeadline) {
		t.Errorf("IdleDeadline after extend = %v; want %v", got.IdleDeadline, wantIdleDeadline)
	}
	// AbsoluteDeadline must be untouched by the extension.
	if !got.AbsoluteDeadline.Equal(elev.AbsoluteDeadline) {
		t.Errorf("AbsoluteDeadline after extend = %v; want unchanged %v", got.AbsoluteDeadline, elev.AbsoluteDeadline)
	}
}

// testElevationExtendClampedToAbsoluteDeadline verifies that
// ExtendElevation never pushes idle_deadline_us past absolute_deadline_us,
// even when idleTTL alone would compute a later instant (REQ-AUTH-74,
// issue #225 -- continuous activity must not defeat the absolute cap).
func testElevationExtendClampedToAbsoluteDeadline(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-extend-clamp@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-extend-clamp",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	idleTTL := 15 * time.Minute
	absoluteDeadline := now.Add(20 * time.Minute)
	elev := store.ElevationRow{
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(idleTTL),
		AbsoluteDeadline: absoluteDeadline,
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	// Activity at now+10m: naive now+idleTTL = now+25m, which is past the
	// now+20m absolute deadline. The extension must clamp to the absolute
	// deadline, not to the naive sum.
	activityAt := now.Add(10 * time.Minute)
	if err := s.Meta().ExtendElevation(ctx, sess.SessionID, activityAt.UnixMicro(), idleTTL.Microseconds()); err != nil {
		t.Fatalf("ExtendElevation: %v", err)
	}
	got, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, now.Add(19*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveElevation before absolute cap: %v", err)
	}
	if !got.IdleDeadline.Equal(absoluteDeadline) {
		t.Errorf("IdleDeadline after clamped extend = %v; want absolute deadline %v", got.IdleDeadline, absoluteDeadline)
	}

	// A second extension attempt after the absolute deadline has elapsed
	// must fail (the row is no longer active) and must not resurrect it.
	pastAbsolute := absoluteDeadline.Add(time.Minute)
	if err := s.Meta().ExtendElevation(ctx, sess.SessionID, pastAbsolute.UnixMicro(), idleTTL.Microseconds()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ExtendElevation past absolute deadline: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, pastAbsolute.UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation past absolute deadline after failed extend: got %v; want ErrNotFound", err)
	}
}

// testElevationExtendNotFoundWhenIdleExpired verifies that ExtendElevation
// refuses to resurrect a row whose idle deadline has already elapsed
// (REQ-AUTH-74, issue #225).
func testElevationExtendNotFoundWhenIdleExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "elev-extend-idle-gone@example.test").ID

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sess := store.SessionRow{
		SessionID:   "elev-sess-extend-idle-gone",
		PrincipalID: pid,
		CreatedAt:   now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}
	if err := s.Meta().UpsertSession(ctx, sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	elev := store.ElevationRow{
		SessionID:        sess.SessionID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertElevation: %v", err)
	}

	pastIdle := now.Add(16 * time.Minute)
	if err := s.Meta().ExtendElevation(ctx, sess.SessionID, pastIdle.UnixMicro(), (15 * time.Minute).Microseconds()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ExtendElevation on idle-expired row: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveElevation(ctx, sess.SessionID, pastIdle.UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveElevation after failed extend on idle-expired row: got %v; want ErrNotFound", err)
	}
}
