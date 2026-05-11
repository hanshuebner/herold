package storepg_test

// sweeper_compliance_test.go runs the identityverify sweeper test
// matrix against the Postgres backend. Skips when HEROLD_PG_DSN is
// unset (matches TestCompliance gating).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/identityverify"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

func TestSweeperCompliance(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres sweeper compliance")
	}
	if !strings.Contains(dsn, "postgres") {
		t.Skipf("HEROLD_PG_DSN=%q does not look like a Postgres DSN", dsn)
	}
	identityverify.RunSweeperCompliance(t, func(t *testing.T) (store.Store, *clock.FakeClock, func()) {
		t.Helper()
		clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
		dir := t.TempDir()
		s, err := storepg.Open(context.Background(), dsn,
			filepath.Join(dir, "blobs"), nil, clk)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// Truncate so the test starts from a clean slate (compliance
		// tests must be independent of any prior PG state).
		if tr, ok := s.(interface {
			TruncateAll(ctx context.Context) error
		}); ok {
			if err := tr.TruncateAll(context.Background()); err != nil {
				_ = s.Close()
				t.Fatalf("truncate: %v", err)
			}
		}
		return s, clk, func() { _ = s.Close() }
	})
}
