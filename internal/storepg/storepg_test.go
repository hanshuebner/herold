package storepg_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest"
	"github.com/hanshuebner/herold/internal/storepg"
)

// getDSN returns a Postgres DSN for running integration tests, and a
// bool indicating whether tests should run. The CI matrix job sets
// HEROLD_PG_DSN (preferred). HEROLD_PG_TESTS=1 with testcontainers-go
// on the host is the alternate path; we keep the gate simple (DSN
// only) to avoid dragging docker in by default and skip cleanly when
// neither is set.
func getDSN(t *testing.T) (string, bool) {
	t.Helper()
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		return dsn, true
	}
	if testing.Short() {
		return "", false
	}
	return "", false
}

func openStore(t *testing.T, dsn string) (store.Store, func()) {
	t.Helper()
	// Use a unique database-schema prefix per test by scoping via a
	// dedicated database if the DSN's host is controllable; otherwise
	// reset per-test state by wiping known tables in a rollback-only
	// tx. Simplest portable approach: create a temporary schema and
	// set search_path on the pgxpool via the DSN's search_path option.
	// For Wave 1 we accept a destructive test DB: callers MUST point
	// HEROLD_PG_DSN at a throwaway database. We DROP+CREATE the public
	// schema tables (only the ones we manage) before each test by
	// issuing a TRUNCATE ... RESTART IDENTITY CASCADE.
	blobDir := t.TempDir()
	s, err := storepg.Open(
		context.Background(),
		dsn,
		filepath.Join(blobDir, "blobs"),
		nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := truncateTables(s); err != nil {
		_ = s.Close()
		t.Fatalf("truncate: %v", err)
	}
	return s, func() { _ = s.Close() }
}

// truncateTables wipes row state between tests while leaving the
// schema (and therefore the schema_migrations table) intact. Only
// known tables are touched.
func truncateTables(s store.Store) error {
	// We need raw access to exec a TRUNCATE; storepg does not expose
	// its pool publicly, so we issue a no-op writing method via the
	// Metadata surface. For the harness we instead use an exposed
	// TruncateAll test helper.
	tr, ok := s.(interface {
		TruncateAll(ctx context.Context) error
	})
	if !ok {
		return nil
	}
	return tr.TruncateAll(context.Background())
}

func TestCompliance(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres integration tests (set HEROLD_PG_DSN or pass -run with a provided DB)")
	}
	if !strings.Contains(dsn, "postgres") {
		t.Skipf("HEROLD_PG_DSN=%q does not look like a Postgres DSN", dsn)
	}
	storetest.Run(t, func(t *testing.T) (store.Store, func()) {
		return openStore(t, dsn)
	})
}

func TestMigrationIdempotency(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set")
	}
	// First open runs migrations; close; second open is a no-op.
	s1, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	_ = s1.Close()
	storetest.RunMigrationIdempotency(t, func(t *testing.T) store.Store {
		s, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
		if err != nil {
			t.Fatalf("Open #2: %v", err)
		}
		return s
	})
}

