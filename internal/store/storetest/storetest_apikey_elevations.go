package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// mustInsertAPIKeyElev inserts an api_keys row for pid and returns its ID.
// Named distinctly from other test helpers because the hash must be
// unique per call site across this file.
func mustInsertAPIKeyElev(t *testing.T, s store.Store, pid store.PrincipalID, hash string) store.APIKeyID {
	t.Helper()
	k, err := s.Meta().InsertAPIKey(ctxT(t), store.APIKey{
		PrincipalID: pid,
		Hash:        hash,
		Name:        "elev-test",
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	return k.ID
}

// testAPIKeyElevationUpsertGetRoundtrip verifies that UpsertAPIKeyElevation
// stores a row and GetActiveAPIKeyElevation returns it when neither
// deadline has passed (REQ-AUTH-74, REQ-AUTH-78, issue #357).
func testAPIKeyElevationUpsertGetRoundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-rt@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-rt-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	got, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, now.Add(1*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveAPIKeyElevation: %v", err)
	}
	if got.APIKeyID != elev.APIKeyID {
		t.Errorf("APIKeyID = %d; want %d", got.APIKeyID, elev.APIKeyID)
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

// testAPIKeyElevationGetExpiredReturnsNotFound verifies that
// GetActiveAPIKeyElevation returns ErrNotFound once the idle deadline has
// elapsed.
func testAPIKeyElevationGetExpiredReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-expired@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-expired-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	afterIdleExpiry := now.Add(16 * time.Minute).UnixMicro()
	_, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, afterIdleExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation after idle expiry: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationGetExpiredByAbsoluteCapReturnsNotFound verifies that
// GetActiveAPIKeyElevation rejects a row whose absolute deadline has
// elapsed even though its idle deadline has not (REQ-AUTH-74, issue #357).
func testAPIKeyElevationGetExpiredByAbsoluteCapReturnsNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-abs-expired@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-abs-expired-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(30 * time.Minute),
		AbsoluteDeadline: now.Add(20 * time.Minute),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	afterAbsExpiry := now.Add(25 * time.Minute).UnixMicro()
	_, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, afterAbsExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation after absolute-cap expiry: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationGetNotFound verifies that GetActiveAPIKeyElevation
// returns ErrNotFound when no elevation row exists for the given key ID.
func testAPIKeyElevationGetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	_, err := s.Meta().GetActiveAPIKeyElevation(ctx, store.APIKeyID(999999999), now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation unknown key: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationUpsertRefreshesWindow verifies that a second
// UpsertAPIKeyElevation for the same api_key_id overwrites the record so
// the elevation window resets.
func testAPIKeyElevationUpsertRefreshesWindow(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-refresh@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-refresh-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	first := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, first); err != nil {
		t.Fatalf("UpsertAPIKeyElevation first: %v", err)
	}

	later := now.Add(10 * time.Minute)
	second := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       later,
		IdleDeadline:     later.Add(15 * time.Minute),
		AbsoluteDeadline: later.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, second); err != nil {
		t.Fatalf("UpsertAPIKeyElevation second: %v", err)
	}

	midway := now.Add(20 * time.Minute).UnixMicro()
	got, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, midway)
	if err != nil {
		t.Fatalf("GetActiveAPIKeyElevation at midway: %v", err)
	}
	if !got.ElevatedAt.Equal(second.ElevatedAt) {
		t.Errorf("ElevatedAt = %v; want second elevation %v", got.ElevatedAt, second.ElevatedAt)
	}
	if !got.IdleDeadline.Equal(second.IdleDeadline) {
		t.Errorf("IdleDeadline = %v; want second elevation %v", got.IdleDeadline, second.IdleDeadline)
	}
}

// testAPIKeyElevationDeleteRemovesRow verifies that DeleteAPIKeyElevation
// removes the row and a subsequent GetActiveAPIKeyElevation returns
// ErrNotFound.
func testAPIKeyElevationDeleteRemovesRow(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-del@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-del-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	if err := s.Meta().DeleteAPIKeyElevation(ctx, keyID); err != nil {
		t.Fatalf("DeleteAPIKeyElevation: %v", err)
	}

	_, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation after delete: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationDeleteNotFound verifies that DeleteAPIKeyElevation
// returns ErrNotFound when no elevation row exists for the key.
func testAPIKeyElevationDeleteNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	err := s.Meta().DeleteAPIKeyElevation(ctx, store.APIKeyID(999999998))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteAPIKeyElevation unknown: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationCascadeOnKeyDelete verifies that deleting the parent
// api_keys row removes the elevation row automatically (ON DELETE CASCADE
// FK) -- revoking a device token or OAuth2 grant leaves no stray
// elevation behind (issue #357).
func testAPIKeyElevationCascadeOnKeyDelete(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-casc@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-casc-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	if err := s.Meta().DeleteAPIKey(ctx, keyID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}

	_, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, now.UnixMicro())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation after key delete (cascade): got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationEvictExpired verifies that
// EvictExpiredAPIKeyElevations removes rows whose idle deadline OR
// absolute deadline is in the past and leaves rows that are active on
// both bounds intact (REQ-AUTH-74, issue #357).
func testAPIKeyElevationEvictExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-evict@example.test").ID

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := epoch.Add(-1 * time.Minute)
	future := epoch.Add(15 * time.Minute)
	farFuture := epoch.Add(8 * time.Hour)

	keyIdleExpired := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-evict-idle-expired")
	keyAbsExpired := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-evict-abs-expired")
	keyAlive := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-evict-alive")

	elevIdleExpired := store.APIKeyElevationRow{
		APIKeyID:         keyIdleExpired,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-16 * time.Minute),
		IdleDeadline:     past,
		AbsoluteDeadline: farFuture,
	}
	elevAbsExpired := store.APIKeyElevationRow{
		APIKeyID:         keyAbsExpired,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-16 * time.Minute),
		IdleDeadline:     future,
		AbsoluteDeadline: past,
	}
	elevAlive := store.APIKeyElevationRow{
		APIKeyID:         keyAlive,
		PrincipalID:      pid,
		ElevatedAt:       epoch.Add(-1 * time.Minute),
		IdleDeadline:     future,
		AbsoluteDeadline: farFuture,
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elevIdleExpired); err != nil {
		t.Fatalf("UpsertAPIKeyElevation idle-expired: %v", err)
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elevAbsExpired); err != nil {
		t.Fatalf("UpsertAPIKeyElevation abs-expired: %v", err)
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elevAlive); err != nil {
		t.Fatalf("UpsertAPIKeyElevation alive: %v", err)
	}

	deleted, err := s.Meta().EvictExpiredAPIKeyElevations(ctx, epoch.UnixMicro())
	if err != nil {
		t.Fatalf("EvictExpiredAPIKeyElevations: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d; want 2", deleted)
	}

	if _, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyIdleExpired, epoch.Add(-2*time.Minute).UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation idle-expired: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyAbsExpired, epoch.Add(-2*time.Minute).UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation abs-expired: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyAlive, epoch.UnixMicro()); err != nil {
		t.Errorf("GetActiveAPIKeyElevation alive: %v", err)
	}
}

