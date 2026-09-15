package protosmtp_test

// deliver_llm_record_model_test.go covers re #396 part 2: the live SMTP
// delivery path stored no spam_model, because persistLLMRecord
// (internal/protosmtp/deliver.go) only ever read
// classification.RawResponse["model"], a key no shipped classifier
// plugin's SpamClassifyResult/MailClassifyResult populates. The
// reclassify path (internal/admin/spam_reclassify.go's
// recordReclassifyVerdict) already recorded the configured plugin name
// as the model; the fix makes delivery do the same.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_LLMRecord_SpamModelSet_SQLite(t *testing.T) {
	testDeliveryLLMRecordSpamModelSet(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_LLMRecord_SpamModelSet_Postgres(t *testing.T) {
	testDeliveryLLMRecordSpamModelSet(t, newPGStoreFactory)
}

func testDeliveryLLMRecordSpamModelSet(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		// No "model" field in the plugin's response -- matching every
		// shipped classifier plugin's SpamClassifyResult/
		// MailClassifyResult, which carries no model name on the wire.
		return json.RawMessage(`{"verdict":"spam","score":0.95,"reason":"promo blast"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<model-check@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: model-check@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: spam model check\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Sender: "model-check@sender.test", Limit: 10})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchAdminMessages hits = %d, want 1", len(hits))
	}
	mid := hits[0].MessageID

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, mid)
	if err != nil {
		t.Fatalf("GetLLMClassification(%d): %v", mid, err)
	}
	if rec.SpamModel == nil || *rec.SpamModel == "" {
		t.Fatalf("SpamModel = %v, want the configured plugin name (non-nil, non-empty) -- the live SMTP path must record a model like the reclassify path does", rec.SpamModel)
	}
	if *rec.SpamModel != "spam" {
		t.Fatalf("SpamModel = %q, want %q (the fixture's configured spam plugin name)", *rec.SpamModel, "spam")
	}
}
