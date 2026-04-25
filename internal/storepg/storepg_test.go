package storepg_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
// shape per docs/architecture/05-sync-and-state.md §Forward-
// compatibility. Forward-only check: existing dev databases at version
// 4 migrate cleanly on next start.
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

	// Tear down any prior shape from a previous run.
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS state_changes`); err != nil {
		t.Fatalf("drop pre-existing state_changes: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS state_changes`)
	})

	if _, err := pool.Exec(ctx, `CREATE TABLE state_changes (
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
		if _, err := pool.Exec(ctx,
			`INSERT INTO state_changes(principal_id, seq, kind, mailbox_id, message_id, message_uid, produced_at_us)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			42, s.seq, s.kind, s.mailboxID, s.messageID, s.messageUID, 1700000000000000,
		); err != nil {
			t.Fatalf("seed row seq=%d: %v", s.seq, err)
		}
	}

	if _, err := pool.Exec(ctx, storepg.Migration0005SQL); err != nil {
		t.Fatalf("apply 0005: %v", err)
	}

	type got struct {
		seq                          int64
		entityKind                   string
		entityID, parentEntityID, op int64
	}
	rows, err := pool.Query(ctx, `SELECT seq, entity_kind, entity_id, parent_entity_id, op
		  FROM state_changes ORDER BY seq ASC`)
	if err != nil {
		t.Fatalf("read post-migration rows: %v", err)
	}
	defer rows.Close()
	var migrated []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.seq, &g.entityKind, &g.entityID, &g.parentEntityID, &g.op); err != nil {
			t.Fatalf("scan: %v", err)
		}
		migrated = append(migrated, g)
	}
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
