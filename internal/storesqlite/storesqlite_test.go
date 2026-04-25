package storesqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func openStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := storesqlite.Open(
		context.Background(),
		filepath.Join(dir, "meta.db"),
		nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, func() { _ = s.Close() }
}

func TestCompliance(t *testing.T) {
	storetest.Run(t, openStore)
}

func TestMigrationIdempotency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	// First open — migrations apply.
	s1, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	// Second open — must be a no-op.
	storetest.RunMigrationIdempotency(t, func(t *testing.T) store.Store {
		s2, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
		if err != nil {
			t.Fatalf("Open #2: %v", err)
		}
		return s2
	})
}

// TestMigration0005StateChangeGeneric seeds the pre-0005 (mail-typed)
// state_changes shape and verifies that the 0005 migration SQL converts
// every row into the (entity_kind, entity_id, parent_entity_id, op)
// shape per docs/architecture/05-sync-and-state.md §Forward-
// compatibility. Forward-only check: existing dev databases at version
// 4 migrate cleanly on next start.
func TestMigration0005StateChangeGeneric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	// Stand up just the pre-0005 state_changes table — enough to
	// exercise the migration SQL in isolation.
	if _, err := raw.Exec(`CREATE TABLE state_changes (
		  id             INTEGER PRIMARY KEY AUTOINCREMENT,
		  principal_id   INTEGER NOT NULL,
		  seq            INTEGER NOT NULL,
		  kind           INTEGER NOT NULL,
		  mailbox_id     INTEGER NOT NULL DEFAULT 0,
		  message_id     INTEGER NOT NULL DEFAULT 0,
		  message_uid    INTEGER NOT NULL DEFAULT 0,
		  produced_at_us INTEGER NOT NULL,
		  UNIQUE(principal_id, seq)
		) STRICT`); err != nil {
		t.Fatalf("seed pre-0005 schema: %v", err)
	}
	if _, err := raw.Exec(`CREATE INDEX idx_state_changes_principal_seq ON state_changes(principal_id, seq)`); err != nil {
		t.Fatalf("seed pre-0005 idx1: %v", err)
	}
	if _, err := raw.Exec(`CREATE INDEX idx_state_changes_global_id    ON state_changes(id)`); err != nil {
		t.Fatalf("seed pre-0005 idx2: %v", err)
	}

	// Old kind values: 1=MessageCreated, 2=MessageUpdated,
	// 3=MessageDestroyed, 4=MailboxCreated, 5=MailboxUpdated,
	// 6=MailboxDestroyed.
	type seed struct {
		seq, kind, mailboxID, messageID, messageUID int64
	}
	seeds := []seed{
		{seq: 1, kind: 4, mailboxID: 100},                                // MailboxCreated
		{seq: 2, kind: 1, mailboxID: 100, messageID: 200, messageUID: 1}, // MessageCreated
		{seq: 3, kind: 2, mailboxID: 100, messageID: 200, messageUID: 1}, // MessageUpdated
		{seq: 4, kind: 3, mailboxID: 100, messageID: 200, messageUID: 1}, // MessageDestroyed
		{seq: 5, kind: 5, mailboxID: 100},                                // MailboxUpdated
		{seq: 6, kind: 6, mailboxID: 100},                                // MailboxDestroyed
	}
	for _, s := range seeds {
		if _, err := raw.Exec(
			`INSERT INTO state_changes(principal_id, seq, kind, mailbox_id, message_id, message_uid, produced_at_us)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			42, s.seq, s.kind, s.mailboxID, s.messageID, s.messageUID, 1700000000000000,
		); err != nil {
			t.Fatalf("seed row seq=%d: %v", s.seq, err)
		}
	}

	// Apply 0005 directly. The embedded migration runner exposed via
	// Migration0005SQL keeps test and production in lockstep — any
	// future tweak to the .sql file flows through here automatically.
	if _, err := raw.Exec(storesqlite.Migration0005SQL); err != nil {
		t.Fatalf("apply 0005: %v", err)
	}

	type got struct {
		seq                          int64
		entityKind                   string
		entityID, parentEntityID, op int64
	}
	rows, err := raw.Query(`SELECT seq, entity_kind, entity_id, parent_entity_id, op
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

func TestRejectNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()

	// Forge a future migration version directly in the DB.
	injected, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if _, err := injected.Exec(`INSERT INTO schema_migrations(version, applied_at_us) VALUES (9999, 0)`); err != nil {
		t.Fatalf("forge: %v", err)
	}
	_ = injected.Close()

	if _, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal()); err == nil {
		t.Fatal("expected Open to reject newer schema, got nil")
	}
}
