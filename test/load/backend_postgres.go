package load

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// openPostgresStore opens a Postgres-backed store for load tests.
// The DSN must point at a throwaway database; this function does not
// truncate tables automatically — the caller is responsible for database
// hygiene between runs.
func openPostgresStore(t testing.TB, dsn string) store.Store {
	t.Helper()
	blobDir := t.TempDir()
	s, err := storepg.Open(
		context.Background(),
		dsn,
		blobDir,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("storepg.Open: %v", err)
	}
	return s
}
