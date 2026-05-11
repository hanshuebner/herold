package storesqlite_test

// sweeper_compliance_test.go runs the identityverify sweeper test
// matrix against the sqlite backend. The matching postgres entry is
// internal/storepg/sweeper_compliance_test.go; both share the helper
// in internal/identityverify/sweeper_compliance.go.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/identityverify"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func TestSweeperCompliance(t *testing.T) {
	identityverify.RunSweeperCompliance(t, func(t *testing.T) (store.Store, *clock.FakeClock, func()) {
		t.Helper()
		clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
		dir := t.TempDir()
		s, err := storesqlite.Open(context.Background(),
			filepath.Join(dir, "meta.db"), nil, clk)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s, clk, func() { _ = s.Close() }
	})
}
