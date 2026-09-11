package imapimport

// seenbackfill_test.go covers the once-per-worker-lifetime maintenance pass
// (seenbackfill.go) that corrects existing rows minted before the re #316
// $seen fixes. The fixtures are built directly through the store API
// (InsertMessage / AddMessageToMailbox / UpsertIMAPImportMessageState with
// flags=0 throughout), bypassing ingestMessage entirely, so they reproduce
// exactly the row shape the PRE-FIX code would have left on disk -- an
// unseen \Sent-role membership, an unseen provenance-label membership, and
// a LastSyncedFlags baseline with no \Seen bit.

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// TestSeenBackfillCorrectsPreExistingRow seeds a pre-existing unseen \Sent
// membership plus an unseen provenance-label membership and a stale
// (unseen) LastSyncedFlags baseline, runs the backfill once, and asserts
// every one of those three is corrected.
func TestSeenBackfillCorrectsPreExistingRow(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("sb1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "sb1@example.test",
		username:            "sb1",
		credentialPlaintext: "pw",
	}, nil)

	ctx := context.Background()

	// Provenance label, wired the way account-enable does in production.
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

	// A \Sent-role mailbox, matching what ensureMailbox would have created.
	sent, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: acc.PrincipalID,
		Name:        "Sent",
		Attributes:  store.MailboxAttrSent,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (Sent): %v", err)
	}

	// The pre-fix row shape: an unseen message inserted directly into Sent
	// (bypassing insertNewMessage's $seen force), no provenance membership
	// yet.
	blob, err := ha.Store.Blobs().Put(ctx, strings.NewReader("body of pre-fix-sent@test"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: acc.PrincipalID,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "pre-fix own-sent", MessageID: "pre-fix-sent@test"},
	}, []store.MessageMailbox{{MailboxID: sent.ID, Flags: 0}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "pre-fix-sent@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}

	// The pre-fix provenance-label membership: AddMessageToMailbox always
	// starts a fresh membership unseen (see addProvenanceLabel's doc
	// comment) -- exactly what the OLD addProvenanceLabel (no mirror) left
	// in place.
	if _, _, err := ha.Store.Meta().AddMessageToMailbox(ctx, msg.ID, prov.ID); err != nil {
		t.Fatalf("AddMessageToMailbox (provenance): %v", err)
	}

	// The pre-fix message_state row: LastSyncedFlags recorded from the raw
	// (unseen) upstream flags, matching the OLD fetchAndIngest.
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "Sent",
		UpstreamUID:     1,
		HeroldMessageID: msg.ID,
		HeroldMailboxID: sent.ID,
		MappedMailboxID: sent.ID,
		LastSyncedFlags: 0,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}

	// Precondition: both memberships unseen.
	before, err := ha.Store.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage (precondition): %v", err)
	}
	if len(before.Mailboxes) != 2 {
		t.Fatalf("precondition: want 2 memberships, got %d: %+v", len(before.Mailboxes), before.Mailboxes)
	}
	for _, mm := range before.Mailboxes {
		if mm.Flags&store.MessageFlagSeen != 0 {
			t.Fatalf("precondition: membership in mailbox %d is already $seen", mm.MailboxID)
		}
	}

	w := newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		log:     newTestLogger(t),
		clk:     ha.Clock,
		dialer:  &fakeDialer{ts: ts},
	})

	w.runSeenBackfill(ctx)

	after, err := ha.Store.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage (after): %v", err)
	}
	for _, mm := range after.Mailboxes {
		if mm.Flags&store.MessageFlagSeen == 0 {
			t.Errorf("membership in mailbox %d still not $seen after backfill", mm.MailboxID)
		}
	}

	msState, found, err := ha.Store.Meta().GetIMAPImportMessageState(ctx, acc.ID, "Sent", 1)
	if err != nil || !found {
		t.Fatalf("GetIMAPImportMessageState: found=%v err=%v", found, err)
	}
	if !msState.LastSyncedFlags.HasSeen() {
		t.Error("LastSyncedFlags should record \\Seen after the backfill (re #316 durability)")
	}

	// Idempotent: running it again (bypassing the once-per-lifetime guard,
	// as a fresh session would after a restart) makes no further writes and
	// leaves the corrected state exactly as it was.
	w.seenBackfillDone = false
	w.runSeenBackfill(ctx)
	after2, err := ha.Store.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage (after second pass): %v", err)
	}
	for _, mm := range after2.Mailboxes {
		if mm.Flags&store.MessageFlagSeen == 0 {
			t.Errorf("second backfill pass regressed membership in mailbox %d", mm.MailboxID)
		}
	}
}

// TestSeenBackfillLeavesGenuinelyUnreadMailAlone verifies the backfill does
// not touch a non-Sent-role message that has never been $seen anywhere --
// it must not manufacture a false "read" state for genuinely unread mail.
func TestSeenBackfillLeavesGenuinelyUnreadMailAlone(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("sb2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "sb2@example.test",
		username:            "sb2",
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

	inbox, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: acc.PrincipalID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox (INBOX): %v", err)
	}

	blob, err := ha.Store.Blobs().Put(ctx, strings.NewReader("body of unread@test"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: acc.PrincipalID,
		Size:        blob.Size,
		Blob:        blob,
		Envelope:    store.Envelope{Subject: "genuinely unread", MessageID: "unread@test"},
	}, []store.MessageMailbox{{MailboxID: inbox.ID, Flags: 0}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "unread@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if _, _, err := ha.Store.Meta().AddMessageToMailbox(ctx, msg.ID, prov.ID); err != nil {
		t.Fatalf("AddMessageToMailbox (provenance): %v", err)
	}
	if err := ha.Store.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     1,
		HeroldMessageID: msg.ID,
		HeroldMailboxID: inbox.ID,
		MappedMailboxID: inbox.ID,
		LastSyncedFlags: 0,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}

	w := newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		log:     newTestLogger(t),
		clk:     ha.Clock,
		dialer:  &fakeDialer{ts: ts},
	})
	w.runSeenBackfill(ctx)

	after, err := ha.Store.Meta().GetMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	for _, mm := range after.Mailboxes {
		if mm.Flags&store.MessageFlagSeen != 0 {
			t.Errorf("backfill marked a genuinely unread INBOX message $seen in mailbox %d", mm.MailboxID)
		}
	}
}
