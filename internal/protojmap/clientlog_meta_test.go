package protojmap_test

// clientlog_meta_test.go tests the urn:netzhansa:params:jmap:clientlog
// capability injection (REQ-OPS-211, REQ-CLOG-05, REQ-CLOG-12).
//
// Tests cover:
//   - The clientlog capability always appears in the session descriptor.
//   - telemetry_enabled reflects the sessions row value; changing it and
//     re-fetching the session returns the new value.
//   - livetail_until appears when set to a future time, is omitted when
//     null or past.
//   - The session state changes when livetail or telemetry columns change,
//     causing the client to re-fetch the descriptor.
//   - ClearExpiredLivetail (the sweeper's core operation) clears expired
//     livetail rows.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// fetchSessionDescriptor fetches GET /.well-known/jmap and returns the
// parsed descriptor as a map. Requires Bearer auth.
func fetchSessionDescriptor(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	res, body := f.doRequest("GET", "/.well-known/jmap", f.apiKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session descriptor: status %d body %s", res.StatusCode, body)
	}
	var desc map[string]any
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("session descriptor: unmarshal: %v", err)
	}
	return desc
}

// clientlogCapFromDesc extracts the clientlog capability map from a
// session descriptor.
func clientlogCapFromDesc(t *testing.T, desc map[string]any) map[string]any {
	t.Helper()
	caps, ok := desc["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing: %v", desc["capabilities"])
	}
	clRaw, ok := caps[string(protojmap.CapabilityClientLog)]
	if !ok {
		t.Fatalf("clientlog capability absent; caps = %v", caps)
	}
	clCap, ok := clRaw.(map[string]any)
	if !ok {
		t.Fatalf("clientlog capability wrong type %T", clRaw)
	}
	return clCap
}

// upsertSessionRow is a helper that creates or replaces a session row.
func upsertSessionRow(ctx context.Context, t *testing.T, st store.Store, row store.SessionRow) {
	t.Helper()
	if err := st.Meta().UpsertSession(ctx, row); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
}

// newServerForStore constructs a minimal protojmap.Server backed by
// the given store. The server has no listeners; tests call exported test
// methods directly.
func newServerForStore(t *testing.T, fs store.Store, clk clock.Clock) *protojmap.Server {
	t.Helper()
	srv := protojmap.NewServer(fs, nil, nil, nil, clk, protojmap.Options{
		DownloadRatePerSec: -1,
	})
	return srv
}

// newClientlogFixture constructs a store, seeds a domain, creates a
// principal and returns the store, clock, principal ID, and server.
func newClientlogFixture(t *testing.T) (store.Store, *clock.FakeClock, store.PrincipalID, *protojmap.Server) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	pid, err := dir.CreatePrincipal(ctx, "test@example.com", "password-placeholder-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	srv := newServerForStore(t, fs, clk)
	return fs, clk, pid, srv
}

// TestClientlogCapabilityAlwaysPresent verifies that the clientlog
// capability always appears in the session descriptor, even for Bearer
// auth where no session row exists.
func TestClientlogCapabilityAlwaysPresent(t *testing.T) {
	f := newFixture(t)
	desc := fetchSessionDescriptor(t, f)
	clCap := clientlogCapFromDesc(t, desc)
	// Bearer auth has no session row -> telemetry defaults to false.
	if v, _ := clCap["telemetry_enabled"].(bool); v {
		t.Errorf("telemetry_enabled = true; want false for Bearer auth (no session row)")
	}
	if _, ok := clCap["livetail_until"]; ok {
		t.Errorf("livetail_until present; want absent when no session row")
	}
}

// TestClientlogTelemetryEnabledReflectsSession verifies that the
// telemetry_enabled field reflects the sessions row, and that changing
// the row via UpdateSessionTelemetry is visible immediately.
func TestClientlogTelemetryEnabledReflectsSession(t *testing.T) {
	ctx := context.Background()
	fs, clk, pid, srv := newClientlogFixture(t)
	now := clk.Now()
	sessID := "csrf-telemetry-test"

	// Insert with telemetry=true.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	})

	desc := srv.BuildClientlogCapForTest(ctx, sessID)
	if !desc.TelemetryEnabled {
		t.Error("telemetry_enabled should be true after insert with true")
	}

	// Flip to false.
	if err := fs.Meta().UpdateSessionTelemetry(ctx, sessID, false); err != nil {
		t.Fatalf("UpdateSessionTelemetry: %v", err)
	}
	desc = srv.BuildClientlogCapForTest(ctx, sessID)
	if desc.TelemetryEnabled {
		t.Error("telemetry_enabled should be false after flip")
	}

	// Flip back to true.
	if err := fs.Meta().UpdateSessionTelemetry(ctx, sessID, true); err != nil {
		t.Fatalf("UpdateSessionTelemetry back: %v", err)
	}
	desc = srv.BuildClientlogCapForTest(ctx, sessID)
	if !desc.TelemetryEnabled {
		t.Error("telemetry_enabled should be true after flip back")
	}
}

