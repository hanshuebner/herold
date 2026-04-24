package storepg

import "context"

// TruncateAll wipes state between compliance test runs against a
// single shared database. Application tables are truncated; the
// schema_migrations table is left alone so the migration idempotency
// tests can observe its persistence across open/close cycles. Exposed
// only to tests that import this package under a _test.go file.
func (s *Store) TruncateAll(ctx context.Context) error {
	tables := []string{
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
