package admin

// imapimport_repair_archive_test.go exercises restoreIMAPImportArchive (re
// #376, second round + its eligibility-guard follow-up):
//
//   - A message in INBOX matching one of the #376 shapes -- principal-sent
//     via a Sent-role membership, principal-sent via a From identity match,
//     or an imapimport dedup hit whose thread has an Archive member -- is
//     moved to Archive (created on demand) and marked $seen.
//   - A message in INBOX matching NEITHER shape is refused (left untouched,
//     reported with a reason) unless --force is given, in which case it is
//     moved anyway.
//   - A message with no INBOX membership is left untouched (idempotent).
//   - A message belonging to a different principal is reported as an error
//     and left untouched.

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

// insertRestoreArchiveFixtureMessage inserts a message with the given
// Message-ID header, placed in mailboxID plus any extraMailboxes, and
// returns its store.MessageID.
func insertRestoreArchiveFixtureMessage(t *testing.T, st store.Store, pid store.PrincipalID, msgIDHeader string, mailboxID store.MailboxID, extraMailboxes ...store.MailboxID) store.MessageID {
	t.Helper()
	ctx := context.Background()
	blob, err := st.Blobs().Put(ctx, strings.NewReader("body of "+msgIDHeader))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	targets := []store.MessageMailbox{{MailboxID: mailboxID}}
	for _, mb := range extraMailboxes {
		targets = append(targets, store.MessageMailbox{MailboxID: mb})
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: pid,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "restore-archive", MessageID: msgIDHeader},
	}, targets)
	if err != nil {
		t.Fatalf("InsertMessage(%s): %v", msgIDHeader, err)
	}
	msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, pid, msgIDHeader)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(%s): %v", msgIDHeader, err)
	}
	return msg.ID
}

