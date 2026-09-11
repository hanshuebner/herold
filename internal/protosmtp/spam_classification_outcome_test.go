package protosmtp_test

// spam_classification_outcome_test.go covers re #326: every delivered
// message's classification outcome -- spam, ham, and an Unclassified
// outcome caused by a classifier timeout or a plugin error -- is
// recorded in llm_classifications, queryable after the fact. Runs on
// both store backends (SQLite by default; Postgres when HEROLD_PG_DSN
// is set), mirroring spam_reclassify_test.go's parametrization.
//
// The timeout/plugin-error cases use a scripted fake plugin handler
// that returns the target error directly (context.DeadlineExceeded /
// a plain error) rather than actually blocking past a deadline: the
// deadline-cutoff mechanism itself (server budget vs FakeClock) is
// already covered by internal/spam's TestClassify_BudgetCutoff_FakeClock;
// this test only needs a deterministic Unclassified-with-reason outcome
// to reach the delivery pipeline.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

func TestDelivery_SpamClassificationOutcome_SQLite(t *testing.T) {
	testDeliverySpamClassificationOutcome(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_SpamClassificationOutcome_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	testDeliverySpamClassificationOutcome(t, func(t *testing.T) store.Store {
		// A fresh pool per subtest (rather than one shared across the
		// table): testharness.Server.Close unconditionally closes
		// Options.Store on t.Cleanup, so a store shared across sibling
		// t.Run subtests would be closed after the first one returns.
		st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
		if err != nil {
			t.Skipf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface {
			TruncateAll(ctx context.Context) error
		}); ok {
			if err := tr.TruncateAll(context.Background()); err != nil {
				_ = st.Close()
				t.Fatalf("TruncateAll: %v", err)
			}
		}
		return st
	})
}

func testDeliverySpamClassificationOutcome(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	cases := []struct {
		name           string
		handle         func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error)
		wantVerdict    string
		wantReasonHas  string // substring wantReason must contain; "" skips the check
		wantReasonNone bool   // when true, SpamReason must be nil
	}{
		{
			name: "spam",
			handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"verdict":"spam","score":0.95,"reason":"promo blast"}`), nil
			},
			wantVerdict:   "spam",
			wantReasonHas: "promo blast",
		},
		{
			name: "ham",
			handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"verdict":"ham","score":0.05}`), nil
			},
			wantVerdict:    "ham",
			wantReasonNone: true,
		},
		{
			name: "timeout",
			handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return nil, context.DeadlineExceeded
			},
			wantVerdict:   "unclassified",
			wantReasonHas: "timeout:",
		},
		{
			name: "plugin-error",
			handle: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return nil, errors.New("plugin crashed")
			},
			wantVerdict:   "unclassified",
			wantReasonHas: "plugin_error:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
			f.spamPlug.Handle("spam.classify", tc.handle)

			cli, closeFn := f.dial(t)
			defer closeFn()
			mustOK(t, cli, 220)
			cli.send(t, "EHLO client.example.test")
			mustOK(t, cli, 250)
			cli.send(t, "MAIL FROM:<sender@sender.test>")
			mustOK(t, cli, 250)
			cli.send(t, "RCPT TO:<alice@example.test>")
			mustOK(t, cli, 250)
			cli.send(t, "DATA")
			mustOK(t, cli, 354)
			msgID := "outcome-" + tc.name + "@sender.test"
			body := "From: sender@sender.test\r\nTo: alice@example.test\r\n" +
				"Message-ID: <" + msgID + ">\r\n" +
				"Subject: outcome test " + tc.name + "\r\n\r\nBody text.\r\n.\r\n"
			cli.sendRaw(t, []byte(body))
			mustOK(t, cli, 250)
			cli.send(t, "QUIT")
			mustOK(t, cli, 221)

			ctx := context.Background()
			hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: msgID, Limit: 10})
			if err != nil {
				t.Fatalf("SearchAdminMessages: %v", err)
			}
			if len(hits) != 1 {
				t.Fatalf("SearchAdminMessages(%q) hits = %d, want 1", msgID, len(hits))
			}
			mid := hits[0].MessageID

			rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, mid)
			if err != nil {
				t.Fatalf("GetLLMClassification(%d): %v", mid, err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != tc.wantVerdict {
				t.Fatalf("SpamVerdict = %v, want %q", rec.SpamVerdict, tc.wantVerdict)
			}
			if tc.wantReasonNone {
				if rec.SpamReason != nil {
					t.Errorf("SpamReason = %q, want nil", *rec.SpamReason)
				}
			} else if tc.wantReasonHas != "" {
				if rec.SpamReason == nil {
					t.Fatalf("SpamReason = nil, want substring %q", tc.wantReasonHas)
				}
				if got := *rec.SpamReason; !strings.Contains(got, tc.wantReasonHas) {
					t.Errorf("SpamReason = %q, want substring %q", got, tc.wantReasonHas)
				}
			}

			// message-research renders the same fields the admin surface
			// reads (internal/protoadmin/message_research.go), re #326.
			if hits[0].SpamVerdict == nil || *hits[0].SpamVerdict != tc.wantVerdict {
				t.Errorf("AdminMessageHit.SpamVerdict = %v, want %q", hits[0].SpamVerdict, tc.wantVerdict)
			}
		})
	}
}
