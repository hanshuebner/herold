package storepg

import "context"

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

// TruncateAll wipes state between compliance test runs against a
// single shared database. Application tables are truncated; the
// schema_migrations table is left alone so the migration idempotency
// tests can observe its persistence across open/close cycles. Exposed
// only to tests that import this package under a _test.go file.
func (s *Store) TruncateAll(ctx context.Context) error {
	// Phase 2 tables come first; their FKs reference principals /
	// mailboxes which we tear down at the end. CASCADE on TRUNCATE
	// would handle ordering but enumerating keeps the test predictable
	// when the schema grows.
	tables := []string{
		"tlsrpt_failures",
		"jmap_states",
		"mailbox_acl",
		"dmarc_rows",
		"dmarc_reports_raw",
		"webhooks",
		"acme_certs",
		"acme_orders",
		"acme_accounts",
		"dkim_keys",
		"queue",
		"audit_log",
		"cursors",
		"sieve_scripts",
		"state_changes",
		"messages",
		"mailboxes",
		"blob_refs",
		"aliases",
		"api_keys",
		"oidc_links",
		"oidc_providers",
		"principals",
		"domains",
	}
	for _, t := range tables {
		if _, err := s.pool.Exec(ctx, "TRUNCATE TABLE "+t+" RESTART IDENTITY CASCADE"); err != nil {
			return err
		}
	}
	return nil
}
