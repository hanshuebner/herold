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
	"github.com/hanshuebner/herold/internal/sysconfig"
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

// identityWorker builds a worker with a dialed connection for tests that call
// worker methods directly.
func identityWorker(t *testing.T, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount) (*accountWorker, Conn) {
	t.Helper()
	ctx := context.Background()
	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})
	cred, err := w.openCredential(ctx, acc)
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID: acc.ID, Host: acc.Host, Port: acc.Port,
		TLSMode: string(acc.TLSMode), Username: acc.Username,
		AuthMethod: string(acc.AuthMethod), CredentialPlaintext: cred,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Logout(); conn.Close() })
	return w, conn
}

// TestIdentityBackfill_ConcurrentWriteBackChange_Survives: a write-back change
// that lands between the backfill's upstream fetch and its store write is not
// undone: a deleted row is not re-inserted, and a row whose other columns
// changed keeps them.
func TestIdentityBackfill_ConcurrentWriteBackChange_Survives(t *testing.T) {
	for _, mode := range []string{"delete", "update"} {
		for _, be := range ownSentDedupBackends(t) {
			t.Run(be.name+"/"+mode, func(t *testing.T) {
				d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				user := "wb561f" + mode
				ha, ts, acc, uid, ms := mirrorOne(t, be, user, buildRFC822(user+"-id@test", "race", d))
				ctx := context.Background()
				meta := ha.Store.Meta()
				ms.UpstreamMessageID, ms.UpstreamSize = "", 0
				if err := meta.UpsertIMAPImportMessageState(ctx, ms); err != nil {
					t.Fatalf("upsert legacy: %v", err)
				}

				w, conn := identityWorker(t, ha, ts, acc)
				si, err := conn.Select(ctx, "INBOX")
				if err != nil {
					t.Fatalf("Select: %v", err)
				}
				w.testBeforeIdentityWrite = func() {
					switch mode {
					case "delete":
						if err := meta.DeleteIMAPImportMessageState(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID); err != nil {
							t.Errorf("hook delete: %v", err)
						}
					case "update":
						changed := ms
						changed.LastSyncedFlags = store.IMAPImportFlagFlagged
						if err := meta.UpsertIMAPImportMessageState(ctx, changed); err != nil {
							t.Errorf("hook update: %v", err)
						}
					}
				}
				w.backfillUpstreamIdentity(ctx, conn, "INBOX", si.UIDValidity)

				got, found, err := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
				if err != nil {
					t.Fatalf("get: %v", err)
				}
				switch mode {
				case "delete":
					if found {
						t.Fatalf("backfill re-inserted a row write-back deleted: %+v", got)
					}
				case "update":
					if !found || got.LastSyncedFlags != store.IMAPImportFlagFlagged {
						t.Fatalf("backfill overwrote a concurrent change: found=%v row=%+v", found, got)
					}
					if !rowHasUpstreamIdentity(got) {
						t.Fatalf("identity not recorded on the unchanged-key row: %+v", got)
					}
				}
			})
		}
	}
}

// TestIdentity_UIDValidityMismatch_Skips: when the folder's UIDVALIDITY differs
// from the cursor's, destroy does not expunge and the backfill records nothing.
func TestIdentity_UIDValidityMismatch_Skips(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561g", buildRFC822("wb561g-id@test", "uv", d))
			ctx := context.Background()
			meta := ha.Store.Meta()
			cur, found, err := meta.GetIMAPImportFolderCursor(ctx, acc.ID, "INBOX")
			if err != nil || !found {
				t.Fatalf("cursor: found=%v err=%v", found, err)
			}

			// Backfill: legacy row, cursor validity differs from the folder's.
			legacy := ms
			legacy.UpstreamMessageID, legacy.UpstreamSize = "", 0
			if err := meta.UpsertIMAPImportMessageState(ctx, legacy); err != nil {
				t.Fatalf("upsert legacy: %v", err)
			}
			w, conn := identityWorker(t, ha, ts, acc)
			si, err := conn.Select(ctx, "INBOX")
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			stale := cur
			stale.UIDValidity = cur.UIDValidity + 1
			if err := meta.UpsertIMAPImportFolderCursor(ctx, stale); err != nil {
				t.Fatalf("cursor upsert: %v", err)
			}
			w.backfillUpstreamIdentity(ctx, conn, "INBOX", si.UIDValidity)
			got, _, _ := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid))
			if rowHasUpstreamIdentity(got) {
				t.Errorf("backfill ran across a UIDVALIDITY mismatch: %+v", got)
			}

			// Destroy: identified row, same mismatch.
			if err := meta.UpsertIMAPImportMessageState(ctx, ms); err != nil {
				t.Fatalf("restore identity: %v", err)
			}
			if err := meta.ExpungeMessages(ctx, ms.HeroldMailboxID, []store.MessageID{ms.HeroldMessageID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}
			runOneWriteBackPass(t, ha, ts, acc)
			if fl := getUpstreamFlags(t, ts, "wb561g", "pw", "INBOX", uid); fl == nil {
				t.Fatalf("upstream uid %d expunged across a UIDVALIDITY mismatch", uid)
			}
		})
	}
}

