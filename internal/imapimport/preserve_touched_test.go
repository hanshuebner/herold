package imapimport

// preserve_touched_test.go covers the #487 preservation rule for
// dropOrphanedProvenanceLabel: an upstream expunge that would leave a
// mirrored message with only its provenance label preserves the message
// into Archive when the user has "touched" it in herold -- answered by a
// herold-authored reply already in its thread -- and lets it follow the
// delete as before when the message carries only INBOX plus the
// provenance label and nothing else.
//
// All tests run against the in-process imapmemserver (testserver_test.go).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// setUpPreserveTest is setUpOrphanTest plus a "Sent" upstream folder, used to
// mirror a herold-authored reply the same way an IMAP account with
// delete_propagates would (re #486: the reply was itself mirrored back from
// the account's own Sent folder). be selects the store backend
// (ownSentDedupBackends, sync_test.go): sqlite always, postgres when
// HEROLD_PG_DSN is set, since the preservation rule is a store-touching
// change.
func setUpPreserveTest(t *testing.T, user string, be ownSentDedupBackend) (*testIMAPServer, *testharness.Server, store.IMAPImportAccount, *accountWorker, Conn) {
	t.Helper()
	ts := startTestIMAPServer(t)
	u := ts.addUser(user, "pw")
	if err := u.Create("Sent", nil); err != nil {
		t.Fatalf("Create Sent: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})

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

	w := newAccountWorker(accountWorkerOpts{
		account:        acc,
		store:          ha.Store,
		dataKey:        testDataKey(t),
		log:            newTestLogger(t),
		clk:            ha.Clock,
		dialer:         &fakeDialer{ts: ts},
		categoriser:    noopCategoriser{},
		spamClassifier: noopSpamClassifier{},
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

// TestUpstreamExpunge_UntouchedMessage_Deleted is the #487 "follows the
// upstream delete as before" half of the rule: a message with only INBOX
// plus the provenance label, and no reply in its thread, is deleted when its
// INBOX copy is expunged upstream -- unchanged from the pre-#487 behaviour.
func TestUpstreamExpunge_UntouchedMessage_Deleted(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts, ha, acc, w, conn := setUpPreserveTest(t, "untouched1", be)
			ctx := context.Background()

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("prime INBOX sync: %v", err)
			}

			d := time.Date(2025, 9, 6, 9, 0, 0, 0, time.UTC)
			msgID := "untouched-msg@test"
			raw := buildRFC822(msgID, "Untouched", d)
			inboxUID := appendToServer(t, ts, "untouched1", "pw", "INBOX", raw, nil, d)

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("INBOX pull: %v", err)
			}

			msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgID)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader: %v", err)
			}
			if len(msg.Mailboxes) != 2 {
				t.Fatalf("after INBOX pull: %d memberships; want 2 (INBOX, provenance)", len(msg.Mailboxes))
			}

			expungeOnServer(t, ts, "untouched1", "pw", "INBOX", inboxUID)

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("post-expunge sync: %v", err)
			}

			if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgID); err == nil {
				t.Fatalf("message survived an upstream expunge with no other membership and no reply; want it deleted")
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetMessageByMessageIDHeader: unexpected error: %v", err)
			}
		})
	}
}

// TestUpstreamExpunge_RepliedMessage_MovesToArchive is the #486/#487
// reproduction: an upstream expunge of a message that carries only INBOX
// plus the provenance label, but whose thread already has a herold-authored
// reply (mirrored back from the account's own Sent folder, so it carries a
// Sent-role membership per MessageIsPrincipalSent), preserves the message
// into Archive with its provenance label kept instead of deleting it.
func TestUpstreamExpunge_RepliedMessage_MovesToArchive(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts, ha, acc, w, conn := setUpPreserveTest(t, "replied1", be)
			ctx := context.Background()

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("prime INBOX sync: %v", err)
			}
			if err := w.syncFolder(ctx, conn, "Sent", "Sent"); err != nil {
				t.Fatalf("prime Sent sync: %v", err)
			}

			d := time.Date(2026, 8, 14, 9, 41, 21, 0, time.UTC)
			origMsgID := "orig-msg@test"
			raw := buildRFC822(origMsgID, "Terminvorschlag", d)
			inboxUID := appendToServer(t, ts, "replied1", "pw", "INBOX", raw, nil, d)

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("INBOX pull: %v", err)
			}

			orig, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, origMsgID)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader (orig): %v", err)
			}
			if len(orig.Mailboxes) != 2 {
				t.Fatalf("after INBOX pull: %d memberships; want 2 (INBOX, provenance)", len(orig.Mailboxes))
			}

			replyMsgID := "reply-msg@test"
			replyRaw := buildRFC822WithInReplyTo(replyMsgID, "Re: Terminvorschlag", d.Add(3*time.Minute), origMsgID)
			appendToServer(t, ts, "replied1", "pw", "Sent", replyRaw, nil, d.Add(3*time.Minute))

			if err := w.syncFolder(ctx, conn, "Sent", "Sent"); err != nil {
				t.Fatalf("Sent pull: %v", err)
			}

			reply, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, replyMsgID)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader (reply): %v", err)
			}
			if !MessageIsPrincipalSent(ctx, ha.Store, acc.PrincipalID, reply) {
				t.Fatalf("reply is not recognised as principal-sent; the touched check depends on this")
			}
			if !ThreadHasPrincipalSentMember(ctx, ha.Store, acc.PrincipalID, orig) {
				t.Fatalf("orig's thread is not recognised as having a principal-sent reply; the touched check depends on this")
			}

			// Simulate the upstream expunging its INBOX copy of the
			// original, the way #486's vorsitz@classic-computing.de mirror
			// observed on 22 Aug.
			expungeOnServer(t, ts, "replied1", "pw", "INBOX", inboxUID)

			if err := w.syncFolder(ctx, conn, "INBOX", "INBOX"); err != nil {
				t.Fatalf("post-expunge sync: %v", err)
			}

			orig2, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, origMsgID)
			if err != nil {
				t.Fatalf("original was deleted by the upstream expunge; want it preserved into Archive: %v", err)
			}
			names := messageMailboxNames(t, ha.Store, acc.PrincipalID, orig2)
			if !hasName(names, "Archive") {
				t.Errorf("mailboxes = %v; want the Archive membership added", names)
			}
			if !hasName(names, acc.AccountName) {
				t.Errorf("mailboxes = %v; want the provenance label kept", names)
			}
			if hasName(names, "INBOX") {
				t.Errorf("mailboxes = %v; want no INBOX membership (source folder expunged)", names)
			}
			if len(names) != 2 {
				t.Errorf("mailboxes = %v; want exactly [Archive, %s]", names, acc.AccountName)
			}
		})
	}
}
