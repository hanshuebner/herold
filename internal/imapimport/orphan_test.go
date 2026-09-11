package imapimport

// orphan_test.go covers issue #319: an upstream \Deleted seen in the
// folder a message was originally pulled from must never strip a
// membership herold placed the message in via a DIFFERENT route (its own
// spam verdict, or a later dedup hit from another folder) -- and a
// removal that genuinely leaves the message with no real membership must
// also drop the provenance label rather than leaving a label-only orphan.
//
// Both reproduction sequences from the ticket are covered, each with and
// without a spam-verdict classifier fake, in both folder-sync orders:
//
//   - forward order: the INBOX \Deleted is processed before the Spam-
//     folder dedup hit runs (syncFolder("INBOX") then syncFolder("Spam")).
//   - reverse order: the Spam-folder dedup hit runs first, while the
//     INBOX copy still carries its membership (syncFolder("Spam") then
//     syncFolder("INBOX")).
//
// All tests run against the in-process imapmemserver (testserver_test.go).

import (
	"context"
	"errors"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// setUpOrphanTest creates a test server + user with an (initially empty)
// Spam folder, a herold account with a cached provenance label, and dials
// a Conn. Returns everything a test needs to call w.syncFolder directly in
// whatever order it wants.
func setUpOrphanTest(t *testing.T, user string, spamCl SpamClassifier) (*testIMAPServer, *testharness.Server, store.IMAPImportAccount, *accountWorker, Conn) {
	t.Helper()
	ts := startTestIMAPServer(t)
	u := ts.addUser(user, "pw")
	if err := u.Create("Junk", nil); err != nil {
		t.Fatalf("Create Junk: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               user + "@example.test",
		username:            user,
		credentialPlaintext: "pw",
	}, nil)

	ctx := context.Background()
	prov, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: acc.PrincipalID,
		Name:        acc.AccountName,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (provenance): %v", err)
	}
	if err := ha.Store.Meta().SetIMAPImportProvenanceMailbox(ctx, acc.ID, prov.ID); err != nil {
		t.Fatalf("SetIMAPImportProvenanceMailbox: %v", err)
	}
	acc, err = ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}

	if spamCl == nil {
		spamCl = noopSpamClassifier{}
	}
	w := newAccountWorker(accountWorkerOpts{
		account:        acc,
		store:          ha.Store,
		dataKey:        testDataKey(t),
		log:            newTestLogger(t),
		clk:            ha.Clock,
		dialer:         &fakeDialer{ts: ts},
		categoriser:    noopCategoriser{},
		spamClassifier: spamCl,
	})

	dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	credPlaintext, err := w.openCredential(dialCtx, acc)
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	conn, err := w.opts.dialer.Dial(dialCtx, dialParams{
		AccountID:           acc.ID,
		Host:                acc.Host,
		Port:                acc.Port,
		TLSMode:             string(acc.TLSMode),
		Username:            acc.Username,
		AuthMethod:          string(acc.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		conn.Logout()
		conn.Close()
	})
	return ts, ha, acc, w, conn
}

// setDeletedOnServer flags uid \Deleted on mailbox without expunging it,
// simulating an upstream mail filter (e.g. imap-cleaner) that marks a
// message for deletion without herold's involvement. Mirrors
// expungeOnServer (junk_expunge_test.go) minus the EXPUNGE step.
func setDeletedOnServer(t *testing.T, ts *testIMAPServer, user, password, mailbox string, uid imap.UID) {
	t.Helper()
	client := dialRawTestClient(t, ts, user, password)
	defer client.Close()
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("setDeletedOnServer: SELECT: %v", err)
	}
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	sf := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if err := client.Store(uidSet, sf, nil).Close(); err != nil {
		t.Fatalf("setDeletedOnServer: STORE: %v", err)
	}
}

