package storesqlite

import (
	"context"
	"database/sql"
	"time"
)

// OpenRaw opens the given path using the same DSN production Open uses,
// without applying migrations. Exposed for tests that need to inject
// state into the schema (e.g. a forged future migration record).
func OpenRaw(path string) (*sql.DB, error) {
	return sql.Open("sqlite", buildDSN(path))
}

// DB exposes the underlying *sql.DB of a Store for white-box tests that
// need to query PRAGMAs or inspect schema state without going through the
// store.Store interface.
func (s *Store) DB() *sql.DB { return s.db }

// ClientlogCursorForTime returns the cursor for the row with the largest id
// whose server_ts is <= ts. Test helper for clientlog pagination tests.
func ClientlogCursorForTime(_ context.Context, ts time.Time) string {
	return encodeClientLogCursor("", usMicros(ts))
}

// Migration0005SQL is the verbatim 0005_state_change_generic.sql body,
// re-exported for the migration mapping test in storesqlite_test.go so
// that the test exercises the production migration text and never
// drifts from it.
var Migration0005SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0005_state_change_generic.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()
