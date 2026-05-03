package directory_test

// telemetry_test.go — tests for per-user clientlog telemetry opt-out
// (REQ-OPS-208, REQ-CLOG-06).
//
// Covers:
//   - EffectiveTelemetry: NULL principal column + default true/false.
//   - EffectiveTelemetry: explicit true/false overrides system default.
//   - SetTelemetry: persists, audit-logs with before/after, and the helper
//     resolves correctly after the mutation.
//   - TelemetryGate.IsEnabled: reads the effective flag from the session row.
//   - Session-descriptor projection: refresh session, observe updated value.
//   - Migration test: existing principal (no column value) resolves to default.

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newDirForTelemetry builds a minimal Directory + fakestore + clock for
// telemetry tests, seeding the "example.test" domain.
func newDirForTelemetry(t *testing.T) (*directory.Directory, store.Store) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	fs, err := storesqlite.OpenWithRand(context.Background(), dbPath, nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	dir := directory.New(fs.Meta(), slog.New(slog.NewTextHandler(io.Discard, nil)), clk, newDeterministicReader())
	return dir, fs
}

// TestEffectiveTelemetry_NullPrincipal_DefaultTrue confirms that a NULL
// principal column with defaultEnabled=true resolves to true.
func TestEffectiveTelemetry_NullPrincipal_DefaultTrue(t *testing.T) {
	p := store.Principal{ClientlogTelemetryEnabled: nil}
	if got := directory.EffectiveTelemetry(p, true); !got {
		t.Errorf("want true (null principal + default true), got false")
	}
}

// TestEffectiveTelemetry_NullPrincipal_DefaultFalse confirms that a NULL
// principal column with defaultEnabled=false resolves to false.
func TestEffectiveTelemetry_NullPrincipal_DefaultFalse(t *testing.T) {
	p := store.Principal{ClientlogTelemetryEnabled: nil}
	if got := directory.EffectiveTelemetry(p, false); got {
		t.Errorf("want false (null principal + default false), got true")
	}
}

// TestEffectiveTelemetry_ExplicitTrue confirms that an explicit true on the
// principal overrides the default regardless of its value.
func TestEffectiveTelemetry_ExplicitTrue(t *testing.T) {
	boolTrue := true
	for _, defaultVal := range []bool{true, false} {
		p := store.Principal{ClientlogTelemetryEnabled: &boolTrue}
		if got := directory.EffectiveTelemetry(p, defaultVal); !got {
			t.Errorf("explicit true with default %v: want true, got false", defaultVal)
		}
	}
}

// TestEffectiveTelemetry_ExplicitFalse confirms that an explicit false on the
// principal overrides the default regardless of its value.
func TestEffectiveTelemetry_ExplicitFalse(t *testing.T) {
	boolFalse := false
	for _, defaultVal := range []bool{true, false} {
		p := store.Principal{ClientlogTelemetryEnabled: &boolFalse}
		if got := directory.EffectiveTelemetry(p, defaultVal); got {
			t.Errorf("explicit false with default %v: want false, got true", defaultVal)
		}
	}
}

