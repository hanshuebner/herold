package admin

// imapimport_repair_seen_test.go exercises repairIMAPImportSeen (re #435):
//
//   - An unseen message with two imapimport_message_state rows (the
//     dedup-folded To/Cc scenario #435 reports) is marked $seen and both
//     state rows gain \Seen in LastSyncedFlags.
//   - A message on which every membership is already $seen is reported
//     "already-seen" and left untouched.
//   - A message with no imapimport_message_state row is reported as an
//     error and left untouched (not an imported message).
//   - A message belonging to a different principal is reported as an error
//     and left untouched.
//   - --dry-run reports the intended action without writing anything.

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

func TestRepairIMAPImportSeen_SQLite(t *testing.T) {
	testRepairIMAPImportSeen(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestRepairIMAPImportSeen_Postgres(t *testing.T) {
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
	testRepairIMAPImportSeen(t, st)
}

func testRepairIMAPImportSeen(t *testing.T, st store.Store) {
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "repairseen@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	other, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "repairseen-other@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal (other): %v", err)
	}

	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (INBOX): %v", err)
	}

	insertMsg := func(msgIDHeader string) store.MessageID {
		t.Helper()
		blob, err := st.Blobs().Put(ctx, strings.NewReader("body of "+msgIDHeader))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		_, _, err = st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID: p.ID,
			Size:        blob.Size,
			Blob:        blob,
			Envelope:    store.Envelope{Subject: "repair-seen", MessageID: msgIDHeader},
		}, []store.MessageMailbox{{MailboxID: inbox.ID}})
		if err != nil {
			t.Fatalf("InsertMessage(%s): %v", msgIDHeader, err)
		}
		msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, p.ID, msgIDHeader)
		if err != nil {
			t.Fatalf("GetMessageByMessageIDHeader(%s): %v", msgIDHeader, err)
		}
		return msg.ID
	}

	acc, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Repair Seen Test",
		Host:         "imap.example.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     "repair-seen",
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	// Two upstream copies (a To copy and a Cc copy, uids 1 and 2) folded
	// onto one herold message -- the #435 dedup scenario -- both left at
	// their ingest-time baseline (unseen).
	dedupMsgID := insertMsg("dedup@repair-seen.test")
	for _, uid := range []uint32{1, 2} {
		if err := st.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
			AccountID:       acc.ID,
			UpstreamFolder:  "INBOX",
			UpstreamUID:     uid,
			HeroldMessageID: dedupMsgID,
			HeroldMailboxID: inbox.ID,
			MappedMailboxID: inbox.ID,
		}); err != nil {
			t.Fatalf("UpsertIMAPImportMessageState (uid %d): %v", uid, err)
		}
	}

	// A message with no imapimport_message_state row at all.
	plainMsgID := insertMsg("plain@repair-seen.test")

	// A message belonging to a different principal.
	otherInbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: other.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (other INBOX): %v", err)
	}
	blob, err := st.Blobs().Put(ctx, strings.NewReader("body of other"))
	if err != nil {
		t.Fatalf("Blobs.Put (other): %v", err)
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: other.ID,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "repair-seen", MessageID: "other@repair-seen.test"},
	}, []store.MessageMailbox{{MailboxID: otherInbox.ID}})
	if err != nil {
		t.Fatalf("InsertMessage (other): %v", err)
	}
	otherMsg, err := st.Meta().GetMessageByMessageIDHeader(ctx, other.ID, "other@repair-seen.test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (other): %v", err)
	}

	// --dry-run must not write anything.
	dryResults, err := repairIMAPImportSeen(ctx, st, p.ID, []store.MessageID{dedupMsgID}, true)
	if err != nil {
		t.Fatalf("repairIMAPImportSeen (dry-run): %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Action != "marked-seen" {
		t.Fatalf("dry-run result: %+v", dryResults)
	}
	msg, err := st.Meta().GetMessage(ctx, dedupMsgID)
	if err != nil {
		t.Fatalf("GetMessage after dry-run: %v", err)
	}
	if msg.Mailboxes[0].Flags&store.MessageFlagSeen != 0 {
		t.Error("dry-run must not have written \\Seen")
	}

	// Apply for real.
	results, err := repairIMAPImportSeen(ctx, st, p.ID, []store.MessageID{dedupMsgID, plainMsgID, otherMsg.ID}, false)
	if err != nil {
		t.Fatalf("repairIMAPImportSeen: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d: %+v", len(results), results)
	}

	if results[0].Action != "marked-seen" {
		t.Errorf("dedup message: want marked-seen, got %+v", results[0])
	}
	msg2, err := st.Meta().GetMessage(ctx, dedupMsgID)
	if err != nil {
		t.Fatalf("GetMessage after repair: %v", err)
	}
	if msg2.Mailboxes[0].Flags&store.MessageFlagSeen == 0 {
		t.Error("dedup message should be \\Seen after repair")
	}
	states, err := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, dedupMsgID)
	if err != nil {
		t.Fatalf("ListIMAPImportMessageStatesByMessage: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("want 2 state rows, got %d", len(states))
	}
	for _, s := range states {
		if !s.LastSyncedFlags.HasSeen() {
			t.Errorf("state row (uid %d) should have \\Seen in last_synced_flags after repair", s.UpstreamUID)
		}
	}

	if results[1].Error == "" {
		t.Errorf("plain message (no state row) should be reported as an error, got %+v", results[1])
	}
	msgPlain, err := st.Meta().GetMessage(ctx, plainMsgID)
	if err != nil {
		t.Fatalf("GetMessage (plain): %v", err)
	}
	if msgPlain.Mailboxes[0].Flags&store.MessageFlagSeen != 0 {
		t.Error("plain message should be left untouched (no state row)")
	}

	if results[2].Error == "" {
		t.Errorf("cross-principal message should be reported as an error, got %+v", results[2])
	}

	// Re-running on the now-seen dedup message reports already-seen.
	results2, err := repairIMAPImportSeen(ctx, st, p.ID, []store.MessageID{dedupMsgID}, false)
	if err != nil {
		t.Fatalf("repairIMAPImportSeen (second run): %v", err)
	}
	if results2[0].Action != "already-seen" {
		t.Errorf("second run: want already-seen, got %+v", results2[0])
	}

	if anySkipped(results) == false {
		t.Error("anySkipped should report true given the plain and cross-principal errors above")
	}
}