// TestClientlogLivetailObservable verifies that livetail_until appears
// in the capability when set to a future timestamp and is omitted when
// null or in the past.
func TestClientlogLivetailObservable(t *testing.T) {
	ctx := context.Background()
	fs, clk, pid, srv := newClientlogFixture(t)
	now := clk.Now()
	future := now.Add(5 * time.Minute)
	past := now.Add(-1 * time.Minute)
	sessID := "csrf-livetail-test"

	// No session row: livetail absent.
	desc := srv.BuildClientlogCapForTest(ctx, sessID)
	if desc.LivetailUntil != nil {
		t.Errorf("no session row: LivetailUntil should be nil, got %v", desc.LivetailUntil)
	}

	// Insert row with future livetail.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &future,
	})

	desc = srv.BuildClientlogCapForTest(ctx, sessID)
	if desc.LivetailUntil == nil {
		t.Fatal("livetail_until should be present when set to future time")
	}
	// Verify RFC 3339 with millisecond precision.
	ts, err := time.Parse("2006-01-02T15:04:05.000Z07:00", *desc.LivetailUntil)
	if err != nil {
		t.Errorf("livetail_until parse error: %v (value=%q)", err, *desc.LivetailUntil)
	} else if !ts.Equal(future.UTC().Truncate(time.Millisecond)) {
		t.Errorf("livetail_until = %v; want %v", ts, future.UTC().Truncate(time.Millisecond))
	}

	// Advance clock past the livetail expiry — must be omitted.
	clk.SetNow(now.Add(10 * time.Minute))
	desc = srv.BuildClientlogCapForTest(ctx, sessID)
	if desc.LivetailUntil != nil {
		t.Errorf("livetail_until should be omitted after clock passes expiry; got %v", desc.LivetailUntil)
	}

	// Reset clock; set livetail to a past timestamp — still omitted.
	clk.SetNow(now)
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &past,
	})
	desc = srv.BuildClientlogCapForTest(ctx, sessID)
	if desc.LivetailUntil != nil {
		t.Errorf("past livetail_until should be omitted; got %v", desc.LivetailUntil)
	}
}

// TestLivetailSweeper verifies that ClearExpiredLivetail (the core
// operation the sweeper goroutine runs) clears expired rows while
// leaving active rows untouched.
func TestLivetailSweeper(t *testing.T) {
	ctx := context.Background()
	fs, clk, pid, _ := newClientlogFixture(t)
	now := clk.Now()
	expiredLivetail := now.Add(-2 * time.Minute)
	activeLivetail := now.Add(5 * time.Minute)

	// Insert an expired-livetail session row.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 "csrf-sweep-expired",
		PrincipalID:               pid,
		CreatedAt:                 now.Add(-1 * time.Hour),
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &expiredLivetail,
	})
	// Insert an active-livetail session row.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 "csrf-sweep-active",
		PrincipalID:               pid,
		CreatedAt:                 now.Add(-1 * time.Hour),
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &activeLivetail,
	})
	// Insert a row with no livetail.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 "csrf-sweep-none",
		PrincipalID:               pid,
		CreatedAt:                 now.Add(-1 * time.Hour),
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	})

	cleared, err := fs.Meta().ClearExpiredLivetail(ctx, now.UnixMicro())
	if err != nil {
		t.Fatalf("ClearExpiredLivetail: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d; want 1", cleared)
	}

	// Expired row must have livetail cleared.
	got, err := fs.Meta().GetSession(ctx, "csrf-sweep-expired")
	if err != nil {
		t.Fatalf("GetSession expired: %v", err)
	}
	if got.ClientlogLivetailUntil != nil {
		t.Errorf("expired livetail should be nil after sweep; got %v", got.ClientlogLivetailUntil)
	}

	// Active row must be untouched.
	got, err = fs.Meta().GetSession(ctx, "csrf-sweep-active")
	if err != nil {
		t.Fatalf("GetSession active: %v", err)
	}
	if got.ClientlogLivetailUntil == nil {
		t.Error("active livetail should remain after sweep; got nil")
	}

	// No-livetail row must be untouched.
	got, err = fs.Meta().GetSession(ctx, "csrf-sweep-none")
	if err != nil {
		t.Fatalf("GetSession no-livetail: %v", err)
	}
	if got.ClientlogLivetailUntil != nil {
		t.Errorf("no-livetail row should remain nil; got %v", got.ClientlogLivetailUntil)
	}
}

// TestClientlogSessionStateChanges verifies that the session state hash
// changes when the clientlog columns are modified (REQ-CLOG-05: client
// re-fetches the session descriptor when state changes).
func TestClientlogSessionStateChanges(t *testing.T) {
	ctx := context.Background()
	fs, clk, pid, srv := newClientlogFixture(t)
	now := clk.Now()
	sessID := "csrf-state-change"

	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: false,
	})
	state1 := srv.SessionStateForTest(ctx, pid, sessID)

	// Flip telemetry; state must change.
	if err := fs.Meta().UpdateSessionTelemetry(ctx, sessID, true); err != nil {
		t.Fatalf("UpdateSessionTelemetry: %v", err)
	}
	state2 := srv.SessionStateForTest(ctx, pid, sessID)
	if state1 == state2 {
		t.Error("session state did not change after telemetry flip")
	}

	// Add a livetail; state must change again.
	future := now.Add(5 * time.Minute)
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
		ClientlogLivetailUntil:    &future,
	})
	state3 := srv.SessionStateForTest(ctx, pid, sessID)
	if state2 == state3 {
		t.Error("session state did not change after livetail set")
	}

	// Clear livetail; state must change again.
	upsertSessionRow(ctx, t, fs, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 now,
		ExpiresAt:                 now.Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	})
	state4 := srv.SessionStateForTest(ctx, pid, sessID)
	if state3 == state4 {
		t.Error("session state did not change after livetail clear")
	}
}
