package storepg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hanshuebner/herold/internal/store"
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
		// REQ-STORE-40 late-ancestor thread-merge reverse index (issue
		// #485; FK to principals and messages).
		"message_references",
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
		// Mail core: messages, mailboxes, blob refs. Mailbox ACL rows now live
		// in "grants" (epic #210), cascaded via its granted_by FK to principals.
		"messages",
		"mailboxes",
		"blob_refs",
		// Identity surfaces.
		"aliases",
		// OAuth2 native-client grant (issue #199, migration 0082). FK to
		// principals and api_keys; enumerated before both parents.
		"oauth_refresh_tokens",
		"oauth_auth_codes",
		"api_keys",
		// OAuth2 native-client registry (issue #199, migration 0092). No FK
		// (client_id is referenced as free text by the tables above), so it
		// is NOT reached by CASCADE from any of them; must be enumerated
		// explicitly or rows leak across subtests.
		"oauth_clients",
		// Registered apps (issue #522, migration 0118). No FK to any
		// table above (app_principals cascades from apps), so apps must
		// be enumerated explicitly or rows leak across subtests.
		"app_principals",
		"apps",
		"oidc_links",
		"oidc_providers",
		"principals",
		"domains",
	}
	// Reset row state between tests with resetDirty (row-level); the
	// fallback truncates every managed table in one statement, which acquires all the ACCESS EXCLUSIVE locks together.
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
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET lock_timeout = '20s'"); err != nil {
		return err
	}

	truncate := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"

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

		// A full TRUNCATE ... CASCADE swaps the relfilenode of every table
		// reachable through a foreign key (nearly the whole schema, via
		// principals and domains) and flushes WAL for each, which dominates
		// the suite's wall time. Row-level reset touches only what a case
		// dirtied, so it runs first; the full TRUNCATE remains as the
		// fallback when the targeted reset cannot complete.
		if err := resetDirty(ctx, conn); err == nil {
			return nil
		}
		if _, err := conn.Exec(ctx, truncate); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// resetDirty deletes the rows of every application table that holds any and
