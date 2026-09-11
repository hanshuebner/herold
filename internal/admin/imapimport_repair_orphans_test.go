package admin

// imapimport_repair_orphans_test.go exercises repairIMAPImportOrphans
// (issue #319) against both store backends: a message carrying only an
// IMAP-import account's provenance label and no message_state row is
// filed into Junk when it carries a recorded "spam" verdict, or into
// INBOX otherwise; a tracked or multiply-placed message is left alone.

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

func TestRepairIMAPImportOrphans_SQLite(t *testing.T) {
	testRepairIMAPImportOrphans(t, sqlitetest.Open(t, clock.NewReal()))
}

func TestRepairIMAPImportOrphans_Postgres(t *testing.T) {
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
	testRepairIMAPImportOrphans(t, st)
}

// insertOrphanFixtureMessage inserts a message that is a member only of
// labelID (mirroring the #319 orphan shape) and returns its id.
func insertOrphanFixtureMessage(t *testing.T, st store.Store, pid store.PrincipalID, msgIDHeader string, labelID store.MailboxID) store.MessageID {
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
		Envelope:    store.Envelope{Subject: "orphan", MessageID: msgIDHeader},
	}, []store.MessageMailbox{{MailboxID: labelID}})
	if err != nil {
		t.Fatalf("InsertMessage(%s): %v", msgIDHeader, err)
	}
	msg, err := st.Meta().GetMessageByMessageIDHeader(ctx, pid, msgIDHeader)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(%s): %v", msgIDHeader, err)
	}
	return msg.ID
}

func testRepairIMAPImportOrphans(t *testing.T, st store.Store) {
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "orphans@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	acc, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "Repair Test",
		Host:             "imap.example.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "orphans",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("v1:test"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	label, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        acc.AccountName,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (label): %v", err)
	}
	if err := st.Meta().SetIMAPImportProvenanceMailbox(ctx, acc.ID, label.ID); err != nil {
		t.Fatalf("SetIMAPImportProvenanceMailbox: %v", err)
	}
	acc, err = st.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}

	inboxMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (INBOX): %v", err)
	}
	archiveMB, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Archive",
	})
	if err != nil {
		t.Fatalf("InsertMailbox (Archive): %v", err)
	}

	// (a) label-only, recorded spam verdict -> orphan, files to Junk.
	spamOrphan := insertOrphanFixtureMessage(t, st, p.ID, "orphan-spam@test", label.ID)
	if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:   spamOrphan,
		PrincipalID: p.ID,
		SpamVerdict: strPtr("spam"),
	}); err != nil {
		t.Fatalf("SetLLMClassification(spamOrphan): %v", err)
	}

	// (b) label-only, no verdict -> orphan, files to INBOX.
	hamOrphan := insertOrphanFixtureMessage(t, st, p.ID, "orphan-ham@test", label.ID)

	// (c) label-only, but a message_state row exists for this account ->
	// tracked, not an orphan (the #319 fix keeps it addressable).
	tracked := insertOrphanFixtureMessage(t, st, p.ID, "orphan-tracked@test", label.ID)
	if err := st.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     1,
		HeroldMessageID: tracked,
		HeroldMailboxID: 999999, // a mailbox this test never creates; placement is irrelevant here
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState(tracked): %v", err)
	}

	// (d) label plus a real membership already -> not an orphan.
	notOrphan := insertOrphanFixtureMessage(t, st, p.ID, "orphan-notorphan@test", label.ID)
	if _, _, err := st.Meta().AddMessageToMailbox(ctx, notOrphan, archiveMB.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(notOrphan, Archive): %v", err)
	}

	// Dry run: reports counts, moves nothing.
	dry, err := repairIMAPImportOrphans(ctx, st, p.ID, true)
	if err != nil {
		t.Fatalf("repairIMAPImportOrphans (dry-run): %v", err)
	}
	wantDry := IMAPImportRepairOrphansSummary{
		AccountsScanned: 1, LabelMembers: 4, Orphans: 2, FiledJunk: 1, FiledInbox: 1,
	}
	if dry != wantDry {
		t.Fatalf("dry-run summary = %+v, want %+v", dry, wantDry)
	}
	if got := mailboxSet(t, st, spamOrphan); len(got) != 1 || !got[acc.AccountName] {
		t.Errorf("dry-run must not move spamOrphan: mailboxes = %v", got)
	}

	// Apply.
	sum, err := repairIMAPImportOrphans(ctx, st, p.ID, false)
	if err != nil {
		t.Fatalf("repairIMAPImportOrphans: %v", err)
	}
	if sum != wantDry {
		t.Fatalf("apply summary = %+v, want %+v", sum, wantDry)
	}

	if got := mailboxSet(t, st, spamOrphan); len(got) != 2 || !got[acc.AccountName] || !got["Junk"] {
		t.Errorf("spamOrphan mailboxes = %v, want {%s, Junk}", got, acc.AccountName)
	}
	if got := mailboxSet(t, st, hamOrphan); len(got) != 2 || !got[acc.AccountName] || !got["INBOX"] {
		t.Errorf("hamOrphan mailboxes = %v, want {%s, INBOX}", got, acc.AccountName)
	}
	if got := mailboxSet(t, st, tracked); len(got) != 1 || !got[acc.AccountName] {
		t.Errorf("tracked mailboxes = %v, want {%s} (untouched -- has a message_state row)", got, acc.AccountName)
	}
	if got := mailboxSet(t, st, notOrphan); len(got) != 2 || !got[acc.AccountName] || !got["Archive"] {
		t.Errorf("notOrphan mailboxes = %v, want {%s, Archive} (untouched -- already has a real membership)", got, acc.AccountName)
	}

	// Re-running is idempotent: both filed orphans now carry a real
	// membership, so a second pass finds nothing left to repair.
	sum2, err := repairIMAPImportOrphans(ctx, st, p.ID, false)
	if err != nil {
		t.Fatalf("repairIMAPImportOrphans (second run): %v", err)
	}
	if sum2.Orphans != 0 {
		t.Errorf("second run found %d orphans, want 0 (idempotent)", sum2.Orphans)
	}

	_ = inboxMB
}

func strPtr(s string) *string { return &s }