// TestMigration0005StateChangeGeneric seeds the pre-0005 (mail-typed)
// state_changes shape and verifies that the 0005 migration SQL converts
// every row into the (entity_kind, entity_id, parent_entity_id, op)
// shape per docs/design/server/architecture/05-sync-and-state.md §Forward-
// compatibility. Forward-only check: existing dev databases at version
// 4 migrate cleanly on next start.
//
// The whole test runs inside a single transaction that is ROLLBACK'd at
// the end. Postgres allows DDL inside transactions and the 0005 SQL is
// composed entirely of ALTER TABLE / UPDATE / CREATE INDEX (without
// CONCURRENTLY) statements, so this is safe. The rollback restores the
// real state_changes table (with its full set of post-migration columns
// such as `cause` from 0044) so subsequent tests in this package — which
// share the CI postgres service container — see an unmodified schema.
func TestMigration0005StateChangeGeneric(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	// Rollback always: even on success, we discard so the production
	// state_changes shape (including columns added by later migrations
	// such as 0044's `cause`) is restored for whatever sibling test
	// runs next against the same shared CI database.
	defer func() { _ = tx.Rollback(ctx) }()

	// Drop the post-migration state_changes inside the tx so the
	// CREATE TABLE below can install the pre-0005 shape. The drop is
	// undone by the rollback.
	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS state_changes`); err != nil {
		t.Fatalf("drop pre-existing state_changes: %v", err)
	}

	if _, err := tx.Exec(ctx, `CREATE TABLE state_changes (
		  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		  principal_id   BIGINT  NOT NULL,
		  seq            BIGINT  NOT NULL,
		  kind           INTEGER NOT NULL,
		  mailbox_id     BIGINT  NOT NULL DEFAULT 0,
		  message_id     BIGINT  NOT NULL DEFAULT 0,
		  message_uid    BIGINT  NOT NULL DEFAULT 0,
		  produced_at_us BIGINT  NOT NULL,
		  UNIQUE(principal_id, seq)
		)`); err != nil {
		t.Fatalf("seed pre-0005 schema: %v", err)
	}

	// Old kind values: 1=MessageCreated, 2=MessageUpdated,
	// 3=MessageDestroyed, 4=MailboxCreated, 5=MailboxUpdated,
	// 6=MailboxDestroyed.
	type seed struct {
		seq, kind, mailboxID, messageID, messageUID int64
	}
	seeds := []seed{
		{seq: 1, kind: 4, mailboxID: 100},
		{seq: 2, kind: 1, mailboxID: 100, messageID: 200, messageUID: 1},
		{seq: 3, kind: 2, mailboxID: 100, messageID: 200, messageUID: 1},
		{seq: 4, kind: 3, mailboxID: 100, messageID: 200, messageUID: 1},
		{seq: 5, kind: 5, mailboxID: 100},
		{seq: 6, kind: 6, mailboxID: 100},
	}
	for _, s := range seeds {
		if _, err := tx.Exec(ctx,
			`INSERT INTO state_changes(principal_id, seq, kind, mailbox_id, message_id, message_uid, produced_at_us)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			42, s.seq, s.kind, s.mailboxID, s.messageID, s.messageUID, 1700000000000000,
		); err != nil {
			t.Fatalf("seed row seq=%d: %v", s.seq, err)
		}
	}

	if _, err := tx.Exec(ctx, storepg.Migration0005SQL); err != nil {
		t.Fatalf("apply 0005: %v", err)
	}

	type got struct {
		seq                          int64
		entityKind                   string
		entityID, parentEntityID, op int64
	}
	rows, err := tx.Query(ctx, `SELECT seq, entity_kind, entity_id, parent_entity_id, op
		  FROM state_changes ORDER BY seq ASC`)
	if err != nil {
		t.Fatalf("read post-migration rows: %v", err)
	}
	var migrated []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.seq, &g.entityKind, &g.entityID, &g.parentEntityID, &g.op); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		migrated = append(migrated, g)
	}
	rows.Close()
	want := []got{
		{seq: 1, entityKind: "mailbox", entityID: 100, parentEntityID: 0, op: 1},
		{seq: 2, entityKind: "email", entityID: 200, parentEntityID: 100, op: 1},
		{seq: 3, entityKind: "email", entityID: 200, parentEntityID: 100, op: 2},
		{seq: 4, entityKind: "email", entityID: 200, parentEntityID: 100, op: 3},
		{seq: 5, entityKind: "mailbox", entityID: 100, parentEntityID: 0, op: 2},
		{seq: 6, entityKind: "mailbox", entityID: 100, parentEntityID: 0, op: 3},
	}
	if len(migrated) != len(want) {
		t.Fatalf("row count = %d, want %d (rows = %#v)", len(migrated), len(want), migrated)
	}
	for i := range want {
		if migrated[i] != want[i] {
			t.Fatalf("row %d = %#v, want %#v", i, migrated[i], want[i])
		}
	}
}

// TestMaterializeDefaultIdentity_ConcurrentRace exercises the TOCTOU retry
// in storepg.MaterializeDefaultIdentity: two goroutines call the method
// simultaneously for the same principal and must (a) both return without
// error, (b) both return the same identity id, and (c) only one row is
// inserted into jmap_identities.
func TestMaterializeDefaultIdentity_ConcurrentRace(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres-specific concurrent test")
	}
	s, cleanup := openStore(t, dsn)
	defer cleanup()

	ctx := context.Background()

	// Insert a principal to test against.
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		CanonicalEmail: "concurrent-mat@example.com",
		DisplayName:    "Concurrent Mat",
		PasswordHash:   "x",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	// Launch two goroutines that call MaterializeDefaultIdentity simultaneously.
	const goroutines = 2
	results := make([]string, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	// Use a barrier so both goroutines hit the store method at the same time.
	var ready sync.WaitGroup
	ready.Add(goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			results[i], errs[i] = s.Meta().MaterializeDefaultIdentity(ctx, p.ID)
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if results[0] == "" || results[1] == "" {
		t.Fatalf("one or both goroutines returned empty id: %q %q", results[0], results[1])
	}
	if results[0] != results[1] {
		t.Errorf("goroutines returned different ids: %q vs %q", results[0], results[1])
	}

	// Verify exactly one row was inserted (may_delete=false for this principal).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM jmap_identities WHERE principal_id = $1 AND may_delete = false`,
		int64(p.ID)).Scan(&rowCount); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("jmap_identities default rows = %d; want exactly 1", rowCount)
	}
}

