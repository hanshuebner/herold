package imapimport

// writeback_race_test.go pins #563: flag write-back reads a state row, does
// an upstream round trip, then records the new LastSyncedFlags. A change the
// sync goroutine makes to the row in between (expunge reconcile deleting it,
// the identity backfill recording upstream_message_id/upstream_size, a
// membership change) must survive the write-back.

import (
	"context"
	"sync"
	"testing"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// hookConn runs hook once, right after the first UIDFetchFlags returns: the
// window between write-back's row read and its row write.
type hookConn struct {
	Conn
	once sync.Once
	hook func()
}

func (c *hookConn) UIDFetchFlags(ctx context.Context, uid imap.UID) ([]imap.Flag, error) {
	fl, err := c.Conn.UIDFetchFlags(ctx, uid)
	c.once.Do(c.hook)
	return fl, err
}

func TestWriteBackFlagRecord_ConcurrentRowChangeSurvives(t *testing.T) {
	type race struct {
		name  string
		setup func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState)
		hook  func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState)
		check func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState, got store.IMAPImportMessageState, found bool)
	}
	ctx := context.Background()
	races := []race{
		{
			name: "expunge_reconcile_deletes_row",
			hook: func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState) {
				if err := f.ha.Store.Meta().DeleteIMAPImportMessageState(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID); err != nil {
					t.Errorf("hook delete: %v", err)
				}
			},
			check: func(t *testing.T, f *moveFixture, ms, got store.IMAPImportMessageState, found bool) {
				if found {
					t.Errorf("deleted row was resurrected: %+v", got)
				}
			},
		},
		{
			name: "identity_backfill_records_identity",
			setup: func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState) {
				ms.UpstreamMessageID, ms.UpstreamSize = "", 0
				if err := f.ha.Store.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil {
					t.Fatalf("clear identity: %v", err)
				}
			},
			hook: func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState) {
				ok, err := f.ha.Store.Meta().SetIMAPImportMessageStateIdentity(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID, ms.HeroldMessageID, "late-id@test", 777)
				if err != nil || !ok {
					t.Errorf("hook identity: ok=%v err=%v", ok, err)
				}
			},
			check: func(t *testing.T, f *moveFixture, ms, got store.IMAPImportMessageState, found bool) {
				if !found {
					t.Fatal("row vanished")
				}
				if got.UpstreamMessageID != "late-id@test" || got.UpstreamSize != 777 {
					t.Errorf("recorded identity erased: (%q, %d)", got.UpstreamMessageID, got.UpstreamSize)
				}
			},
		},
		{
			name: "membership_change",
			hook: func(t *testing.T, f *moveFixture, ms store.IMAPImportMessageState) {
				moved := ms
				moved.HeroldMailboxID = f.inbox
				if err := f.ha.Store.Meta().UpsertIMAPImportMessageState(ctx, moved); err != nil {
					t.Errorf("hook membership: %v", err)
				}
			},
			check: func(t *testing.T, f *moveFixture, ms, got store.IMAPImportMessageState, found bool) {
				if !found {
					t.Fatal("row vanished")
				}
				if got.HeroldMailboxID != f.inbox {
					t.Errorf("membership change reverted: HeroldMailboxID=%d want %d", got.HeroldMailboxID, f.inbox)
				}
			},
		},
	}
	for _, branch := range []string{"push", "down"} {
		for _, r := range races {
			t.Run(branch+"/"+r.name, func(t *testing.T) {
				for _, be := range ownSentDedupBackends(t) {
					t.Run(be.name, func(t *testing.T) {
						f := newMoveFixture(t, be, "wb563"+branch[:1]+r.name[:3], false)
						meta := f.ha.Store.Meta()
						rows, err := meta.ListIMAPImportMessageStatesByMessage(ctx, f.msgID)
						if err != nil || len(rows) != 1 {
							t.Fatalf("rows: %v %v", rows, err)
						}
						ms := rows[0]
						if r.setup != nil {
							r.setup(t, f, ms)
							ms, _, _ = meta.GetIMAPImportMessageState(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID)
						}
						if branch == "push" {
							if _, err := meta.UpdateMessageFlags(ctx, f.msgID, ms.HeroldMailboxID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
								t.Fatalf("seen: %v", err)
							}
						} else {
							setUpstreamSeen(t, f.ts, f.user, "pw", ms.UpstreamFolder, imap.UID(ms.UpstreamUID))
						}

						w := newAccountWorker(accountWorkerOpts{
							account: f.acc, store: f.ha.Store, dataKey: testDataKey(t),
							cfg: sysconfig.IMAPImportConfig{}, log: newTestLogger(t), clk: f.ha.Clock,
							dialer: &fakeDialer{ts: f.ts}, categoriser: noopCategoriser{},
						})
						conn, err := w.openSecondaryConn(ctx)
						if err != nil {
							t.Fatalf("dial: %v", err)
						}
						defer conn.Close()
						hc := &hookConn{Conn: conn, hook: func() { r.hook(t, f, ms) }}

						w.reconcileMessageFlags(ctx, hc, f.msgID)

						got, found, err := meta.GetIMAPImportMessageState(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID)
						if err != nil {
							t.Fatalf("get: %v", err)
						}
						r.check(t, f, ms, got, found)
					})
				}
			})
		}
	}
}
