package protosmtp_test

// deliver_llm_record_msgid_test.go covers re #394: persistLLMRecord used to
// locate the just-delivered message by looking up its Message-ID header, so
// a message with no Message-ID header got no llm_classifications row at
// all, and two messages sharing one Message-ID value both raced to attach
// their record to whichever row the header lookup happened to find first.
// The fix keys the record on the store-assigned message id the delivery
// already resolves from InsertMessage's returned UID, so both cases are
// unambiguous.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

func newPGStoreFactory(t *testing.T) store.Store {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
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
}

func TestDelivery_LLMRecord_NoMessageID_SQLite(t *testing.T) {
	testDeliveryLLMRecordNoMessageID(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_LLMRecord_NoMessageID_Postgres(t *testing.T) {
	testDeliveryLLMRecordNoMessageID(t, newPGStoreFactory)
}

func testDeliveryLLMRecordNoMessageID(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.95,"reason":"promo blast"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<no-msgid@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	// Deliberately no Message-ID header.
	body := "From: no-msgid@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: no message-id\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Sender: "no-msgid@sender.test", Limit: 10})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchAdminMessages hits = %d, want 1", len(hits))
	}
	mid := hits[0].MessageID

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, mid)
	if err != nil {
		t.Fatalf("GetLLMClassification(%d): %v -- transparency record missing for a message with no Message-ID header", mid, err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want %q", rec.SpamVerdict, "spam")
	}
}

func TestDelivery_LLMRecord_DuplicateMessageID_SQLite(t *testing.T) {
	testDeliveryLLMRecordDuplicateMessageID(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_LLMRecord_DuplicateMessageID_Postgres(t *testing.T) {
	testDeliveryLLMRecordDuplicateMessageID(t, newPGStoreFactory)
}

func testDeliveryLLMRecordDuplicateMessageID(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})

	deliver := func(t *testing.T, sender, verdictJSON string) {
		t.Helper()
		f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(verdictJSON), nil
		})
		cli, closeFn := f.dial(t)
		defer closeFn()
		mustOK(t, cli, 220)
		cli.send(t, "EHLO client.example.test")
		mustOK(t, cli, 250)
		cli.send(t, "MAIL FROM:<"+sender+">")
		mustOK(t, cli, 250)
		cli.send(t, "RCPT TO:<alice@example.test>")
		mustOK(t, cli, 250)
		cli.send(t, "DATA")
		mustOK(t, cli, 354)
		body := "From: " + sender + "\r\nTo: alice@example.test\r\n" +
			"Message-ID: <shared-id@sender.test>\r\n" +
			"Subject: duplicate message-id\r\n\r\nBody text.\r\n.\r\n"
		cli.sendRaw(t, []byte(body))
		mustOK(t, cli, 250)
		cli.send(t, "QUIT")
		mustOK(t, cli, 221)
	}

	// Two distinct messages sharing one Message-ID value, delivered from
	// different senders (so each row is independently locatable), each
	// classified with a different verdict.
	deliver(t, "dup1@sender.test", `{"verdict":"spam","score":0.95,"reason":"promo blast"}`)
	deliver(t, "dup2@sender.test", `{"verdict":"ham","score":0.05}`)

	ctx := context.Background()
	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{MessageID: "shared-id@sender.test", Limit: 10})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("SearchAdminMessages(shared-id) hits = %d, want 2", len(hits))
	}

	// mail.Address.String() (used to build Envelope.From) always wraps a
	// bare address in angle brackets.
	wantVerdict := map[string]string{
		"<dup1@sender.test>": "spam",
		"<dup2@sender.test>": "ham",
	}
	seen := map[string]bool{}
	for _, hit := range hits {
		want, ok := wantVerdict[hit.Envelope.From]
		if !ok {
			t.Fatalf("unexpected From %q in hits", hit.Envelope.From)
		}
		rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, hit.MessageID)
		if err != nil {
			t.Fatalf("GetLLMClassification(%d) for %q: %v -- transparency record missing/misattached for a duplicate Message-ID", hit.MessageID, hit.Envelope.From, err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != want {
			t.Fatalf("From %q: SpamVerdict = %v, want %q", hit.Envelope.From, rec.SpamVerdict, want)
		}
		seen[hit.Envelope.From] = true
	}
	if len(seen) != 2 {
		t.Fatalf("verified %d distinct senders, want 2", len(seen))
	}
}