// testAPIKeyElevationExtendSlidesIdleDeadline verifies that
// ExtendAPIKeyElevation moves idle_deadline_us forward to now+idleTTL,
// past the row's originally granted idle deadline, when the new deadline
// stays within the absolute cap (REQ-AUTH-74, issue #357).
func testAPIKeyElevationExtendSlidesIdleDeadline(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-extend@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-extend-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	idleTTL := 15 * time.Minute
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(idleTTL),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	activityAt := now.Add(10 * time.Minute)
	if err := s.Meta().ExtendAPIKeyElevation(ctx, keyID, activityAt.UnixMicro(), idleTTL.Microseconds()); err != nil {
		t.Fatalf("ExtendAPIKeyElevation: %v", err)
	}

	got, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, now.Add(15*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveAPIKeyElevation at original fixed deadline after extension: %v", err)
	}
	wantIdleDeadline := activityAt.Add(idleTTL)
	if !got.IdleDeadline.Equal(wantIdleDeadline) {
		t.Errorf("IdleDeadline after extend = %v; want %v", got.IdleDeadline, wantIdleDeadline)
	}
	if !got.AbsoluteDeadline.Equal(elev.AbsoluteDeadline) {
		t.Errorf("AbsoluteDeadline after extend = %v; want unchanged %v", got.AbsoluteDeadline, elev.AbsoluteDeadline)
	}
}