// hasName reports whether names contains want.
func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// runOrphanScenario drives the shared #319 reproduction: a message
// arrives in INBOX (twice-synced so the folder is already-initialised and
// spam classification, if configured, is live), is copied upstream to
// Spam and flagged \Deleted in INBOX, and the two affected folders are
// then synced in either order. Asserts the final state is the message
// present in exactly the Junk-attributed "Spam" mailbox plus the
// provenance label -- never label-only, and never missing the Junk
// membership.
func runOrphanScenario(t *testing.T, user string, spamCl SpamClassifier, reverseOrder bool) {
	t.Helper()
	ts, ha, acc, w, conn := setUpOrphanTest(t, user, spamCl)
	ctx := context.Background()

	// Establish both folders as already-initialised (folderInitialised
	// gates the spam classifier and matches the ticket's "the INBOX pull
	// classified the message as spam" framing -- a live arrival on an
	// account that has already synced INBOX before, not its very first
	// backfill).
	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("prime INBOX sync: %v", err)
	}
	if err := w.syncFolder(ctx, conn, "Junk", "Junk"); err != nil {
		t.Fatalf("prime Junk sync: %v", err)
	}

	d := time.Date(2025, 9, 5, 10, 0, 0, 0, time.UTC)
	msgID := "orphan-" + user + "@test"
	raw := buildRFC822(msgID, "Orphan", d)
	inboxUID := appendToServer(t, ts, user, "pw", "INBOX", raw, nil, d)

	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("INBOX pull: %v", err)
	}

	// Upstream copies the message to Junk and flags the INBOX copy
	// \Deleted (the imap-cleaner pattern), without expunging it.
	appendToServer(t, ts, user, "pw", "Junk", raw, nil, d)
	setDeletedOnServer(t, ts, user, "pw", "INBOX", inboxUID)

	if reverseOrder {
		if err := w.syncFolder(ctx, conn, "Junk", "Junk"); err != nil {
			t.Fatalf("Junk sync: %v", err)
		}
		if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
			t.Fatalf("INBOX sync: %v", err)
		}
	} else {
		if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
			t.Fatalf("INBOX sync: %v", err)
		}
		if err := w.syncFolder(ctx, conn, "Junk", "Junk"); err != nil {
			t.Fatalf("Junk sync: %v", err)
		}
	}

	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	names := messageMailboxNames(t, ha.Store, acc.PrincipalID, msg)
	if !hasName(names, "Junk") {
		t.Errorf("mailboxes = %v; want the Junk-attributed Junk membership present", names)
	}
	if hasName(names, "INBOX") {
		t.Errorf("mailboxes = %v; want no INBOX membership (source folder \\Deleted)", names)
	}
	if !hasName(names, acc.AccountName) {
		t.Errorf("mailboxes = %v; want the provenance label present (message is not label-only orphan)", names)
	}
	if len(names) != 2 {
		t.Errorf("mailboxes = %v; want exactly [Junk, %s]", names, acc.AccountName)
	}
}

func TestOrphan_ForwardOrder_NoVerdict(t *testing.T) {
	runOrphanScenario(t, "orph1", nil, false)
}

func TestOrphan_ReverseOrder_NoVerdict(t *testing.T) {
	runOrphanScenario(t, "orph2", nil, true)
}

func TestOrphan_ForwardOrder_SpamVerdict(t *testing.T) {
	runOrphanScenario(t, "orph3", &fakeSpamClassifier{
		verdicts: []spam.Classification{{Verdict: spam.Spam, Score: 0.99}},
	}, false)
}

func TestOrphan_ReverseOrder_SpamVerdict(t *testing.T) {
	runOrphanScenario(t, "orph4", &fakeSpamClassifier{
		verdicts: []spam.Classification{{Verdict: spam.Spam, Score: 0.99}},
	}, true)
}

// TestAlreadyDeletedOnFirstSightNotCreated is requirement 4: a message
// that is already \Deleted the first time INBOX is synced is never
// created in herold at all.
func TestAlreadyDeletedOnFirstSightNotCreated(t *testing.T) {
	ts, ha, acc, w, conn := setUpOrphanTest(t, "orph5", nil)
	ctx := context.Background()

	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("prime INBOX sync: %v", err)
	}

	d := time.Date(2025, 9, 6, 10, 0, 0, 0, time.UTC)
	raw := buildRFC822("already-deleted@test", "Already Deleted", d)
	appendToServer(t, ts, "orph5", "pw", "INBOX", raw, []imap.Flag{imap.FlagDeleted}, d)

	if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
		t.Fatalf("INBOX sync: %v", err)
	}

	if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "already-deleted@test"); err == nil {
		t.Error("message was created despite being \\Deleted on first sight")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessageByMessageIDHeader: unexpected error: %v", err)
	}
}
