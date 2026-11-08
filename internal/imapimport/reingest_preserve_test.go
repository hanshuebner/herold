package imapimport

// reingest_preserve_test.go pins #563 for the ingest paths: a re-ingest of a
// UID whose state row already names the herold message (the next pass
// re-fetches UIDs above a failed one, re #279) must not rewrite the
// placement, MappedMailboxID, LastSyncedFlags or upstream identity that
// write-back and the identity backfill maintain on the row.

import (
	"context"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// divergeRow rewrites ms into a state a later re-ingest would otherwise
// clobber: a different recorded identity, flags, and a MappedMailboxID that
// differs from the placement (the #319 marker). It returns the new row.
func divergeRow(t *testing.T, ha *testharness.Server, ms store.IMAPImportMessageState, mapped store.MailboxID) store.IMAPImportMessageState {
	t.Helper()
	ms.MappedMailboxID = mapped
	ms.LastSyncedFlags = store.IMAPImportFlagFlagged
	ms.UpstreamMessageID = "recorded-by-backfill@test"
	ms.UpstreamSize = 4242
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(context.Background(), ms); err != nil {
		t.Fatalf("diverge row: %v", err)
	}
	return ms
}

func assertRowUnchanged(t *testing.T, ha *testharness.Server, want store.IMAPImportMessageState) {
	t.Helper()
	got, found, err := ha.Store.Meta().GetIMAPImportMessageState(context.Background(), want.AccountID, want.UpstreamFolder, want.UpstreamUID)
	if err != nil || !found {
		t.Fatalf("row: found=%v err=%v", found, err)
	}
	if got != want {
		t.Errorf("re-ingest rewrote the row:\n got  %+v\n want %+v", got, want)
	}
}

func newIngestWorker(t *testing.T, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount) (*accountWorker, Conn) {
	t.Helper()
	w := newAccountWorker(accountWorkerOpts{
		account: acc, store: ha.Store, dataKey: testDataKey(t),
		cfg: sysconfig.IMAPImportConfig{}, log: newTestLogger(t), clk: ha.Clock,
		dialer: &fakeDialer{ts: ts}, categoriser: noopCategoriser{},
	})
	conn, err := w.openSecondaryConn(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return w, conn
}

func otherMailbox(t *testing.T, ha *testharness.Server, acc store.IMAPImportAccount, name string) store.MailboxID {
	t.Helper()
	mb, err := ha.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{PrincipalID: store.PrincipalID(acc.PrincipalID), Name: name})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	return mb.ID
}

// The forward re-fetch of an already ingested UID (fetchAndIngest on a UID
// that has a state row: what the next pass does for UIDs above a failed one).
func TestReingestLinkedUID_PreservesRow(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "re563a", buildRFC822("re563a@test", "re", d))
			want := divergeRow(t, ha, ms, otherMailbox(t, ha, acc, "mapped563"))
			w, conn := newIngestWorker(t, ha, ts, acc)
			if _, err := conn.SelectReadWrite(context.Background(), "INBOX"); err != nil {
				t.Fatalf("select: %v", err)
			}
			if _, _, _, err := w.fetchAndIngest(context.Background(), conn, []imap.UID{uid}, "INBOX", "INBOX", true); err != nil {
				t.Fatalf("fetchAndIngest: %v", err)
			}
			assertRowUnchanged(t, ha, want)
		})
	}
}

// The All Mail envelope-dedup skip branch.
func TestReingestLinkedUID_GmailAllMailSkip_PreservesRow(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			u := ts.addUser("re563b", "pw")
			if err := u.Create(gmailAllMail, nil); err != nil {
				t.Fatalf("Create: %v", err)
			}
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email: "re563b@example.test", username: "re563b", credentialPlaintext: "pw",
			}, nil)
			d := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
			raw := buildRFC822("re563b@test", "re", d)
			appendToServer(t, ts, "re563b", "pw", "INBOX", raw, nil, d)
			amUID := appendToServer(t, ts, "re563b", "pw", gmailAllMail, raw, nil, d)
			w, conn := newIngestWorker(t, ha, ts, acc)
			ctx := context.Background()
			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("syncFolder: %v", err)
			}
			if err := w.syncFolderGmailAllMailEnvelopeDedup(ctx, conn); err != nil {
				t.Fatalf("first all-mail pass: %v", err)
			}
			ms, found, _ := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, gmailAllMail, uint32(amUID))
			if !found {
				t.Fatal("no All Mail row")
			}
			want := divergeRow(t, ha, ms, otherMailbox(t, ha, acc, "mapped563"))
			_ = ha.Store.Meta().DeleteIMAPImportFolderCursor(ctx, acc.ID, gmailAllMail)
			if err := w.syncFolderGmailAllMailEnvelopeDedup(ctx, conn); err != nil {
				t.Fatalf("second all-mail pass: %v", err)
			}
			assertRowUnchanged(t, ha, want)
		})
	}
}

// The X-GM-LABELS placement path.
func TestReingestLinkedUID_GmailLabels_PreservesRow(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
			ctx := context.Background()
			p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
				Kind: store.PrincipalKindUser, CanonicalEmail: "re563c@example.test", DisplayName: "re563c", QuotaBytes: 1 << 30,
			})
			if err != nil {
				t.Fatalf("InsertPrincipal: %v", err)
			}
			acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
				PrincipalID: p.ID, AccountName: "Gmail", Host: "imap.gmail.com", Port: 993,
				TLSMode: store.IMAPImportTLSModeImplicit, Username: "re563c@example.test",
				AuthMethod: store.IMAPImportAuthMethodPassword, CredentialCT: sealCred(t, "pw"),
				State: store.IMAPImportAccountStateEnabled,
			})
			if err != nil {
				t.Fatalf("CreateIMAPImportAccount: %v", err)
			}
			d := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			conn := &fakeLabelsConn{uidNext: 3, msgs: []labeledMsg{
				{uid: 1, raw: buildRFC822("re563c@test", "re", d), labels: []string{`\Inbox`}},
			}}
			w := newAccountWorker(accountWorkerOpts{
				account: acc, store: ha.Store, dataKey: testDataKey(t),
				log: newTestLogger(t), clk: ha.Clock, categoriser: noopCategoriser{},
			})
			folders := []folderInfo{{Name: gmailAllMail}}
			if err := w.syncAllFoldersGmailLabels(ctx, conn, folders); err != nil {
				t.Fatalf("first pass: %v", err)
			}
			ms, found, _ := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, gmailAllMail, 1)
			if !found {
				t.Fatal("no All Mail row")
			}
			want := divergeRow(t, ha, ms, otherMailbox(t, ha, acc, "mapped563"))
			_ = ha.Store.Meta().DeleteIMAPImportFolderCursor(ctx, acc.ID, gmailAllMail)
			if err := w.syncAllFoldersGmailLabels(ctx, conn, folders); err != nil {
				t.Fatalf("second pass: %v", err)
			}
			assertRowUnchanged(t, ha, want)
		})
	}
}
