package storesqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
)

// TestPragmaKnobs opens a SQLite store with non-default CacheSize and
// WALAutocheckpoint, then queries the live PRAGMA values through the
// store's own connection pool to confirm they were applied.
// Operator pragma knobs — operator hygiene feature, no REQ ID.
func TestPragmaKnobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	const wantCache = -32768 // half the default (-65536)
	const wantWALACP = 500   // half the SQLite default (1000 pages)

	raw, err := OpenWithOptions(context.Background(), path, nil, clock.NewReal(),
		Options{
			CacheSize:         wantCache,
			WALAutocheckpoint: wantWALACP,
		}, nil)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	defer raw.Close()

	// The Store interface does not expose the DB; use the white-box
	// helper added to export_test.go to query PRAGMAs on the live pool.
	st := raw.(*Store)
	db := st.DB()

	var gotCache int
	if err := db.QueryRow("PRAGMA cache_size").Scan(&gotCache); err != nil {
		t.Fatalf("query cache_size: %v", err)
	}
	if gotCache != wantCache {
		t.Errorf("cache_size: got %d, want %d", gotCache, wantCache)
	}

	var gotWAL int
	if err := db.QueryRow("PRAGMA wal_autocheckpoint").Scan(&gotWAL); err != nil {
		t.Fatalf("query wal_autocheckpoint: %v", err)
	}
	if gotWAL != wantWALACP {
		t.Errorf("wal_autocheckpoint: got %d, want %d", gotWAL, wantWALACP)
	}
}

// TestPragmaKnobs_DefaultsPreserved verifies that a zero Options (Open path)
// applies the built-in -65536 cache_size default.
func TestPragmaKnobs_DefaultsPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	raw, err := Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer raw.Close()

	st := raw.(*Store)
	db := st.DB()

	var gotCache int
	if err := db.QueryRow("PRAGMA cache_size").Scan(&gotCache); err != nil {
		t.Fatalf("query cache_size: %v", err)
	}
	if gotCache != -65536 {
		t.Errorf("default cache_size: got %d, want -65536", gotCache)
	}
}
