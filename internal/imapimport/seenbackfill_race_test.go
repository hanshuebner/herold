package imapimport

// seenbackfill_race_test.go pins #563 for the seen backfill: it reads the
// state row, then records the \Seen baseline. A change made to the row in
// between (the sync goroutine's expunge reconcile deleting it, the identity
// backfill recording upstream identity) must survive. The seam is a store
// wrapper that runs a hook right after GetIMAPImportMessageState returns.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

type hookReadMeta struct {
	store.Metadata
	once sync.Once
	hook func()
}

func (m *hookReadMeta) GetIMAPImportMessageState(ctx context.Context, accountID, folder string, uid uint32) (store.IMAPImportMessageState, bool, error) {
	ms, found, err := m.Metadata.GetIMAPImportMessageState(ctx, accountID, folder, uid)
	m.once.Do(m.hook)
	return ms, found, err
}

type hookReadStore struct {
	store.Store
	meta *hookReadMeta
}

func (s *hookReadStore) Meta() store.Metadata { return s.meta }

func TestSeenBackfillRecord_ConcurrentRowChangeSurvives(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"delete", "identity"} {
		t.Run(mode, func(t *testing.T) {
			for _, be := range ownSentDedupBackends(t) {
				t.Run(be.name, func(t *testing.T) {
					ts := startTestIMAPServer(t)
					ts.addUser("sbr563", "pw")
					ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})
					acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
						email: "sbr563@example.test", username: "sbr563", credentialPlaintext: "pw",
					}, nil)
					meta := ha.Store.Meta()
					sent, err := meta.InsertMailbox(ctx, store.Mailbox{PrincipalID: acc.PrincipalID, Name: "Sent", Attributes: store.MailboxAttrSent})
					if err != nil {
						t.Fatalf("InsertMailbox: %v", err)
					}
					blob, err := ha.Store.Blobs().Put(ctx, strings.NewReader("body of sbr563@test"))
					if err != nil {
						t.Fatalf("Blobs.Put: %v", err)
					}
					if _, _, err := meta.InsertMessage(ctx, store.Message{
						PrincipalID: acc.PrincipalID, Size: blob.Size, Blob: blob,
						Envelope: store.Envelope{Subject: "s", MessageID: "sbr563@test"},
					}, []store.MessageMailbox{{MailboxID: sent.ID}}); err != nil {
						t.Fatalf("InsertMessage: %v", err)
					}
					msg, err := meta.GetMessageByMessageIDHeader(ctx, acc.PrincipalID, "sbr563@test")
					if err != nil {
						t.Fatalf("lookup: %v", err)
					}
					ms := store.IMAPImportMessageState{
						AccountID: acc.ID, UpstreamFolder: "Sent", UpstreamUID: 1,
						HeroldMessageID: msg.ID, HeroldMailboxID: sent.ID, MappedMailboxID: sent.ID,
					}
					if err := meta.UpsertIMAPImportMessageState(ctx, ms); err != nil {
						t.Fatalf("seed row: %v", err)
					}

					hm := &hookReadMeta{Metadata: meta}
					hm.hook = func() {
						switch mode {
						case "delete":
							if err := meta.DeleteIMAPImportMessageState(ctx, acc.ID, "Sent", 1); err != nil {
								t.Errorf("hook delete: %v", err)
							}
						case "identity":
							if ok, err := meta.SetIMAPImportMessageStateIdentity(ctx, acc.ID, "Sent", 1, msg.ID, "late@test", 99); err != nil || !ok {
								t.Errorf("hook identity: %v %v", ok, err)
							}
						}
					}
					w := newAccountWorker(accountWorkerOpts{
						account: acc, store: &hookReadStore{Store: ha.Store, meta: hm},
						dataKey: testDataKey(t), log: newTestLogger(t), clk: ha.Clock,
						dialer: &fakeDialer{ts: ts},
					})
					if !w.seenBackfillOne(ctx, ms) {
						t.Fatal("backfill did not correct the Sent membership; fixture is not exercising the write")
					}

					got, found, err := meta.GetIMAPImportMessageState(ctx, acc.ID, "Sent", 1)
					if err != nil {
						t.Fatalf("get: %v", err)
					}
					switch mode {
					case "delete":
						if found {
							t.Errorf("deleted row was resurrected: %+v", got)
						}
					case "identity":
						if !found || got.UpstreamMessageID != "late@test" || got.UpstreamSize != 99 {
							t.Errorf("recorded identity erased: found=%v %+v", found, got)
						}
						if !got.LastSyncedFlags.HasSeen() {
							t.Errorf("the \\Seen baseline was not recorded: %+v", got)
						}
					}
				})
			}
		})
	}
}
