package imapimport

// Sub-account transport wiring (issue #227, REQ-SUBACCT-07,
// REQ-IMAP-IMP-106/107): a running accountWorker's owning principal must
// track store.RebindIMAPImportAccountPrincipal so a live arrival for a
// just-separated account lands in the sub-account's mailbox tree, never
// in the parent's. This exercises the fix to refreshAccount /
// stateChangedFromEnabled directly against the same *accountWorker
// instance a live IDLE session would use -- not a freshly-constructed
// worker, which would trivially pick up the new principal and prove
// nothing about the cache invalidation.

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// TestSeparatedAccount_LiveArrivalLandsUnderSubPrincipal drives one
// accountWorker through: an initial sync under the parent principal, a
// store-level rebind to a sub-principal (the effect
// store.SeparateIdentity has on the account row), a refreshAccount call
// (what run()'s reconnect loop and stateChangedFromEnabled's live
// mid-IDLE-session check both do), and a second sync of a newly-arrived
// message. The new message must land under the sub-principal and must
// not appear anywhere under the parent.
func TestSeparatedAccount_LiveArrivalLandsUnderSubPrincipal(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("sep1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})
	ctx := context.Background()

	d := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	appendToServer(t, ts, "sep1", "pw", "INBOX", buildRFC822("sep-msg-1@test", "Before", d), nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "sep1@example.test",
		username:            "sep1",
		credentialPlaintext: "pw",
	}, nil)
	parentPID := acc.PrincipalID

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	before, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, parentPID, "sep-msg-1@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(before): %v", err)
	}
	if before.PrincipalID != parentPID {
		t.Fatalf("pre-separation message PrincipalID = %d; want parent %d", before.PrincipalID, parentPID)
	}

	// Simulate store.SeparateIdentity's effect on the account row: a
	// new sub-principal, rebound in place. The account's State is
	// untouched -- separation is not a state transition
	// (REQ-IMAP-IMP-90 only watches enabled/migrating/etc.).
	sub, err := ha.Store.Meta().InsertSubPrincipal(ctx, parentPID, store.Principal{
		CanonicalEmail: "sep1-sub@example.test",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	if err := ha.Store.Meta().RebindIMAPImportAccountPrincipal(ctx, acc.ID, sub.ID); err != nil {
		t.Fatalf("RebindIMAPImportAccountPrincipal: %v", err)
	}

	appendToServer(t, ts, "sep1", "pw", "INBOX", buildRFC822("sep-msg-2@test", "After", d), nil, d)

	// One worker instance carries acc.PrincipalID == parentPID from
	// construction, exactly like a worker that was already running
	// when the separation happened. w.refreshAccount is what run()'s
	// reconnect loop calls with a freshly-read row before the next
	// attempt(); stateChangedFromEnabled applies the same PrincipalID
	// refresh live, within an already-open IDLE session. Either path
	// converges on the same in-memory update, so exercising
	// refreshAccount here covers the mechanism both paths share.
	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})
	cur, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount(post-rebind): %v", err)
	}
	if cur.PrincipalID != sub.ID {
		t.Fatalf("GetIMAPImportAccount.PrincipalID = %d; want sub-principal %d", cur.PrincipalID, sub.ID)
	}
	w.refreshAccount(cur)
	if w.opts.account.PrincipalID != sub.ID {
		t.Fatalf("w.opts.account.PrincipalID after refreshAccount = %d; want sub-principal %d", w.opts.account.PrincipalID, sub.ID)
	}

	credPlaintext, err := w.openCredential(ctx, w.opts.account)
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID:           acc.ID,
		Host:                acc.Host,
		Port:                acc.Port,
		TLSMode:             string(acc.TLSMode),
		Username:            acc.Username,
		AuthMethod:          string(acc.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		conn.Logout()
		conn.Close()
	}()
	if err := w.syncAllFolders(ctx, conn); err != nil {
		t.Fatalf("syncAllFolders (post-rebind): %v", err)
	}

	after, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, sub.ID, "sep-msg-2@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader(after, sub): %v", err)
	}
	if after.PrincipalID != sub.ID {
		t.Fatalf("post-rebind live arrival PrincipalID = %d; want sub-principal %d", after.PrincipalID, sub.ID)
	}
	if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, parentPID, "sep-msg-2@test"); err == nil {
		t.Fatal("post-rebind live arrival also landed under the parent principal; want absent")
	}
}
