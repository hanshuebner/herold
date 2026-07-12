package storepg

import (
	"context"
	"strings"
)

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
	// Enumerating the managed tables keeps the test predictable when the
	// schema grows. Order is loose because the TRUNCATE below uses CASCADE;
	// children are listed before parents only as a readability convention.
	tables := []string{
		// Attachment shares (per-principal; FK to principals). Cascades
		// from principals, but enumerated per the convention above.
		"file_shares",
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
		// SRS (Sender Rewriting Scheme) signing secrets (issue #204, no FK).
		"srs_secrets",
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
		// REQ-ADM-304 system-events ring buffer (migration 0073). Same shape
		// as clientlog: no FK, so CASCADE never reaches it; must be enumerated
		// explicitly or rows leak across subtests and break
		// SystemEvents_AppendList / EvictByCap / DomainFilter / ActionFilter /
		// CursorPagination in storetest.
		"system_events",
		// Per-blob MIME part index (migration 0061). Keyed by blob_hash with
		// no FK, so CASCADE from messages never reaches it; like clientlog it
		// must be enumerated explicitly or rows leak across subtests and break
		// BlobPartIndex/ListNeedingPartIndex_MinVersionFilter in storetest.
		"blob_part_index",
		// Mail core: messages, mailbox ACL, mailboxes, blob refs.
		"messages",
		"mailbox_acl",
		"mailboxes",
		"blob_refs",
		// Identity surfaces.
		"aliases",
		// OAuth2 native-client grant (issue #199, migration 0082). FK to
		// principals and api_keys; enumerated before both parents.
		"oauth_refresh_tokens",
		"oauth_auth_codes",
		"api_keys",
		"oidc_links",
		"oidc_providers",
		"principals",
		"domains",
	}
	// Reset row state between tests by truncating every managed table in
	// one statement, which acquires all the ACCESS EXCLUSIVE locks together.
	//
	// The hazard this guards against: pgxpool.Pool.Close() sends Terminate
	// to each pooled connection and returns once the client sockets are
	// closed, but the Postgres server reaps those backends asynchronously.
	// On a loaded host a backend from the *previous* test's pool can still
	// be alive for a moment afterwards, holding an AccessShareLock from its
	// last query. The TRUNCATE then waits for ACCESS EXCLUSIVE behind it.
	// Left unbounded, that wait runs all the way to the package test
	// timeout (observed as the 5-minute storepg hang in CI runs 112/115/116).
	//
	// Everything runs on a single acquired connection so the session-level
	// lock_timeout actually applies to the TRUNCATE that follows on the
	// same backend:
	//   - lock_timeout bounds every TRUNCATE attempt, so a straggler can
	//     never turn into a multi-minute hang -- worst case is a prompt,
	//     legible 55P03 error.
	//   - pg_terminate_backend(pid, timeout) (the two-argument form) BLOCKS
	//     until the signalled backend has actually exited, unlike the
	//     one-argument form which returns the instant SIGTERM is sent.
	//     Terminating stragglers up front clears their locks before we ask
	//     for ACCESS EXCLUSIVE; the bounded retry re-clears any that raced
	//     in, then surfaces the error rather than spinning forever.
	const truncateAttempts = 3
	truncate := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET lock_timeout = '20s'"); err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < truncateAttempts; attempt++ {
		// Wait (up to 5s each) for any other backend in this database to
		// terminate before attempting the TRUNCATE. Errors are ignored:
		// an empty result set when nothing needs terminating is not an
		// error, and a transient failure here is retried below anyway.
		_, _ = conn.Exec(ctx, `
			SELECT pg_terminate_backend(pid, 5000)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
		`)

		if _, err := conn.Exec(ctx, truncate); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}
