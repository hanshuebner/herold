package imapimport

// writeback_identity_test.go pins the positive-identity requirement of #561:
// writeBackDestroy expunges the upstream message at a state row's
// (folder, uid) only when that message is provably the one that was deleted
// in herold.

import (
	"context"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// TestWriteBackDestroy_UnrelatedUpstreamMessage_NoExpunge: a deleted herold
// message whose non-divergent, below-UIDNEXT state row names an upstream
// message that no live herold message carries (never mirrored, or its herold
// copy already gone) must not expunge that upstream message.
func TestWriteBackDestroy_UnrelatedUpstreamMessage_NoExpunge(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newMoveFixture(t, be, "wb561a", true)
			ctx := context.Background()
			meta := f.ha.Store.Meta()

			stranger, found, err := meta.GetIMAPImportMessageState(ctx, f.acc.ID, "INBOX", 1)
			if err != nil || !found {
				t.Fatalf("setup: stranger state row: found=%v err=%v", found, err)
			}
			rows, _ := meta.ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
			for _, r := range rows {
				_ = meta.DeleteIMAPImportMessageState(ctx, r.AccountID, r.UpstreamFolder, r.UpstreamUID)
			}
			// The row names INBOX uid 1, below the folder's UIDNEXT.
			if err := meta.UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
				AccountID: f.acc.ID, UpstreamFolder: "INBOX", UpstreamUID: 1,
				HeroldMessageID: f.msgID, HeroldMailboxID: f.inbox, MappedMailboxID: f.inbox,
			}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			// No live herold message carries upstream INBOX uid 1's Message-ID.
			if err := meta.ExpungeMessages(ctx, stranger.HeroldMailboxID, []store.MessageID{stranger.HeroldMessageID}); err != nil {
				t.Fatalf("expunge stranger: %v", err)
			}
			if err := meta.ExpungeMessages(ctx, f.archive, []store.MessageID{f.msgID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, f.ha, f.ts, f.acc)

			assertUpstreamIntact(t, f, "INBOX", 1)
		})
	}
}

// mirrorOne mirrors one upstream message (raw, in INBOX) and returns its
// upstream UID and state row.
func mirrorOne(t *testing.T, be ownSentDedupBackend, user string, raw []byte) (*testharness.Server, *testIMAPServer, store.IMAPImportAccount, imap.UID, store.IMAPImportMessageState) {
	t.Helper()
	ts := startTestIMAPServer(t)
	ts.addUser(user, "pw")
	ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               user + "@example.test",
		username:            user,
		credentialPlaintext: "pw",
	}, nil)
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	uid := appendToServer(t, ts, user, "pw", "INBOX", raw, nil, d)
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	ms, found, err := ha.Store.Meta().GetIMAPImportMessageState(context.Background(), acc.ID, "INBOX", uint32(uid))
	if err != nil || !found {
		t.Fatalf("state row: found=%v err=%v", found, err)
	}
	return ha, ts, acc, uid, ms
}

// TestWriteBackDestroy_EmptyUpstreamMessageID_NoExpunge: an upstream message
// without a Message-ID cannot be identified, so deleting its herold copy does
// not expunge it.
func TestWriteBackDestroy_EmptyUpstreamMessageID_NoExpunge(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561b", buildRFC822NoMsgID("no id", d))
			if err := ha.Store.Meta().ExpungeMessages(context.Background(), ms.HeroldMailboxID, []store.MessageID{ms.HeroldMessageID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, ha, ts, acc)

			fl := getUpstreamFlags(t, ts, "wb561b", "pw", "INBOX", uid)
			if fl == nil {
				t.Fatalf("upstream INBOX uid %d without a Message-ID was expunged", uid)
			}
			for _, x := range fl {
				if x == imap.FlagDeleted {
					t.Fatalf("upstream INBOX uid %d was flagged \\Deleted", uid)
				}
			}
		})
	}
}

// TestWriteBackDestroy_IdentifiedMessage_Expunged: a mirrored message with a
// Message-ID, deleted in herold, is expunged upstream.
func TestWriteBackDestroy_IdentifiedMessage_Expunged(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561c", buildRFC822("wb561c-id@test", "identified", d))
			if err := ha.Store.Meta().ExpungeMessages(context.Background(), ms.HeroldMailboxID, []store.MessageID{ms.HeroldMessageID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, ha, ts, acc)

			if fl := getUpstreamFlags(t, ts, "wb561c", "pw", "INBOX", uid); fl != nil {
				t.Fatalf("identified upstream INBOX uid %d was not expunged (flags %v)", uid, fl)
			}
		})
	}
}

// TestWriteBackDestroy_LegacyRowWithoutIdentity_NoExpunge: a row that predates
// the recorded identity is never expunged through.
func TestWriteBackDestroy_LegacyRowWithoutIdentity_NoExpunge(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561d", buildRFC822("wb561d-id@test", "legacy", d))
			if !rowHasUpstreamIdentity(ms) {
				t.Fatalf("sync did not record the upstream identity: %+v", ms)
			}
			ms.UpstreamMessageID, ms.UpstreamSize = "", 0
			if err := ha.Store.Meta().UpsertIMAPImportMessageState(context.Background(), ms); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if err := ha.Store.Meta().ExpungeMessages(context.Background(), ms.HeroldMailboxID, []store.MessageID{ms.HeroldMessageID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}

			runOneWriteBackPass(t, ha, ts, acc)

			if fl := getUpstreamFlags(t, ts, "wb561d", "pw", "INBOX", uid); fl == nil {
				t.Fatalf("upstream INBOX uid %d was expunged through a row without identity", uid)
			}
		})
	}
}

// TestSyncBackfillsUpstreamIdentity: the sync pass records the identity of a
// legacy row whose upstream message carries the herold message's Message-ID,
// and leaves a row naming a different message unidentified.
func TestSyncBackfillsUpstreamIdentity(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561e", buildRFC822("wb561e-id@test", "backfill", d))
			// A second upstream message, mirrored; its row is then re-pointed at
			// the first message's herold id so it names a different message.
			uid2 := appendToServer(t, ts, "wb561e", "pw", "INBOX", buildRFC822("wb561e-other@test", "other", d), nil, d)
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}
			ctx := context.Background()
			meta := ha.Store.Meta()
			ms2, found, err := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid2))
			if err != nil || !found {
				t.Fatalf("second row: found=%v err=%v", found, err)
			}

			ms.UpstreamMessageID, ms.UpstreamSize = "", 0
			if err := meta.UpsertIMAPImportMessageState(ctx, ms); err != nil {
				t.Fatalf("upsert legacy: %v", err)
			}
			ms2.HeroldMessageID = ms.HeroldMessageID
			ms2.UpstreamMessageID, ms2.UpstreamSize = "", 0
			if err := meta.UpsertIMAPImportMessageState(ctx, ms2); err != nil {
				t.Fatalf("upsert mismatched: %v", err)
			}

			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}

			got, _, _ := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
			if !rowHasUpstreamIdentity(got) || got.UpstreamMessageID != "wb561e-id@test" {
				t.Errorf("legacy row identity not recorded: %+v", got)
			}
			got2, _, _ := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid2))
			if rowHasUpstreamIdentity(got2) {
				t.Errorf("row naming a different message was given an identity: %+v", got2)
			}
		})
	}
}
