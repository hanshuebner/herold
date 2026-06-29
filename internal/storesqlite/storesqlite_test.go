package storesqlite_test

import (
	"context"
	"path/filepath"
	"strings"
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
// shape per docs/design/server/architecture/05-sync-and-state.md §Forward-
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

// TestMigration0048IdentityVerificationColumns verifies that migration
// 0048 leaves the existing jmap_identities columns intact, adds the
// verification trio + verified_at_us in their nullable / NULL-default
// form, and that a row inserted before migration 0048 surfaces with
// VerifiedAtUs == 0 and the three token columns as nil (i.e. the
// pre-feature unverified sentinel).
func TestMigration0048IdentityVerificationColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	// Open through the production Open so every migration has been
	// applied (the test runs against the live migration set; we are
	// asserting the column shape rather than the migration SQL in
	// isolation).
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Probe the schema via PRAGMA table_info.
	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()
	rows, err := raw.Query(`PRAGMA table_info(jmap_identities)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	got := map[string]string{} // colname -> type
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = typ
	}
	for _, c := range []string{
		"verified_at_us",
		"verification_token_hash",
		"verification_code_hash",
		"verification_token_expires_at_us",
	} {
		if _, ok := got[c]; !ok {
			t.Fatalf("column %q missing from jmap_identities", c)
		}
	}

	// Insert an identity through the public surface and verify the
	// new fields backfill to the unverified-pre-feature sentinel.
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "mig0048@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "mig-iv",
		PrincipalID: p.ID,
		Email:       "mig0048@example.com",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	row, err := s.Meta().GetJMAPIdentity(ctx, "mig-iv")
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if row.VerifiedAtUs != 0 {
		t.Fatalf("new row VerifiedAtUs = %d, want 0 (pre-feature)", row.VerifiedAtUs)
	}
	if row.VerificationTokenHash != nil || row.VerificationCodeHash != nil ||
		row.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("new row carries spurious verification fields: %+v", row)
	}
}

// TestMigration0049ReceivedToDefaultsAndRoundTrip verifies that
// migration 0049 adds message_mailboxes.received_to TEXT NOT NULL with
// an empty-string default (REQ-FLOW-33), that existing-pattern inserts
// (no received_to in the caller-side VALUES) backfill to the empty
// sentinel, and that a manual UPDATE setting a real RCPT TO survives
// a subsequent Read through the typed store API. The full caller-side
// wiring lands in task #17; this test exercises the schema column only.
func TestMigration0049ReceivedToDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// PRAGMA-level schema check via OpenRaw (a separate sql.DB handle
	// against the same file). The migration has already been applied
	// by the production Open above.
	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()
	rows, err := raw.Query(`PRAGMA table_info(message_mailboxes)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	var sawReceivedTo bool
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name != "received_to" {
			continue
		}
		sawReceivedTo = true
		if typ != "TEXT" {
			t.Fatalf("received_to type = %q, want TEXT", typ)
		}
		if notNull != 1 {
			t.Fatalf("received_to NOT NULL = %d, want 1", notNull)
		}
	}
	if !sawReceivedTo {
		t.Fatal("received_to column missing from message_mailboxes")
	}

	// InsertMessage with the existing call-site shape (no
	// ReceivedTo). The store-side default '' must apply.
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "mig0049@example.com",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	ref, err := s.Blobs().Put(context.Background(), strings.NewReader("Subject: hi\r\n\r\nbody"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         ref,
		Size:         ref.Size,
		InternalDate: time.Unix(1000, 0).UTC(),
		ReceivedAt:   time.Unix(1000, 0).UTC(),
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// The fan-out row must exist with received_to='' (backfill / DEFAULT).
	var msgID, mboxID int64
	var receivedTo string
	if err := raw.QueryRow(
		`SELECT message_id, mailbox_id, received_to
		   FROM message_mailboxes
		  LIMIT 1`).Scan(&msgID, &mboxID, &receivedTo); err != nil {
		t.Fatalf("post-insert read: %v", err)
	}
	if receivedTo != "" {
		t.Fatalf("new row received_to = %q, want '' (pre-feature sentinel)", receivedTo)
	}

	// Manual UPDATE setting a real RCPT TO; this simulates what task
	// #17 will do at the caller-side wiring step. NB: the production
	// Open holds a separate sql.DB pool on the same file. SQLite's
	// busy_timeout PRAGMA on the production pool absorbs the write
	// contention here even though raw is a second connection.
	if _, err := raw.Exec(
		`UPDATE message_mailboxes
		    SET received_to = ?
		  WHERE message_id = ? AND mailbox_id = ?`,
		"alice+filter@example.com", msgID, mboxID); err != nil {
		t.Fatalf("manual UPDATE: %v", err)
	}

	// The value must round-trip through the public read path.
	msg, err := s.Meta().GetMessage(ctx, store.MessageID(msgID))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(msg.Mailboxes) != 1 {
		t.Fatalf("Mailboxes len = %d, want 1", len(msg.Mailboxes))
	}
	if got := msg.Mailboxes[0].ReceivedTo; got != "alice+filter@example.com" {
		t.Fatalf("ReceivedTo round-trip: got %q, want %q", got, "alice+filter@example.com")
	}
}

// TestMigration0050TaggedAddressFiltersTable verifies that migration
// 0050 creates the tagged_address_filters table with the expected
// columns, primary key, unique constraint, CHECK on action, and FK
// cascade behaviour from jmap_identities.
func TestMigration0050TaggedAddressFiltersTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	rows, err := raw.Query(`PRAGMA table_info(tagged_address_filters)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	cols := map[string]struct {
		typ     string
		notNull int
		pk      int
	}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = struct {
			typ     string
			notNull int
			pk      int
		}{typ, notNull, pk}
	}
	for _, want := range []struct {
		name, typ string
		pk        bool
	}{
		{"id", "TEXT", true},
		{"principal_id", "INTEGER", false},
		{"base_identity_id", "TEXT", false},
		{"suffix", "TEXT", false},
		{"action", "TEXT", false},
		{"label_name", "TEXT", false},
		{"created_at_us", "INTEGER", false},
		{"updated_at_us", "INTEGER", false},
	} {
		c, ok := cols[want.name]
		if !ok {
			t.Fatalf("missing column %q", want.name)
		}
		if c.typ != want.typ {
			t.Fatalf("column %q type = %q, want %q", want.name, c.typ, want.typ)
		}
		if want.pk && c.pk == 0 {
			t.Fatalf("column %q should be primary key", want.name)
		}
	}

	// CHECK on action must reject unknown values.
	_, err = raw.Exec(`INSERT INTO tagged_address_filters
	  (id, principal_id, base_identity_id, suffix, action, label_name,
	   created_at_us, updated_at_us)
	  VALUES ('x', 1, 'ident', 'sfx', 'delete_forever', 'L', 1, 1)`)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject 'delete_forever' action, got nil error")
	}

	// End-to-end FK cascade: insert a principal + identity, then a
	// filter row, then delete the identity and confirm the filter
	// vanishes.
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "mig0050@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "mig0050-id",
		PrincipalID: p.ID,
		Email:       "mig0050@example.com",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: "mig0050-id",
		Suffix:         "ham",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Ham",
	}); err != nil {
		t.Fatalf("InsertTaggedAddressFilter: %v", err)
	}
	if err := s.Meta().DeleteJMAPIdentity(ctx, "mig0050-id"); err != nil {
		t.Fatalf("DeleteJMAPIdentity: %v", err)
	}
	got, err := s.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("filter survived Identity delete: %+v", got)
	}
}

// TestMigration0051TaggedAddressDismissalsTable verifies that migration
// 0051 creates the tagged_address_dismissals table with the expected
// composite primary key and FK cascade behaviour.
func TestMigration0051TaggedAddressDismissalsTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := storesqlite.Open(context.Background(), path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	rows, err := raw.Query(`PRAGMA table_info(tagged_address_dismissals)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	gotCols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotCols[name] = true
	}
	for _, c := range []string{"principal_id", "base_identity_id", "suffix", "dismissed_at_us"} {
		if !gotCols[c] {
			t.Fatalf("missing column %q", c)
		}
	}

	// Composite PK rejects duplicate (principal, identity, suffix).
	ctx := context.Background()
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "mig0051@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          "mig0051-id",
		PrincipalID: p.ID,
		Email:       "mig0051@example.com",
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: "mig0051-id",
		Suffix:         "shopping",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Raw second insert under the SAME tuple must fail the PK
	// constraint at the engine boundary; the store-layer wrapper
	// makes the user-facing call idempotent, so we go around it.
	_, err = raw.Exec(`INSERT INTO tagged_address_dismissals
	  (principal_id, base_identity_id, suffix, dismissed_at_us)
	  VALUES (?, 'mig0051-id', 'shopping', 1)`, int64(p.ID))
	if err == nil {
		t.Fatal("expected composite PK to reject duplicate, got nil error")
	}

	// FK cascade: drop the Identity, the dismissal must follow.
	if err := s.Meta().DeleteJMAPIdentity(ctx, "mig0051-id"); err != nil {
		t.Fatalf("DeleteJMAPIdentity: %v", err)
	}
	got, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dismissal survived Identity delete: %+v", got)
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

// TestMigration0070RethreadSameMsgid seeds a minimal messages table with
// a fragmented thread (two rows sharing the same env_message_id but with
// different effective thread_ids) and verifies that migration 0070 converges
// them. (re #88, REQ-STORE-40)
func TestMigration0070RethreadSameMsgid(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/meta.db"

	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer raw.Close()

	// Minimal messages table: only the columns the migration references.
	if _, err := raw.Exec(`CREATE TABLE messages (
		  id             INTEGER PRIMARY KEY AUTOINCREMENT,
		  principal_id   INTEGER NOT NULL,
		  env_message_id TEXT    NOT NULL DEFAULT '',
		  thread_id      INTEGER NOT NULL DEFAULT 0
		) STRICT`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Seed a fragmented thread: pid=1, root message-id "root@test".
	// Row 1: first copy, thread_id=0 (effective = 1).
	// Row 2: second copy, thread_id=0 (effective = 2) — fragmented.
	// Row 3: reply, thread_id=1 (correct — already in the root thread).
	// Row 4: unrelated message with different message-id.
	seeds := []struct {
		pid, threadID int64
		msgID         string
	}{
		{pid: 1, msgID: "root@test", threadID: 0},
		{pid: 1, msgID: "root@test", threadID: 0},
		{pid: 1, msgID: "reply@test", threadID: 1},
		{pid: 1, msgID: "other@test", threadID: 0},
	}
	for _, s := range seeds {
		if _, err := raw.Exec(
			`INSERT INTO messages(principal_id, env_message_id, thread_id) VALUES (?, ?, ?)`,
			s.pid, s.msgID, s.threadID,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Apply migration 0070.
	if _, err := raw.Exec(storesqlite.Migration0070SQL); err != nil {
		t.Fatalf("apply migration 0070: %v", err)
	}

	// Collect results.
	rows, err := raw.Query(`SELECT id, env_message_id, thread_id FROM messages ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	type row struct {
		id, threadID int64
		msgID        string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.msgID, &r.threadID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	effThread := func(r row) int64 {
		if r.threadID == 0 {
			return r.id
		}
		return r.threadID
	}

	// Row 1 (first root copy): effective thread = 1. thread_id may stay 0 or be set to 1.
	if effThread(got[0]) != 1 {
		t.Errorf("row 1 effective thread = %d, want 1", effThread(got[0]))
	}
	// Row 2 (second root copy): must share effective thread = 1.
	if effThread(got[1]) != 1 {
		t.Errorf("row 2 effective thread = %d, want 1 (must converge with row 1)", effThread(got[1]))
	}
	// Row 3 (reply, already thread_id=1): must remain in effective thread 1.
	if effThread(got[2]) != 1 {
		t.Errorf("row 3 effective thread = %d, want 1", effThread(got[2]))
	}
	// Row 4 (unrelated, single copy): must be its own singleton thread.
	if effThread(got[3]) != got[3].id {
		t.Errorf("row 4 effective thread = %d, want self id %d", effThread(got[3]), got[3].id)
	}
}
