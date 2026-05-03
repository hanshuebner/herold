package storepg

import "context"

// TruncateAll wipes every application table while preserving the
// schema_migrations row so subsequent Open calls see a fully-applied
// schema and not a fresh-install state.
//
// This is a TEST-ONLY helper — it is in a regular (non-_test) build
// file because external test packages (test/e2e/fixtures) need to call
// it to reset a shared destructive Postgres database between sub-tests.
// It is intentionally not on the store.Store interface, so production
// code cannot reach it without an explicit `*storepg.Store` import +
// type assertion. Any caller is signing in big letters that they own
// the database and want it wiped.
//
// Tables are enumerated explicitly (not discovered via
// information_schema) so the list of tables truncated stays in lockstep
// with the diag/backup TableNames manifest. Every new migration that
// adds a table MUST add it here too; the corresponding gate is the
// schema-version invariant test in internal/diag/backup, which fires
// on the manifest side.
func (s *Store) TruncateAll(ctx context.Context) error {
	// Order is loose because every TRUNCATE has CASCADE, but enumerating
	// keeps the test predictable when the schema grows. Children
	// listed before parents only as a readability convention.
	tables := []string{
		// Phase 3 Wave 3.10 — coach (per-principal; FK to principals).
		"coach_dismiss",
		"coach_events",
		// Phase 3 Wave 3.9 — email reactions (FK to messages).
		"email_reactions",
		// Phase 3 Wave 3.2 — SES inbound dedup (no FK).
		"ses_seen_messages",
		// Phase 3 Wave 3.8a — Web Push subscriptions.
		"push_subscription",
		// Phase 2 chat (Wave 2.8 + 2.9.6).
		"chat_blocks",
		"chat_messages",
		"chat_memberships",
		"chat_conversations",
		"chat_account_settings",
		// Phase 2 calendars / contacts.
		"calendar_events",
		"calendars",
		"contacts",
		"address_books",
		// Phase 1/2 mail submission and identity tables.
		"jmap_email_submissions",
		"jmap_identities",
		"tlsrpt_failures",
		"jmap_states",
		"jmap_categorisation_config",
		// Email-security report tables.
		"dmarc_rows",
		"dmarc_reports_raw",
		// Webhook subscriptions.
		"webhooks",
		// ACME state.
		"acme_certs",
		"acme_orders",
		"acme_accounts",
		// DKIM keys (per-domain, FK to domains).
		"dkim_keys",
		// Outbound queue.
		"queue",
		// Audit log + cursors.
		"audit_log",
		"cursors",
		// Sieve scripts (per-principal).
		"sieve_scripts",
		// State change feed.
		"state_changes",
		// REQ-OPS-206 client-log ring buffer (migration 0037). No FK
		// constraints, so it is NOT wiped by CASCADE from any other
		// truncate above; must be enumerated explicitly or rows leak
		// across subtests and break ClientLog_Pagination / EvictByAge /
		// EvictByCap / EvictDoesNotCrossSlice in storetest.
		"clientlog",
		// Mail core: messages, mailbox ACL, mailboxes, blob refs.
		"messages",
		"mailbox_acl",
		"mailboxes",
		"blob_refs",
		// Identity surfaces.
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