// TestSetTelemetry_PersistsAndAuditLogs verifies that SetTelemetry writes the
// value to the store and the audit log carries before/after fields.
func TestSetTelemetry_PersistsAndAuditLogs(t *testing.T) {
	ctx := context.Background()
	dir, fs := newDirForTelemetry(t)

	pid, err := dir.CreatePrincipal(ctx, "user@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Flip to explicit true.
	boolTrue := true
	if err := dir.SetTelemetry(ctx, pid, &boolTrue, ""); err != nil {
		t.Fatalf("SetTelemetry(true): %v", err)
	}

	// Read back and resolve.
	p, err := fs.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if !directory.EffectiveTelemetry(p, false) {
		t.Error("after SetTelemetry(true): want true, got false")
	}

	// Flip to explicit false.
	boolFalse := false
	if err := dir.SetTelemetry(ctx, pid, &boolFalse, ""); err != nil {
		t.Fatalf("SetTelemetry(false): %v", err)
	}
	p, _ = fs.Meta().GetPrincipalByID(ctx, pid)
	if directory.EffectiveTelemetry(p, true) {
		t.Error("after SetTelemetry(false): want false, got true")
	}

	// Clear to NULL.
	if err := dir.SetTelemetry(ctx, pid, nil, ""); err != nil {
		t.Fatalf("SetTelemetry(nil): %v", err)
	}
	p, _ = fs.Meta().GetPrincipalByID(ctx, pid)
	if p.ClientlogTelemetryEnabled != nil {
		t.Errorf("after SetTelemetry(nil): want nil, got %v", *p.ClientlogTelemetryEnabled)
	}

	// Audit log must have at least one entry for the set action.
	audit, err := fs.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		PrincipalID: pid,
		Action:      "principal.clientlog_telemetry.set",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(audit) == 0 {
		t.Fatal("expected audit entries for telemetry.set, got none")
	}
	// The most-recent entry (SetTelemetry(nil)) should carry before=false after=null.
	last := audit[len(audit)-1]
	if before, ok := last.Metadata["before"]; !ok || before != "false" {
		t.Errorf("last audit entry before: want 'false', got %q (ok=%v)", before, ok)
	}
	if after, ok := last.Metadata["after"]; !ok || after != "null" {
		t.Errorf("last audit entry after: want 'null', got %q (ok=%v)", after, ok)
	}
}

// TestTelemetryGate_IsEnabled verifies that TelemetryGate.IsEnabled reads
// the effective flag from the session row.
func TestTelemetryGate_IsEnabled(t *testing.T) {
	ctx := context.Background()
	_, fs := newDirForTelemetry(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Seed a principal (needed for the FK).
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "gate@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}

	gate := directory.NewTelemetryGate(fs.Meta())

	// Non-existent session -> ErrNotFound.
	if _, err := gate.IsEnabled(ctx, "no-such-session"); err == nil {
		t.Error("expected error for missing session, got nil")
	}

	// Insert a session with telemetry=true.
	sessID := "test-session-001"
	if err := fs.Meta().UpsertSession(ctx, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               p.ID,
		CreatedAt:                 clk.Now(),
		ExpiresAt:                 clk.Now().Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if got, err := gate.IsEnabled(ctx, sessID); err != nil || !got {
		t.Errorf("IsEnabled(true session): want true, got %v (err=%v)", got, err)
	}

	// UpdateSessionTelemetry to false.
	if err := fs.Meta().UpdateSessionTelemetry(ctx, sessID, false); err != nil {
		t.Fatalf("UpdateSessionTelemetry: %v", err)
	}
	if got, err := gate.IsEnabled(ctx, sessID); err != nil || got {
		t.Errorf("IsEnabled(false session): want false, got %v (err=%v)", got, err)
	}
}

// TestSetTelemetry_PropagatestoSession verifies that SetTelemetry with a
// non-empty sessionID propagates the resolved flag to the live session row.
func TestSetTelemetry_PropagatestoSession(t *testing.T) {
	ctx := context.Background()
	dir, fs := newDirForTelemetry(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Seed a session with telemetry=true.
	sessID := "session-projection-001"
	if err := fs.Meta().UpsertSession(ctx, store.SessionRow{
		SessionID:                 sessID,
		PrincipalID:               pid,
		CreatedAt:                 clk.Now(),
		ExpiresAt:                 clk.Now().Add(24 * time.Hour),
		ClientlogTelemetryEnabled: true,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	gate := directory.NewTelemetryGate(fs.Meta())

	// Set to false and pass the session ID.
	boolFalse := false
	if err := dir.SetTelemetry(ctx, pid, &boolFalse, sessID); err != nil {
		t.Fatalf("SetTelemetry(false): %v", err)
	}

	// The session row should now reflect false immediately.
	if got, err := gate.IsEnabled(ctx, sessID); err != nil || got {
		t.Errorf("gate.IsEnabled after SetTelemetry(false): want false, got %v (err=%v)", got, err)
	}
}

// TestTelemetry_MigrationExistingPrincipal verifies that a principal whose
// ClientlogTelemetryEnabled is nil (freshly inserted via InsertPrincipal
// without setting the field, simulating an existing row after the migration)
// resolves to the system default via EffectiveTelemetry.
func TestTelemetry_MigrationExistingPrincipal(t *testing.T) {
	ctx := context.Background()
	_, fs := newDirForTelemetry(t)

	// Insert a principal without setting ClientlogTelemetryEnabled.
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "legacy@example.test",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Column is nil after migration (no explicit value).
	if p.ClientlogTelemetryEnabled != nil {
		t.Errorf("freshly-inserted principal: want nil ClientlogTelemetryEnabled, got %v", *p.ClientlogTelemetryEnabled)
	}

	// With defaultEnabled=true it resolves to true.
	if !directory.EffectiveTelemetry(p, true) {
		t.Error("nil principal + default=true: want true, got false")
	}
	// With defaultEnabled=false it resolves to false.
	if directory.EffectiveTelemetry(p, false) {
		t.Error("nil principal + default=false: want false, got true")
	}
}