// testAPIKeyElevationExtendClampedToAbsoluteDeadline verifies that
// ExtendAPIKeyElevation never pushes idle_deadline_us past
// absolute_deadline_us (REQ-AUTH-74, issue #357).
func testAPIKeyElevationExtendClampedToAbsoluteDeadline(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-extend-clamp@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-extend-clamp-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	idleTTL := 15 * time.Minute
	absoluteDeadline := now.Add(20 * time.Minute)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(idleTTL),
		AbsoluteDeadline: absoluteDeadline,
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	activityAt := now.Add(10 * time.Minute)
	if err := s.Meta().ExtendAPIKeyElevation(ctx, keyID, activityAt.UnixMicro(), idleTTL.Microseconds()); err != nil {
		t.Fatalf("ExtendAPIKeyElevation: %v", err)
	}
	got, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, now.Add(19*time.Minute).UnixMicro())
	if err != nil {
		t.Fatalf("GetActiveAPIKeyElevation before absolute cap: %v", err)
	}
	if !got.IdleDeadline.Equal(absoluteDeadline) {
		t.Errorf("IdleDeadline after clamped extend = %v; want absolute deadline %v", got.IdleDeadline, absoluteDeadline)
	}

	pastAbsolute := absoluteDeadline.Add(time.Minute)
	if err := s.Meta().ExtendAPIKeyElevation(ctx, keyID, pastAbsolute.UnixMicro(), idleTTL.Microseconds()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ExtendAPIKeyElevation past absolute deadline: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, pastAbsolute.UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation past absolute deadline after failed extend: got %v; want ErrNotFound", err)
	}
}

// testAPIKeyElevationExtendNotFoundWhenIdleExpired verifies that
// ExtendAPIKeyElevation refuses to resurrect a row whose idle deadline
// has already elapsed (REQ-AUTH-74, issue #357).
func testAPIKeyElevationExtendNotFoundWhenIdleExpired(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	pid := mustInsertPrincipal(t, s, "apikey-elev-extend-idle-gone@example.test").ID
	keyID := mustInsertAPIKeyElev(t, s, pid, "apikey-elev-extend-idle-gone-hash")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	elev := store.APIKeyElevationRow{
		APIKeyID:         keyID,
		PrincipalID:      pid,
		ElevatedAt:       now,
		IdleDeadline:     now.Add(15 * time.Minute),
		AbsoluteDeadline: now.Add(8 * time.Hour),
	}
	if err := s.Meta().UpsertAPIKeyElevation(ctx, elev); err != nil {
		t.Fatalf("UpsertAPIKeyElevation: %v", err)
	}

	pastIdle := now.Add(16 * time.Minute)
	if err := s.Meta().ExtendAPIKeyElevation(ctx, keyID, pastIdle.UnixMicro(), (15 * time.Minute).Microseconds()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ExtendAPIKeyElevation on idle-expired row: got %v; want ErrNotFound", err)
	}
	if _, err := s.Meta().GetActiveAPIKeyElevation(ctx, keyID, pastIdle.UnixMicro()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetActiveAPIKeyElevation after failed extend on idle-expired row: got %v; want ErrNotFound", err)
	}
}
