package storesqlite_test

import (
	"context"
	"path/filepath"
	"strconv"
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

// TestMigration0079GrantBackfill verifies that migration 0079 auto-maps
// existing authority into grant rows on upgrade (epic #182 acceptance:
// "migration auto-maps existing admins to server:superadmin with no lockout").
//
// The migration back-fill runs against rows present when 0079 applies. Open
// runs every migration on a fresh (empty) DB, so the back-fill sees nothing
// there. This test reproduces the upgrade path: seed a super-admin and a
// domain operator (with a managed domain) after migration, drop the empty
// grants table, and re-run the verbatim 0079 body so its INSERT...SELECT
// back-fill executes against the seeded rows.
func TestMigration0079GrantBackfill(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	s, err := storesqlite.Open(ctx, path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sa, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "sa@x.test",
		Flags:          store.PrincipalFlagAdmin | store.PrincipalFlagSuperAdmin,
	})
	if err != nil {
		t.Fatalf("insert super-admin: %v", err)
	}
	op, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "op@x.test",
		Flags:          store.PrincipalFlagAdmin,
	})
	if err != nil {
		t.Fatalf("insert operator: %v", err)
	}
	if err := s.Meta().AssignManagedDomain(ctx, op.ID, "d.example"); err != nil {
		t.Fatalf("assign managed domain: %v", err)
	}
	// No grants exist yet: the migration ran on an empty DB.
	if g, _ := s.Meta().ListGrantsForPrincipal(ctx, sa.ID); len(g) != 0 {
		t.Fatalf("expected no grants before back-fill, got %d", len(g))
	}
	_ = s.Close()

	// Simulate the upgrade back-fill: drop the empty grants table and re-run
	// the verbatim 0079 body against the seeded principals + managed domains.
	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE grants`); err != nil {
		t.Fatalf("drop grants: %v", err)
	}
	if _, err := raw.ExecContext(ctx, storesqlite.Migration0079SQL); err != nil {
		t.Fatalf("re-run 0079 body: %v", err)
	}
	_ = raw.Close()

	s2, err := storesqlite.Open(ctx, path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()

	saGrants, err := s2.Meta().ListGrantsForPrincipal(ctx, sa.ID)
	if err != nil {
		t.Fatalf("list super-admin grants: %v", err)
	}
	if len(saGrants) != 1 || saGrants[0].ResourceKind != store.GrantResourceServer ||
		saGrants[0].Level != store.GrantLevelSuperadmin || saGrants[0].Provenance != store.GrantProvenanceLocal {
		t.Errorf("super-admin back-fill = %+v; want one local server:superadmin grant", saGrants)
	}

	opGrants, err := s2.Meta().ListGrantsForPrincipal(ctx, op.ID)
	if err != nil {
		t.Fatalf("list operator grants: %v", err)
	}
	if len(opGrants) != 1 || opGrants[0].ResourceKind != store.GrantResourceDomain ||
		opGrants[0].ResourceID != "d.example" || opGrants[0].Level != store.GrantLevelOperator {
		t.Errorf("operator back-fill = %+v; want one domain:operator on d.example", opGrants)
	}
	// The operator must not be promoted to any server-level grant.
	for _, g := range opGrants {
		if g.ResourceKind == store.GrantResourceServer {
			t.Errorf("operator wrongly received a server grant: %+v", g)
		}
	}
}

// TestMigration0084MailboxACLGrantMigration is the migration-fidelity test
// for epic #210: it seeds representative mailbox_acl rows (varied per-letter
// combinations across multiple principals and mailboxes, including an
// insert-without-delete grant, an admin-only grant, an "anyone" row, and a
// full-rights row), re-runs the verbatim 0084 migration body, and asserts
// that the rights resolved after migration -- both per-row (GetMailboxACL)
// and per (principal, mailbox) effective access -- are identical to what
// mailbox_acl resolved before.
//
// mailbox_acl was retired by an earlier run of this same migration when
// Open ran every migration up to head, so the table is recreated by hand
// (matching migration 0004's original schema) before seeding, reproducing
// the upgrade path: mailbox_acl rows already exist when 0084 first applies.
func TestMigration0084MailboxACLGrantMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	s, err := storesqlite.Open(ctx, path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ownerA, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "ownerA@x.test",
	})
	if err != nil {
		t.Fatalf("insert ownerA: %v", err)
	}
	ownerB, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "ownerB@x.test",
	})
	if err != nil {
		t.Fatalf("insert ownerB: %v", err)
	}
	p1, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "p1@x.test",
	})
	if err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	p2, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "p2@x.test",
	})
	if err != nil {
		t.Fatalf("insert p2: %v", err)
	}
	mbA, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: ownerA.ID, Name: "Shared/A"})
	if err != nil {
		t.Fatalf("insert mbA: %v", err)
	}
	mbB, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: ownerB.ID, Name: "Shared/B"})
	if err != nil {
		t.Fatalf("insert mbB: %v", err)
	}
	mbC, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: ownerA.ID, Name: "Shared/C"})
	if err != nil {
		t.Fatalf("insert mbC: %v", err)
	}
	_ = s.Close()

	// Recreate the legacy table (dropped by the fresh Open's own run of
	// 0084) and seed representative rows, matching migration 0004's schema.
	raw, err := storesqlite.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		CREATE TABLE mailbox_acl (
		  id            INTEGER PRIMARY KEY AUTOINCREMENT,
		  mailbox_id    INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
		  principal_id  INTEGER REFERENCES principals(id) ON DELETE CASCADE,
		  rights_mask   INTEGER NOT NULL,
		  granted_by    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
		  created_at_us INTEGER NOT NULL
		) STRICT`); err != nil {
		t.Fatalf("recreate mailbox_acl: %v", err)
	}
	const (
		l = 1 << iota // lookup
		r
		sn
		w
		i
		p
		k
		x
		tt
		e
		a
	)
	seed := []struct {
		mailbox   store.MailboxID
		principal *store.PrincipalID // nil == anyone
		rights    int64
	}{
		// insert-without-delete: p1 on mbA can lookup/read/seen/insert but
		// has none of delete-message/expunge/delete-mailbox.
		{mbA.ID, &p1.ID, l | r | sn | i},
		// anyone row on mbA: baseline lookup+read for every principal.
		{mbA.ID, nil, l | r},
		// admin-only edge case: p1 on mbC can administer ACL but was never
		// separately granted lookup/read.
		{mbC.ID, &p1.ID, a},
		// full-rights edge case: p2 on mbB.
		{mbB.ID, &p2.ID, l | r | sn | w | i | p | k | x | tt | e | a},
		// write-without-seen: p1 on mbB.
		{mbB.ID, &p1.ID, l | r | w},
	}
	for _, row := range seed {
		var pidArg any
		if row.principal != nil {
			pidArg = int64(*row.principal)
		}
		if _, err := raw.ExecContext(ctx, `
			INSERT INTO mailbox_acl (mailbox_id, principal_id, rights_mask, granted_by, created_at_us)
			VALUES (?, ?, ?, ?, ?)`,
			int64(row.mailbox), pidArg, row.rights, int64(ownerA.ID), int64(1767225600000000)); err != nil {
			t.Fatalf("seed mailbox_acl row %+v: %v", row, err)
		}
	}
	// Drop the empty grants table this Open's own 0084 run already created
	// (with the widened CHECK) so the migration body's ALTER TABLE ... DROP
	// CONSTRAINT step re-runs against a table shaped like migration 0079
	// left it -- reproducing the exact upgrade path.
	if _, err := raw.ExecContext(ctx, `DROP TABLE grants`); err != nil {
		t.Fatalf("drop grants: %v", err)
	}
	if _, err := raw.ExecContext(ctx, storesqlite.Migration0079SQL); err != nil {
		t.Fatalf("re-run 0079 body: %v", err)
	}
	if _, err := raw.ExecContext(ctx, storesqlite.Migration0084SQL); err != nil {
		t.Fatalf("re-run 0084 body: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `SELECT 1 FROM mailbox_acl LIMIT 1`); err == nil {
		t.Fatalf("mailbox_acl still queryable after 0084 re-run; want it dropped")
	}
	_ = raw.Close()

	s2, err := storesqlite.Open(ctx, path, nil, clock.NewReal())
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()

	// Per-row fidelity: every migrated grant decodes back to exactly the
	// rights_mask it was seeded with, tagged with the migration provenance.
	rowsA, err := s2.Meta().GetMailboxACL(ctx, mbA.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL mbA: %v", err)
	}
	if len(rowsA) != 2 {
		t.Fatalf("mbA acl rows = %d; want 2 (anyone + p1)", len(rowsA))
	}
	for _, row := range rowsA {
		if row.PrincipalID == nil {
			if row.Rights != store.ACLRightLookup|store.ACLRightRead {
				t.Errorf("mbA anyone row rights = %v; want lr", row.Rights)
			}
			continue
		}
		if *row.PrincipalID != p1.ID {
			t.Fatalf("mbA unexpected principal row: %+v", row)
		}
		want := store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen | store.ACLRightInsert
		if row.Rights != want {
			t.Errorf("mbA p1 row rights = %v; want %v (insert-without-delete)", row.Rights, want)
		}
		if row.Rights&(store.ACLRightDeleteMessage|store.ACLRightExpunge|store.ACLRightDeleteMailbox) != 0 {
			t.Errorf("mbA p1 row must not carry any delete-family bit: %v", row.Rights)
		}
	}
	grantsOnA, err := s2.Meta().ListGrantsOnResource(ctx, store.GrantResourceMailbox,
		strconv.FormatUint(uint64(mbA.ID), 10))
	if err != nil {
		t.Fatalf("ListGrantsOnResource mbA: %v", err)
	}
	for _, g := range grantsOnA {
		if g.Provenance != store.GrantProvenanceACLMigration {
			t.Errorf("migrated mbA grant provenance = %q; want %q", g.Provenance, store.GrantProvenanceACLMigration)
		}
	}

	// Effective-rights fidelity: the resolved access for every
	// (principal, mailbox) pair matches what mailbox_acl resolved before
	// migration (own row OR'd with any "anyone" row on that mailbox).
	cases := []struct {
		name      string
		mailbox   store.MailboxID
		principal store.PrincipalID
		want      store.ACLRights
	}{
		{"p1 on mbA: own row union anyone row (anyone is a strict subset)",
			mbA.ID, p1.ID, store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen | store.ACLRightInsert},
		{"p2 on mbA: only the anyone row applies",
			mbA.ID, p2.ID, store.ACLRightLookup | store.ACLRightRead},
		{"p1 on mbC: admin-only, no lookup/read ever granted",
			mbC.ID, p1.ID, store.ACLRightAdmin},
		{"p2 on mbC: no row at all",
			mbC.ID, p2.ID, 0},
		{"p2 on mbB: full rights",
			mbB.ID, p2.ID, store.ACLRightsAll},
		{"p1 on mbB: write without seen",
			mbB.ID, p1.ID, store.ACLRightLookup | store.ACLRightRead | store.ACLRightWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := s2.Meta().GetMailboxACL(ctx, c.mailbox)
			if err != nil {
				t.Fatalf("GetMailboxACL: %v", err)
			}
			var have store.ACLRights
			for _, row := range rows {
				if row.PrincipalID == nil || *row.PrincipalID == c.principal {
					have |= row.Rights
				}
			}
			if have != c.want {
				t.Errorf("resolved rights = %v; want %v", have, c.want)
			}
		})
	}

	// ListMailboxesAccessibleBy filters strictly on the lookup ("l") bit,
	// same as the legacy mailbox_acl query did: p1 has lookup on mbA and
	// mbB (own rows carry "l") but NOT mbC, whose admin-only row never
	// separately carries lookup -- LIST visibility and ACL-administer
	// rights are independent RFC 4314 rights, and this migration must not
	// conflate them. p2 has lookup on mbA (via the anyone row) and mbB
	// (own row).
	p1Accessible, err := s2.Meta().ListMailboxesAccessibleBy(ctx, p1.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy p1: %v", err)
	}
	if len(p1Accessible) != 2 {
		t.Errorf("p1 accessible = %+v; want 2 mailboxes (mbA, mbB; mbC's admin-only row lacks lookup)", p1Accessible)
	}
	p2Accessible, err := s2.Meta().ListMailboxesAccessibleBy(ctx, p2.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy p2: %v", err)
	}
	if len(p2Accessible) != 2 {
		t.Errorf("p2 accessible = %+v; want 2 mailboxes (mbA via anyone, mbB own)", p2Accessible)
	}
}
