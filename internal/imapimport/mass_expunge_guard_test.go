package imapimport

// mass_expunge_guard_test.go covers the #487 reconcile guard: a reconnect
// followed by a suspiciously empty or partial UID SEARCH ALL result must not
// be treated as a genuine mass upstream expunge, reproducing the 2026-09-15
// incident's shape (reconnect, then a sync round with messages_fetched 0
// that removed 731 tracked messages in one pass). The test server has no way
// to make a live upstream lie about its own contents, so a lyingSearchConn
// wraps the real fake-dialer Conn and overrides UIDSearchSince to return a
// forced (empty) result while every other call still reaches the real
// in-process IMAP server.
//
// Both tests run on sqlite always and postgres when HEROLD_PG_DSN is set
// (ownSentDedupBackends, sync_test.go), since the guard's decision to
// remove or keep a mailbox membership is a store-touching change.

import (
	"context"
	"fmt"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// lyingSearchConn wraps a Conn and forces UIDSearchSince to return an empty
// result (regardless of the real upstream state) when forceEmpty is set,
// simulating a truncated or empty SEARCH response after a reconnect. Every
// other method delegates to the embedded Conn unchanged.
type lyingSearchConn struct {
	Conn
	forceEmpty bool
}

func (c *lyingSearchConn) UIDSearchSince(ctx context.Context, since time.Time) ([]imap.UID, error) {
	if c.forceEmpty {
		return nil, nil
	}
	return c.Conn.UIDSearchSince(ctx, since)
}

// TestMassExpungeGuardDefersThenConfirms seeds a folder well above
// massExpungeGuardMinTracked, then drives two more sync passes on the same
// worker whose UID SEARCH ALL is forced empty both times. The first suspect
// pass must defer (nothing removed, one guard_declined system event); the
// second, corroborating pass must let the reconcile through.
func TestMassExpungeGuardDefersThenConfirms(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			ts.addUser("guard1", "pw")
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})

			const n = 30
			d := time.Date(2025, 9, 10, 9, 0, 0, 0, time.UTC)
			for i := 0; i < n; i++ {
				raw := buildRFC822(fmt.Sprintf("guard-%d@test", i), fmt.Sprintf("Guard %d", i), d)
				appendToServer(t, ts, "guard1", "pw", "INBOX", raw, nil, d)
			}

			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "guard1@example.test",
				username:            "guard1",
				credentialPlaintext: "pw",
			}, nil)

			// Seed message_state rows for all n messages via a normal initial
			// sync pass (a throwaway worker; the guard is not exercised on the
			// initial backfill path).
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			ctx := context.Background()
			if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != n {
				t.Fatalf("after initial sync: INBOX has %d messages; want %d", got, n)
			}

			// A single persistent worker drives the next two passes so its
			// per-folder streak counter survives between them, whether that
			// represents two IDLE-wake rounds on one connection or two
			// sessions of the same long-lived worker after a reconnect.
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

			dial := func() Conn {
				t.Helper()
				credPlaintext, err := w.openCredential(ctx, acc)
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
					t.Fatalf("dial: %v", err)
				}
				return conn
			}

			// Pass 2 (suspect): the forward-sync UID SEARCH ALL is forced to
			// return no UIDs at all, though the upstream folder still holds
			// every message -- the 2026-09-15 incident's "reconnected, sync
			// round with messages_fetched 0" shape.
			conn2 := &lyingSearchConn{Conn: dial(), forceEmpty: true}
			if err := w.syncAllFolders(ctx, conn2); err != nil {
				t.Fatalf("pass 2 (suspect): %v", err)
			}
			conn2.Logout()
			conn2.Close()

			if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != n {
				t.Fatalf("after pass 2 (guard should defer): INBOX has %d messages; want %d unchanged", got, n)
			}
			events, err := ha.Store.Meta().ListSystemEvents(ctx, store.SystemEventFilter{Action: "imapimport.reconcile.guard_declined"})
			if err != nil {
				t.Fatalf("ListSystemEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("guard_declined events after pass 2 = %d; want 1", len(events))
			}
			if got := events[0].Metadata["tracked_count"]; got != fmt.Sprintf("%d", n) {
				t.Errorf("guard_declined tracked_count = %q; want %d", got, n)
			}
			if got := events[0].Metadata["missing_count"]; got != fmt.Sprintf("%d", n) {
				t.Errorf("guard_declined missing_count = %q; want %d", got, n)
			}

			// Pass 3 (confirming): the same lying result again. Two
			// consecutive passes now agree the folder is empty, which the
			// guard treats as confirmation and lets through.
			conn3 := &lyingSearchConn{Conn: dial(), forceEmpty: true}
			if err := w.syncAllFolders(ctx, conn3); err != nil {
				t.Fatalf("pass 3 (confirm): %v", err)
			}
			conn3.Logout()
			conn3.Close()

			if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 0 {
				t.Fatalf("after pass 3 (guard should confirm and reconcile): INBOX has %d messages; want 0", got)
			}
			events, err = ha.Store.Meta().ListSystemEvents(ctx, store.SystemEventFilter{Action: "imapimport.reconcile.guard_declined"})
			if err != nil {
				t.Fatalf("ListSystemEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("guard_declined events after pass 3 = %d; want still 1 (the confirming pass does not defer again)", len(events))
			}
		})
	}
}

// TestMassExpungeGuardBelowFloorReconcilesImmediately verifies that a folder
// with fewer than massExpungeGuardMinTracked tracked rows reconciles a
// genuine (non-lying) upstream expunge on the very next pass, unchanged from
// the pre-#487 behaviour -- the guard must never delay an ordinary
// single-message expunge in a small folder.
func TestMassExpungeGuardBelowFloorReconcilesImmediately(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ts := startTestIMAPServer(t)
			u := ts.addUser("smallfolder1", "pw")
			if err := u.Create("Projects", nil); err != nil {
				t.Fatalf("Create Projects: %v", err)
			}
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})

			d := time.Date(2025, 9, 11, 9, 0, 0, 0, time.UTC)
			raw := buildRFC822("small-1@test", "Small", d)
			uid := appendToServer(t, ts, "smallfolder1", "pw", "Projects", raw, nil, d)

			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               "smallfolder1@example.test",
				username:            "smallfolder1",
				credentialPlaintext: "pw",
			}, nil)

			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("initial sync: %v", err)
			}
			if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Projects"); got != 1 {
				t.Fatalf("after initial sync: Projects has %d messages; want 1", got)
			}

			expungeOnServer(t, ts, "smallfolder1", "pw", "Projects", uid)

			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("post-expunge sync: %v", err)
			}
			if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Projects"); got != 0 {
				t.Fatalf("after post-expunge sync: Projects has %d messages; want 0 (guard must not defer a small folder)", got)
			}
		})
	}
}