// TestMigration0070RethreadSameMsgid seeds a minimal messages table with
// a fragmented thread (two rows sharing the same env_message_id but with
// different effective thread_ids) and verifies that migration 0070 converges
// them. (re #88, REQ-STORE-40)
func TestMigration0070RethreadSameMsgid(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	// Run inside a transaction that is rolled back so we don't disturb
	// the schema for other tests sharing the CI database.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Drop and recreate a minimal messages table for this test.
	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS messages`); err != nil {
		t.Fatalf("drop messages: %v", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE messages (
		  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		  principal_id   BIGINT NOT NULL,
		  env_message_id TEXT   NOT NULL DEFAULT '',
		  thread_id      BIGINT NOT NULL DEFAULT 0
		)`); err != nil {
		t.Fatalf("create minimal messages: %v", err)
	}

	// Seed a fragmented thread:
	//   Row 1: first copy of root (env_message_id="root@test", thread_id=0)
	//   Row 2: second copy of root (same env_message_id, thread_id=0) — fragmented
	//   Row 3: unrelated singleton (different env_message_id, thread_id=0)
	// We do not pre-set thread_id to a non-zero value here because the Postgres
	// IDENTITY sequence may not start at 1 in a shared CI database. The migration
	// must converge the two "root@test" rows to the same effective thread regardless
	// of what their auto-generated ids are.
	for _, msgID := range []string{"root@test", "root@test", "other@test"} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO messages(principal_id, env_message_id) VALUES ($1, $2)`,
			1, msgID,
		); err != nil {
			t.Fatalf("seed %q: %v", msgID, err)
		}
	}

	// Query the auto-assigned ids before the migration.
	pgrows, err := tx.Query(ctx, `SELECT id, env_message_id, thread_id FROM messages ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("pre-migration query: %v", err)
	}
	type row struct {
		id, threadID int64
		msgID        string
	}
	var before []row
	for pgrows.Next() {
		var r row
		if err := pgrows.Scan(&r.id, &r.msgID, &r.threadID); err != nil {
			pgrows.Close()
			t.Fatalf("pre-migration scan: %v", err)
		}
		before = append(before, r)
	}
	pgrows.Close()
	if len(before) != 3 {
		t.Fatalf("expected 3 seed rows, got %d", len(before))
	}

	if _, err := tx.Exec(ctx, storepg.Migration0070SQL); err != nil {
		t.Fatalf("apply migration 0070: %v", err)
	}

	pgrows, err = tx.Query(ctx, `SELECT id, env_message_id, thread_id FROM messages ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("post-migration query: %v", err)
	}
	var got []row
	for pgrows.Next() {
		var r row
		if err := pgrows.Scan(&r.id, &r.msgID, &r.threadID); err != nil {
			pgrows.Close()
			t.Fatalf("post-migration scan: %v", err)
		}
		got = append(got, r)
	}
	pgrows.Close()
	if len(got) != 3 {
		t.Fatalf("expected 3 rows after migration, got %d", len(got))
	}

	effThread := func(r row) int64 {
		if r.threadID == 0 {
			return r.id
		}
		return r.threadID
	}

	// The anchor is the effective thread of the first root copy (lowest id with "root@test").
	anchor := before[0].id // first row auto-id; its effective thread = anchor

	// Row 0 (first root copy): effective thread must equal anchor.
	if effThread(got[0]) != anchor {
		t.Errorf("row[0] effective thread = %d, want %d", effThread(got[0]), anchor)
	}
	// Row 1 (second root copy): must converge to anchor.
	if effThread(got[1]) != anchor {
		t.Errorf("row[1] effective thread = %d, want %d (must converge with row[0])", effThread(got[1]), anchor)
	}
	// Row 2 (unrelated singleton, only one row with that message_id): must stay its own thread.
	if effThread(got[2]) != got[2].id {
		t.Errorf("row[2] effective thread = %d, want self id %d", effThread(got[2]), got[2].id)
	}
}
