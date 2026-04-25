package storesqlite

import (
	"database/sql"
)

// OpenRaw opens the given path using the same DSN production Open uses,
// without applying migrations. Exposed for tests that need to inject
// state into the schema (e.g. a forged future migration record).
func OpenRaw(path string) (*sql.DB, error) {
	return sql.Open("sqlite", buildDSN(path))
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
