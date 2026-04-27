package storepg

// Migration0005SQL is the verbatim 0005_state_change_generic.sql body,
// re-exported for the migration-mapping test in storepg_test.go so the
// test exercises the production migration text and never drifts from
// it.
var Migration0005SQL = func() string {
	body, err := migrationsFS.ReadFile("migrations/0005_state_change_generic.sql")
	if err != nil {
		panic(err)
	}
	return string(body)
}()

// TruncateAll moved to testseam.go (regular build) so external test
// packages (e.g. test/e2e/fixtures) can call it across package
// boundaries. _test.go files are not visible to importers.