// TestIdentity_ResendSameMessageID_NotBackfilledNotExpunged: a resend carries
// the same Message-ID with different content. It is not given an identity from
// the herold copy, and an identity recorded for the original never matches it.
func TestIdentity_ResendSameMessageID_NotBackfilledNotExpunged(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ha, ts, acc, uid1, ms1 := mirrorOne(t, be, "wb561h", buildRFC822("wb561h-id@test", "original", d))
			uid2 := appendToServer(t, ts, "wb561h", "pw", "INBOX", buildRFC822("wb561h-id@test", "original resent with a longer subject", d), nil, d)
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}
			ctx := context.Background()
			meta := ha.Store.Meta()
			ms2, found, err := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid2))
			if err != nil || !found {
				t.Fatalf("resend row: found=%v err=%v", found, err)
			}
			if ms2.HeroldMessageID != ms1.HeroldMessageID {
				t.Fatalf("setup: resend did not dedup onto the original")
			}

			// Backfill must not identify the resend row from the herold copy.
			legacy := ms2
			legacy.UpstreamMessageID, legacy.UpstreamSize = "", 0
			if err := meta.UpsertIMAPImportMessageState(ctx, legacy); err != nil {
				t.Fatalf("upsert legacy: %v", err)
			}
			if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
				t.Fatalf("sync: %v", err)
			}
			got, _, _ := meta.GetIMAPImportMessageState(ctx, acc.ID, "INBOX", uint32(uid2))
			if rowHasUpstreamIdentity(got) {
				t.Errorf("resend row was backfilled from the herold copy: %+v", got)
			}

			// A row carrying the original's identity but naming the resend is
			// not expunged either (size differs).
			bad := ms2
			bad.UpstreamMessageID, bad.UpstreamSize = ms1.UpstreamMessageID, ms1.UpstreamSize
			if err := meta.UpsertIMAPImportMessageState(ctx, bad); err != nil {
				t.Fatalf("upsert bad: %v", err)
			}
			if err := meta.ExpungeMessages(ctx, ms1.HeroldMailboxID, []store.MessageID{ms1.HeroldMessageID}); err != nil {
				t.Fatalf("ExpungeMessages: %v", err)
			}
			runOneWriteBackPass(t, ha, ts, acc)
			if fl := getUpstreamFlags(t, ts, "wb561h", "pw", "INBOX", uid2); fl == nil {
				t.Fatalf("resend uid %d expunged on the original's identity", uid2)
			}
			if fl := getUpstreamFlags(t, ts, "wb561h", "pw", "INBOX", uid1); fl != nil {
				t.Fatalf("original uid %d with a matching identity was not expunged", uid1)
			}
		})
	}
}

// TestIdentity_RecordedSizeIsServerRFC822Size: the size recorded at sync time
// is the RFC822.SIZE the server reports, the value destroy compares against.
func TestIdentity_RecordedSizeIsServerRFC822Size(t *testing.T) {
	for _, be := range ownSentDedupBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			raw := buildRFC822("wb561i-id@test", "size", d)
			ha, ts, acc, uid, ms := mirrorOne(t, be, "wb561i", raw)
			ctx := context.Background()
			_, conn := identityWorker(t, ha, ts, acc)
			if _, err := conn.Select(ctx, "INBOX"); err != nil {
				t.Fatalf("Select: %v", err)
			}
			envs, err := conn.UIDFetchEnvelope(ctx, []imap.UID{uid})
			if err != nil || len(envs) != 1 {
				t.Fatalf("envelope: %v %v", envs, err)
			}
			if envs[0].Size <= 0 {
				t.Fatalf("server reported no RFC822.SIZE")
			}
			if ms.UpstreamSize != envs[0].Size || ms.UpstreamSize != int64(len(raw)) {
				t.Fatalf("recorded size %d, server RFC822.SIZE %d, body length %d", ms.UpstreamSize, envs[0].Size, len(raw))
			}
		})
	}
}
