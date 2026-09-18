package admin

// imapimport_repair_archive_test.go exercises restoreIMAPImportArchive
// (re #376, second round): a message currently a member of INBOX is moved
// to Archive (created on demand) and marked $seen; a message with no INBOX
// membership is left untouched; a message belonging to a different
// principal is reported as an error and left untouched.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

func TestRestoreIMAPImportArchive_SQLite(t *testing.T) {
	testRestoreIMAPImportArchive(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestRestoreIMAPImportArchive_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = st.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	testRestoreIMAPImportArchive(t, st)
}

func insertRestoreArchiveFixtureMessage(t *testing.T, st store.Store, pid store.PrincipalID, msgIDHeader string, mailboxID store.MailboxID) store.MessageID {
	t.Helper()
	ctx := context.Background()
	blob, err := st.Blobs().Put(ctx, strings.NewReader("body of "+msgIDHeader))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: pid,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "restore-archive", MessageID: msgIDHeader},
	}, []store.MessageMailbox{{MailboxID: mailboxID}})
	if err != nil {
		t.Fatalf("InsertMessage(%s): %v", msgIDHeader, err)
	}
	msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, pid, msgIDHeader)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(%s): %v", msgIDHeader, err)
	}
	return msg.ID
}

func testRestoreIMAPImportArchive(t *testing.T, st store.Store) {
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "restorearchive@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	other, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "otherprincipal@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal (other): %v", err)
	}

	inboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (INBOX): %v", err)
	}
	otherInboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: other.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (other INBOX): %v", err)
	}

	// resurfacedID: currently in INBOX only, unseen -- the #376 shape.
	resurfacedID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "resurfaced@test", inboxMB.ID)
	// alreadyArchivedID: no INBOX membership -- a plain custom label, not
	// Archive, so the idempotent "nothing to do" path is exercised without
	// depending on an Archive mailbox already existing.
	labelMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "SomeLabel"})
	if err != nil {
		t.Fatalf("InsertMailbox (SomeLabel): %v", err)
	}
	alreadyArchivedID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "already-archived@test", labelMB.ID)
	// otherPrincipalID: belongs to a different principal; must be rejected.
	otherPrincipalID := insertRestoreArchiveFixtureMessage(t, st, other.ID, "other-principal@test", otherInboxMB.ID)

	// dry-run must not write anything.
	dryResults, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{resurfacedID}, true)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive (dry-run): %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Action != "moved-to-archive" {
		t.Fatalf("dry-run results = %+v; want one moved-to-archive", dryResults)
	}
	msgAfterDry, err := st.Meta().GetMessage(ctx, resurfacedID)
	if err != nil {
		t.Fatalf("GetMessage (after dry-run): %v", err)
	}
	if len(msgAfterDry.Mailboxes) != 1 || msgAfterDry.Mailboxes[0].MailboxID != inboxMB.ID {
		t.Fatalf("dry-run wrote a change: mailboxes = %+v", msgAfterDry.Mailboxes)
	}

	results, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{resurfacedID, alreadyArchivedID, otherPrincipalID}, false)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v; want 3 entries", results)
	}
	if results[0].Action != "moved-to-archive" {
		t.Errorf("resurfaced result = %+v; want moved-to-archive", results[0])
	}
	if results[1].Action != "already-archived" {
		t.Errorf("already-archived result = %+v; want already-archived", results[1])
	}
	if results[2].Action != "skipped" || results[2].Error == "" {
		t.Errorf("other-principal result = %+v; want skipped with an error", results[2])
	}

	// The resurfaced message: INBOX membership gone, Archive membership
	// present and $seen.
	msg2, err := st.Meta().GetMessage(ctx, resurfacedID)
	if err != nil {
		t.Fatalf("GetMessage (resurfaced, after repair): %v", err)
	}
	if len(msg2.Mailboxes) != 1 {
		t.Fatalf("resurfaced mailboxes = %+v; want exactly 1 (Archive)", msg2.Mailboxes)
	}
	mbs, err := st.Meta().ListMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var archiveMBID store.MailboxID
	for _, mb := range mbs {
		if mb.Attributes&store.MailboxAttrArchive != 0 {
			archiveMBID = mb.ID
		}
	}
	if archiveMBID == 0 {
		t.Fatal("Archive mailbox was not created")
	}
	if msg2.Mailboxes[0].MailboxID != archiveMBID {
		t.Errorf("resurfaced message mailbox = %d; want Archive (%d)", msg2.Mailboxes[0].MailboxID, archiveMBID)
	}
	if msg2.Mailboxes[0].Flags&store.MessageFlagSeen == 0 {
		t.Error("resurfaced message's Archive membership is not $seen")
	}

	// The already-archived message (no INBOX membership) is untouched.
	msg3, err := st.Meta().GetMessage(ctx, alreadyArchivedID)
	if err != nil {
		t.Fatalf("GetMessage (already-archived, after repair): %v", err)
	}
	if len(msg3.Mailboxes) != 1 || msg3.Mailboxes[0].MailboxID != labelMB.ID {
		t.Errorf("already-archived mailboxes = %+v; want unchanged [SomeLabel]", msg3.Mailboxes)
	}

	// The other-principal's message is untouched.
	msg4, err := st.Meta().GetMessage(ctx, otherPrincipalID)
	if err != nil {
		t.Fatalf("GetMessage (other-principal, after repair): %v", err)
	}
	if len(msg4.Mailboxes) != 1 || msg4.Mailboxes[0].MailboxID != otherInboxMB.ID {
		t.Errorf("other-principal mailboxes = %+v; want unchanged [INBOX]", msg4.Mailboxes)
	}

	// Re-running the repair on the now-archived message is a no-op
	// (idempotent).
	results2, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{resurfacedID}, false)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive (second run): %v", err)
	}
	if len(results2) != 1 || results2[0].Action != "already-archived" {
		t.Errorf("second-run result = %+v; want already-archived (idempotent)", results2)
	}
}