// insertRestoreArchiveFromFixtureMessage is
// insertRestoreArchiveFixtureMessage with an explicit Envelope.From, for the
// "principal-sent via From identity match" eligibility shape.
func insertRestoreArchiveFromFixtureMessage(t *testing.T, st store.Store, pid store.PrincipalID, msgIDHeader, from string, mailboxID store.MailboxID) store.MessageID {
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
		Envelope:    store.Envelope{Subject: "restore-archive", MessageID: msgIDHeader, From: from},
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
	sentMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "Sent",
		Attributes:  store.MailboxAttrSent,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (Sent): %v", err)
	}
	otherInboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: other.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (other INBOX): %v", err)
	}

	// resurfacedID: currently in INBOX plus a Sent-role membership -- the
	// principal-sent-via-Sent-role-membership #376 shape.
	resurfacedID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "resurfaced@test", inboxMB.ID, sentMB.ID)
	// resurfacedByFromID: currently in INBOX only, but its From names the
	// principal's own canonical address -- the principal-sent-via-From
	// #376 shape.
	resurfacedByFromID := insertRestoreArchiveFromFixtureMessage(t, st, p.ID, "resurfaced-from@test", "restorearchive@example.test", inboxMB.ID)
	// dedupHitArchivedThreadID: in INBOX only, carries an
	// imapimport_message_state row (the dedup-hit shape) and its thread has
	// an Archive member -- the second #376 eligibility shape.
	dedupAcc, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Dedup Hit Test",
		Host:         "imap.example.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "dedup-hit",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount (dedup-hit): %v", err)
	}
	dedupHitArchivedThreadID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "dedup-hit@test", inboxMB.ID)
	if err := st.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       dedupAcc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     1,
		HeroldMessageID: dedupHitArchivedThreadID,
		HeroldMailboxID: inboxMB.ID,
		MappedMailboxID: inboxMB.ID,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState (dedup-hit): %v", err)
	}
	archiveMBForThread, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "Archive",
		Attributes:  store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (Archive, thread sibling): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		ThreadID:    uint64(dedupHitArchivedThreadID),
		Envelope:    store.Envelope{Subject: "restore-archive", MessageID: "dedup-hit-thread-sibling@test"},
		Size:        1,
		Blob:        mustBlobRestoreArchive(t, st, "sibling"),
	}, []store.MessageMailbox{{MailboxID: archiveMBForThread.ID}}); err != nil {
		t.Fatalf("InsertMessage (thread sibling): %v", err)
	}
	// ineligibleID: currently in INBOX only, matching neither #376 shape --
	// must be refused without --force.
	ineligibleID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "ineligible@test", inboxMB.ID)
	// ineligibleForceID: same shape as ineligibleID, moved only with
	// --force.
	ineligibleForceID := insertRestoreArchiveFixtureMessage(t, st, p.ID, "ineligible-force@test", inboxMB.ID)
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

	// A refused message is left untouched and reported, without --force.
	refusedResults, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{ineligibleID}, false, false)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive (refused): %v", err)
	}
	if len(refusedResults) != 1 || refusedResults[0].Action != "refused" || refusedResults[0].Reason == "" {
		t.Fatalf("refused result = %+v; want one refused entry with a reason", refusedResults)
	}
	if !anyRefused(refusedResults) {
		t.Error("anyRefused should report the refusal")
	}
	msgIneligible, err := st.Meta().GetMessage(ctx, ineligibleID)
	if err != nil {
		t.Fatalf("GetMessage (ineligible): %v", err)
	}
	if len(msgIneligible.Mailboxes) != 1 || msgIneligible.Mailboxes[0].MailboxID != inboxMB.ID {
		t.Fatalf("refused message was modified: mailboxes = %+v", msgIneligible.Mailboxes)
	}

	// --force overrides the refusal.
	forcedResults, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{ineligibleForceID}, false, true)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive (forced): %v", err)
	}
	if len(forcedResults) != 1 || forcedResults[0].Action != "moved-to-archive" || !strings.Contains(forcedResults[0].Reason, "forced") {
		t.Fatalf("forced result = %+v; want one moved-to-archive entry noting the override", forcedResults)
	}
	if anyRefused(forcedResults) {
		t.Error("anyRefused should not report a forced move")
	}

	// dry-run must not write anything, for an eligible message.
	dryResults, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{resurfacedID}, true, false)
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
	if len(msgAfterDry.Mailboxes) != 2 {
		t.Fatalf("dry-run wrote a change: mailboxes = %+v", msgAfterDry.Mailboxes)
	}

	results, err := restoreIMAPImportArchive(ctx, st, p.ID,
		[]store.MessageID{resurfacedID, resurfacedByFromID, dedupHitArchivedThreadID, alreadyArchivedID, otherPrincipalID},
		false, false)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %+v; want 5 entries", results)
	}
	if results[0].Action != "moved-to-archive" {
		t.Errorf("resurfaced (Sent-role) result = %+v; want moved-to-archive", results[0])
	}
	if results[1].Action != "moved-to-archive" {
		t.Errorf("resurfaced (From-identity) result = %+v; want moved-to-archive", results[1])
	}
	if results[2].Action != "moved-to-archive" {
		t.Errorf("dedup-hit (archived thread) result = %+v; want moved-to-archive", results[2])
	}
	if results[3].Action != "already-archived" {
		t.Errorf("already-archived result = %+v; want already-archived", results[3])
	}
	if results[4].Action != "skipped" || results[4].Error == "" {
		t.Errorf("other-principal result = %+v; want skipped with an error", results[4])
	}

	// Every eligible, moved message: INBOX membership gone, Archive
	// membership present and $seen. The dedup-hit message additionally gets
	// its message_state row repaired.
	for _, id := range []store.MessageID{resurfacedID, resurfacedByFromID, dedupHitArchivedThreadID, ineligibleForceID} {
		msg, err := st.Meta().GetMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetMessage (moved, id=%d): %v", id, err)
		}
		var archiveMembership *store.MessageMailbox
		for i := range msg.Mailboxes {
			if msg.Mailboxes[i].MailboxID == inboxMB.ID {
				t.Fatalf("message %d still has an INBOX membership: %+v", id, msg.Mailboxes)
			}
			mbs, lerr := st.Meta().ListMailboxes(ctx, p.ID)
			if lerr != nil {
				t.Fatalf("ListMailboxes: %v", lerr)
			}
			for _, mb := range mbs {
				if mb.ID == msg.Mailboxes[i].MailboxID && mb.Attributes&store.MailboxAttrArchive != 0 {
					archiveMembership = &msg.Mailboxes[i]
				}
			}
		}
		if archiveMembership == nil {
			t.Fatalf("message %d has no Archive membership: %+v", id, msg.Mailboxes)
		}
		if archiveMembership.Flags&store.MessageFlagSeen == 0 {
			t.Errorf("message %d's Archive membership is not $seen", id)
		}
	}

	msAfter, found, err := st.Meta().GetIMAPImportMessageState(ctx, dedupAcc.ID, "INBOX", 1)
	if err != nil || !found {
		t.Fatalf("GetIMAPImportMessageState (dedup-hit, after repair): found=%v err=%v", found, err)
	}
	msgDedup, err := st.Meta().GetMessage(ctx, dedupHitArchivedThreadID)
	if err != nil {
		t.Fatalf("GetMessage (dedup-hit): %v", err)
	}
	var dedupArchiveMBID store.MailboxID
	for _, mm := range msgDedup.Mailboxes {
		dedupArchiveMBID = mm.MailboxID
	}
	if msAfter.HeroldMailboxID != dedupArchiveMBID {
		t.Errorf("dedup-hit message_state.HeroldMailboxID = %d; want the Archive mailbox (%d)", msAfter.HeroldMailboxID, dedupArchiveMBID)
	}
	if !msAfter.LastSyncedFlags.HasSeen() {
		t.Error("dedup-hit message_state.LastSyncedFlags should record \\Seen after the repair")
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
	results2, err := restoreIMAPImportArchive(ctx, st, p.ID, []store.MessageID{resurfacedID}, false, false)
	if err != nil {
		t.Fatalf("restoreIMAPImportArchive (second run): %v", err)
	}
	if len(results2) != 1 || results2[0].Action != "already-archived" {
		t.Errorf("second-run result = %+v; want already-archived (idempotent)", results2)
	}
}

// mustBlobRestoreArchive is a one-line store.Blobs().Put helper for a fixture
// message whose body content does not matter.
func mustBlobRestoreArchive(t *testing.T, st store.Store, body string) store.BlobRef {
	t.Helper()
	blob, err := st.Blobs().Put(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	return blob
}