// rewinds every identity/serial sequence that has handed out a value, all
// in one transaction. The table set is discovered from the catalog (every
// ordinary table in the current schema except schema_migrations) and
// deleted children-first following the foreign-key graph, so tables
// reached only through a foreign key are reset along with the ones
// enumerated in TruncateAll.
func resetDirty(ctx context.Context, conn *pgxpool.Conn) (err error) {
	// parents[child] lists the tables child references (self-references
	// excluded).
	parents := make(map[string][]string)
	rows, err := conn.Query(ctx, `
		SELECT c.relname::text, COALESCE(p.relname::text, '')
		  FROM pg_class c
		  LEFT JOIN pg_constraint k ON k.conrelid = c.oid AND k.contype = 'f'
		                           AND k.confrelid <> c.oid
		  LEFT JOIN pg_class p ON p.oid = k.confrelid
		 WHERE c.relkind = 'r'
		   AND c.relnamespace = current_schema()::regnamespace
		   AND c.relname <> 'schema_migrations'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			rows.Close()
			return err
		}
		parents[child] = append(parents[child], parent)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	names := make([]string, 0, len(parents))
	for n := range parents {
		names = append(names, n)
	}
	sort.Strings(names)

	// Order tables so every child precedes the tables it references:
	// post-order DFS over the parent edges yields parents first, so the
	// reverse is the delete order.
	var postOrder []string
	state := make(map[string]int)
	var visit func(string)
	visit = func(n string) {
		if state[n] != 0 {
			return
		}
		state[n] = 1
		for _, p := range parents[n] {
			if p != "" {
				visit(p)
			}
		}
		postOrder = append(postOrder, n)
	}
	for _, n := range names {
		visit(n)
	}

	var q strings.Builder
	q.WriteString("SELECT 't', t, '' FROM (")
	for i, t := range names {
		if i > 0 {
			q.WriteString(" UNION ALL ")
		}
		fmt.Fprintf(&q, "SELECT %s::text AS t WHERE EXISTS (SELECT 1 FROM %s)",
			quoteLiteral(t), pgx.Identifier{t}.Sanitize())
	}
	q.WriteString(`) x UNION ALL
		SELECT 's', '',
		       format('SELECT setval(%L, %s, false)', sq.oid::regclass::text, ps.start_value)
		  FROM pg_class c
		  JOIN pg_depend d  ON d.refobjid = c.oid AND d.deptype IN ('a', 'i')
		  JOIN pg_class sq  ON sq.oid = d.objid AND sq.relkind = 'S'
		  JOIN pg_sequences ps ON ps.sequencename = sq.relname
		                      AND ps.schemaname = current_schema()
		 WHERE c.relnamespace = current_schema()::regnamespace
		   AND c.relname <> 'schema_migrations'
		   AND ps.last_value IS NOT NULL`)
	rows, err = conn.Query(ctx, q.String())
	if err != nil {
		return err
	}
	dirty := make(map[string]bool)
	var seqStmts []string
	for rows.Next() {
		var kind, name, stmt string
		if err := rows.Scan(&kind, &name, &stmt); err != nil {
			rows.Close()
			return err
		}
		if kind == "t" {
			dirty[name] = true
		} else {
			seqStmts = append(seqStmts, stmt)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(dirty) == 0 && len(seqStmts) == 0 {
		return nil
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	for i := len(postOrder) - 1; i >= 0; i-- {
		t := postOrder[i]
		if !dirty[t] {
			continue
		}
		if _, err = tx.Exec(ctx, "DELETE FROM "+pgx.Identifier{t}.Sanitize()); err != nil {
			return err
		}
	}
	for _, stmt := range seqStmts {
		if _, err = tx.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ForceCredentialForTest writes passwordHash/totpSecret directly onto the
// principals row identified by id, bypassing UpdatePrincipal's
// REQ-SUBACCT-02 guard (sub-principal cannot carry a credential).
//
// This is a TEST-ONLY helper — it is in a regular (non-_test) build file
// because external test packages (internal/directory, internal/protologin,
// internal/sasl) need to construct a sub-principal row that carries a
// verifiably-correct credential in order to prove that the auth layer
// (Directory.Authenticate and everything built on it) rejects a
// sub-principal even when the row is credentialed -- a defense-in-depth
// property that is otherwise unreachable through any store API, since
// both InsertSubPrincipal and UpdatePrincipal refuse to persist a
// credential on a sub-principal. It is intentionally not on the
// store.Store interface, so production code cannot reach it without an
// explicit *storepg.Store import + type assertion.
func (s *Store) ForceCredentialForTest(ctx context.Context, id store.PrincipalID, passwordHash string, totpSecret []byte) error {
	now := usMicros(s.clock.Now().UTC())
	tag, err := s.pool.Exec(ctx, `
		UPDATE principals
		   SET password_hash = $1, totp_secret = $2, updated_at_us = $3
		 WHERE id = $4`,
		passwordHash, totpSecret, now, int64(id))
	if err != nil {
		return fmt.Errorf("storepg: ForceCredentialForTest: %w", mapErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("storepg: ForceCredentialForTest: %w", store.ErrNotFound)
	}
	return nil
}

// SetConsolidateSnoozeHook installs fn to run inside ConsolidateSnooze's
// transaction between the write that places the reminder on the
// canonical membership and the write that clears it on the previous
// holder. Test seam for asserting that a concurrent wake sweep, which
// sees only committed state, observes exactly one holder; nil removes
// the hook.
func (s *Store) SetConsolidateSnoozeHook(fn func(ctx context.Context)) {
	if fn == nil {
		s.consolidateHook.Store(nil)
		return
	}
	s.consolidateHook.Store(&fn)
}
