package storepg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// TestMigration0101WakeMailboxBackfill is storesqlite's
// TestMigration0101WakeMailboxBackfill counterpart for Postgres: an
// in-flight snooze that straddles the migration -- a message_mailboxes
// row with snoozed_until_us already set and no wake_mailbox_id -- must
// resolve wake_mailbox_id to the owning principal's Inbox once the
// migration's UPDATE runs (issue #274), so the wake worker (stage 2)
// adds the message there instead of leaving it silently in place.
func TestMigration0101WakeMailboxBackfill(t *testing.T) {
	dsn, ok := getDSN(t)
	if !ok {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres integration test")
	}

	// Guard against Migration0101BackfillSQL (a hand-copied constant,
	// re-exported separately because the ALTER TABLE statement that
	// precedes it in the real file cannot be re-run against an
	// already-migrated DB) drifting from the production migration text.
	if !strings.Contains(storepg.Migration0101SQL, storepg.Migration0101BackfillSQL) {
		t.Fatalf("Migration0101BackfillSQL is not a substring of the real 0101 migration body -- update the exported constant")
	}

	ctx := context.Background()
	s, cleanup := openStore(t, dsn)
	defer cleanup()
	pg, ok := s.(*storepg.Store)
	if !ok {
		t.Fatalf("openStore returned %T, want *storepg.Store", s)
	}

	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "legacy-snooze@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	inbox, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(INBOX): %v", err)
	}
	sent, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "Sent",
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Sent): %v", err)
	}

	ref, err := s.Blobs().Put(ctx, strings.NewReader("legacy-snooze-body"))
	if err != nil {
		t.Fatalf("Blobs().Put: %v", err)
	}
	if _, _, err := s.Meta().InsertMessage(ctx,
		store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size},
		[]store.MessageMailbox{{MailboxID: sent.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	pool := pg.Pool()
	var mid int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM messages WHERE principal_id = $1`, int64(p.ID)).Scan(&mid); err != nil {
		t.Fatalf("query message id: %v", err)
	}

	// Simulate a pre-#274 write: a snoozed row with no wake destination,
	// the shape every existing snoozed row had before migration 0101's
	// ALTER TABLE ran.
	due := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		UPDATE message_mailboxes
		   SET snoozed_until_us = $1, keywords_csv = '$snoozed'
		 WHERE message_id = $2 AND mailbox_id = $3`,
		due.UnixMicro(), mid, int64(sent.ID)); err != nil {
		t.Fatalf("seed legacy snooze row: %v", err)
	}

	before, err := s.Meta().GetMessage(ctx, store.MessageID(mid))
	if err != nil {
		t.Fatalf("GetMessage before backfill: %v", err)
	}
	if before.SnoozedUntil == nil {
		t.Fatalf("pre-backfill row should be snoozed")
	}
	if wake := wakeMailboxIDFor(before, sent.ID); wake != nil {
		t.Fatalf("pre-backfill WakeMailboxID = %v, want nil", *wake)
	}

	// Apply the migration's back-fill UPDATE, as would happen on
	// upgrade for a row that already existed when 0101 ran.
	if _, err := pool.Exec(ctx, storepg.Migration0101BackfillSQL); err != nil {
		t.Fatalf("apply backfill: %v", err)
	}

	after, err := s.Meta().GetMessage(ctx, store.MessageID(mid))
	if err != nil {
		t.Fatalf("GetMessage after backfill: %v", err)
	}
	wake := wakeMailboxIDFor(after, sent.ID)
	if wake == nil || *wake != inbox.ID {
		t.Fatalf("WakeMailboxID after backfill = %v, want %d (Inbox)", wake, inbox.ID)
	}
}

// wakeMailboxIDFor returns the WakeMailboxID of the msg.Mailboxes entry
// matching mailboxID, or nil if no such entry exists.
func wakeMailboxIDFor(msg store.Message, mailboxID store.MailboxID) *store.MailboxID {
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID == mailboxID {
			return mm.WakeMailboxID
		}
	}
	return nil
}
