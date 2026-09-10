package imapimport

// junk_expunge_test.go covers three download-path fixes for issue #303
// ("IMAP import mirrors upstream spam into INBOX"):
//
//   - Junk wins over inbox: a message the import finds in a source folder
//     mapped to a Junk-attributed herold mailbox does not receive (or keeps)
//     an INBOX membership.
//   - \Deleted upstream: a message flagged \Deleted in a source folder is not
//     imported from that folder, and an upstream EXPUNGE of a previously-
//     mirrored message drops that folder's membership on the next sync.
//   - excluded_folders: a per-account no-sync folder list creates no cursor
//     and no message_state rows for the excluded folder (#305).
//
// All tests run against the in-process imapmemserver (testserver_test.go).

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// expungeOnServer flags uid \Deleted and expunges it from mailbox via a
// fresh raw connection, simulating an upstream-side deletion outside of
// herold's control.
func expungeOnServer(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID) {
	t.Helper()
	client := dialRawTestClient(t, ts, user, password)
	defer client.Close()
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("expungeOnServer: SELECT: %v", err)
	}
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	sf := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if err := client.Store(uidSet, sf, nil).Close(); err != nil {
		t.Fatalf("expungeOnServer: STORE: %v", err)
	}
	if client.Caps().Has(imap.CapUIDPlus) {
		if err := client.UIDExpunge(uidSet).Close(); err != nil {
			t.Fatalf("expungeOnServer: UID EXPUNGE: %v", err)
		}
		return
	}
	if err := client.Expunge().Close(); err != nil {
		t.Fatalf("expungeOnServer: EXPUNGE: %v", err)
	}
}

// dialRawTestClient opens a logged-in imapclient.Client against ts, bypassing
// the accountWorker/Conn seam. Used by test helpers that need to mutate
// upstream state directly (STORE \Deleted, EXPUNGE) the way an upstream mail
// filter would, independent of herold's own write-back path.
func dialRawTestClient(t *testing.T, ts *testIMAPServer, user, password string) *imapclient.Client {
	t.Helper()
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{
			RootCAs:    ts.ClientTLSConfig.RootCAs,
			ServerName: "127.0.0.1",
		},
	}
	client, err := imapclient.DialTLS(ts.ImplicitAddr, opts)
	if err != nil {
		t.Fatalf("dialRawTestClient: dial: %v", err)
	}
	if err := client.Login(user, password).Wait(); err != nil {
		client.Close()
		t.Fatalf("dialRawTestClient: LOGIN: %v", err)
	}
	return client
}

// messageMailboxNames returns the herold mailbox names msg currently belongs
// to, for assertion messages.
func messageMailboxNames(t *testing.T, s store.Store, pid store.PrincipalID, msg store.Message) []string {
	t.Helper()
	mbs, err := s.Meta().ListMailboxes(context.Background(), pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	byID := make(map[store.MailboxID]string, len(mbs))
	for _, mb := range mbs {
		byID[mb.ID] = mb.Name
	}
	var out []string
	for _, mm := range msg.Mailboxes {
		out = append(out, byID[mm.MailboxID])
	}
	return out
}

// TestJunkWinsOverInboxNoDelete verifies the junk-wins precedence in
// isolation from the \Deleted-skip path: a message present in both INBOX and
// Spam (neither flagged \Deleted) ends up in Junk only, with no INBOX
// membership, regardless of INBOX being the first-synced folder (default
// LIST order puts INBOX before Spam here). Priority-1 behaviour of #303.
func TestJunkWinsOverInboxNoDelete(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("jw1", "pw")
	if err := u.Create("Spam", nil); err != nil {
		t.Fatalf("Create Spam: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 8, 1, 9, 0, 0, 0, time.UTC)
	raw := buildRFC822("junkwins-1@test", "Junk Wins", d)
	appendToServer(t, ts, "jw1", "pw", "INBOX", raw, nil, d)
	appendToServer(t, ts, "jw1", "pw", "Spam", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "jw1@example.test",
		username:            "jw1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "junkwins-1@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	names := messageMailboxNames(t, ha.Store, acc.PrincipalID, msg)
	if len(names) != 1 || names[0] != "Spam" {
		t.Errorf("message mailboxes = %v; want exactly [Spam] (junk wins over inbox)", names)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 0 {
		t.Errorf("herold INBOX has %d messages; want 0 (junk wins)", got)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Spam"); got != 1 {
		t.Errorf("herold Spam has %d messages; want 1", got)
	}
}

// TestUpstreamDeletedFlagSkipsImport is the acceptance scenario from #303: a
// message present in INBOX (flagged \Deleted, not expunged — the imap-cleaner
// pattern) and in Spam yields a herold message in Junk only. The INBOX copy
// is never imported in the first place because of the \Deleted flag, so this
// exercises the \Deleted-skip path rather than the junk-wins path.
func TestUpstreamDeletedFlagSkipsImport(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("del1", "pw")
	if err := u.Create("Spam", nil); err != nil {
		t.Fatalf("Create Spam: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 8, 2, 9, 0, 0, 0, time.UTC)
	raw := buildRFC822("deleted-skip@test", "Deleted Skip", d)
	inboxUID := appendToServer(t, ts, "del1", "pw", "INBOX", raw, []imap.Flag{imap.FlagDeleted}, d)
	appendToServer(t, ts, "del1", "pw", "Spam", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "del1@example.test",
		username:            "del1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "deleted-skip@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	names := messageMailboxNames(t, ha.Store, acc.PrincipalID, msg)
	if len(names) != 1 || names[0] != "Spam" {
		t.Errorf("message mailboxes = %v; want exactly [Spam]", names)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 0 {
		t.Errorf("herold INBOX has %d messages; want 0 (\\Deleted source skipped)", got)
	}

	// No message_state row should exist for the \Deleted INBOX copy: it was
	// never imported from that folder.
	if _, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(inboxUID)); err != nil {
		t.Fatalf("GetIMAPImportMessageState: %v", err)
	} else if found {
		t.Error("message_state row exists for the \\Deleted INBOX copy; want none")
	}
}

// TestUpstreamExpungeDropsMembership verifies mirroring an upstream EXPUNGE:
// a message mirrored from two folders (INBOX and a plain, non-Junk "Archive"
// folder — chosen to isolate expunge handling from the junk-wins path) loses
// only the expunged folder's membership on the next sync; the message and
// its Archive membership survive because another membership remains.
func TestUpstreamExpungeDropsMembership(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("exp1", "pw")
	if err := u.Create("Archive", nil); err != nil {
		t.Fatalf("Create Archive: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 8, 3, 9, 0, 0, 0, time.UTC)
	raw := buildRFC822("expunge-drop@test", "Expunge Drop", d)
	inboxUID := appendToServer(t, ts, "exp1", "pw", "INBOX", raw, nil, d)
	appendToServer(t, ts, "exp1", "pw", "Archive", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "exp1@example.test",
		username:            "exp1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	ctx := context.Background()
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "expunge-drop@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (first sync): %v", err)
	}
	if len(msg.Mailboxes) != 2 {
		t.Fatalf("after first sync: %d memberships; want 2 (INBOX, Archive)", len(msg.Mailboxes))
	}

	// Simulate the upstream expunging its INBOX copy, without herold's
	// involvement (e.g. the user deleted it from a different mail client).
	expungeOnServer(t, ts, "exp1", "pw", "INBOX", inboxUID)

	// A forward-sync-only re-run would never look at this already-mirrored
	// UID again; force the incremental (down-sync-eligible) path by leaving
	// high_water as-is — it already reflects a completed initial sync — and
	// running another pass.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync (post-expunge): %v", err)
	}

	msg2, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "expunge-drop@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (post-expunge): %v", err)
	}
	names := messageMailboxNames(t, ha.Store, acc.PrincipalID, msg2)
	if len(names) != 1 || names[0] != "Archive" {
		t.Errorf("post-expunge mailboxes = %v; want exactly [Archive] (INBOX membership dropped, message kept)", names)
	}

	if _, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(inboxUID)); err != nil {
		t.Fatalf("GetIMAPImportMessageState: %v", err)
	} else if found {
		t.Error("message_state row still exists for the expunged INBOX UID; want it deleted")
	}
}

// TestExcludedFolderNoStateRows verifies per-account folder exclusion
// (#303/#305): an excluded upstream folder is never synced, so it creates no
// cursor row and no message_state rows, and its mail is never mirrored.
func TestExcludedFolderNoStateRows(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("excl1", "pw")
	if err := u.Create("Promo", nil); err != nil {
		t.Fatalf("Create Promo: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 8, 4, 9, 0, 0, 0, time.UTC)
	raw := buildRFC822("excluded@test", "Excluded", d)
	appendToServer(t, ts, "excl1", "pw", "Promo", raw, nil, d)
	// A normal INBOX message to confirm the account still syncs otherwise.
	raw2 := buildRFC822("not-excluded@test", "Not Excluded", d)
	appendToServer(t, ts, "excl1", "pw", "INBOX", raw2, nil, d)

	ctx := context.Background()
	p, err := ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "excl1@example.test",
		DisplayName:    "excl1@example.test",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	h, port := parseAddr(ts.ImplicitAddr)
	acc, err := ha.Store.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "Test",
		Host:             h,
		Port:             port,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "excl1",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     sealCred(t, "pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
		ExcludedFolders:  []string{"Promo"},
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The excluded folder's mail was never mirrored.
	if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "excluded@test"); err == nil {
		t.Error("excluded folder's message was imported; want it skipped entirely")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessageByMessageIDHeader (excluded): unexpected error: %v", err)
	}

	// No cursor row for the excluded folder.
	if _, found, err := ha.Store.Meta().GetIMAPImportFolderCursor(ctx, acc.ID, "Promo"); err != nil {
		t.Fatalf("GetIMAPImportFolderCursor: %v", err)
	} else if found {
		t.Error("cursor row exists for excluded folder Promo; want none")
	}

	// No message_state rows for the excluded folder.
	states, err := ha.Store.Meta().ListIMAPImportMessageStatesByFolder(ctx, acc.ID, "Promo")
	if err != nil {
		t.Fatalf("ListIMAPImportMessageStatesByFolder: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("%d message_state rows for excluded folder Promo; want 0", len(states))
	}

	// The account still syncs its non-excluded folders normally.
	if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "not-excluded@test"); err != nil {
		t.Errorf("non-excluded INBOX message was not imported: %v", err)
	}
}
