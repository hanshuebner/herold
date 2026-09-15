package protosmtp_test

// deliver_llm_record_stamp_test.go covers re #396 part 3: whether the
// live-path stored request (spam_prompt_applied) still carries herold's
// own x-herold-spam / x-herold-spam-engine stamp after #389.
// internal/spam.BuildRequest's stripHeroldSpamToken (re #389) strips the
// stamp from auth.Raw before assigning it to Request.AuthResults, and
// persistLLMRecord (internal/protosmtp/deliver.go) rebuilds its own
// fresh spam.Request from the already-stamped authResults for the
// transparency record -- so the strip already applies there too, even
// though persistLLMRecord runs strictly after the stamp was appended.
// These tests prove that on the actual delivery pipeline rather than
// only at the BuildRequest-unit level (internal/spam/classifier_test.go
// already covers the unit level).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_LLMRecord_StoredRequestHasNoSpamStamp_SQLite(t *testing.T) {
	testDeliveryLLMRecordStoredRequestHasNoSpamStamp(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_LLMRecord_StoredRequestHasNoSpamStamp_Postgres(t *testing.T) {
	testDeliveryLLMRecordStoredRequestHasNoSpamStamp(t, newPGStoreFactory)
}

func testDeliveryLLMRecordStoredRequestHasNoSpamStamp(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"spam","score":0.95,"reason":"promo blast"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<stamp-check@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: stamp-check@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: stamp check\r\n\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Sender: "stamp-check@sender.test", Limit: 10})
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
	if rec.SpamPromptApplied == nil {
		t.Fatalf("SpamPromptApplied is nil, want the canonical spam.Request JSON")
	}
	var stored struct {
		AuthResults string `json:"auth_results"`
	}
	if err := json.Unmarshal([]byte(*rec.SpamPromptApplied), &stored); err != nil {
		t.Fatalf("decode spam_prompt_applied: %v (raw=%s)", err, *rec.SpamPromptApplied)
	}
	if strings.Contains(strings.ToLower(stored.AuthResults), "x-herold-spam") {
		t.Fatalf("stored spam_prompt_applied auth_results carries herold's own post-verdict stamp: %q", stored.AuthResults)
	}

	// Confirm the stamp really was present on the delivered message's
	// own stored Authentication-Results header -- i.e. this is not a
	// vacuously-true assertion against a message with no stamp at all.
	msg, err := f.ha.Store.Meta().GetMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetMessage(%d): %v", mid, err)
	}
	rc, err := f.ha.Store.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	defer rc.Close()
	var raw strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := rc.Read(buf)
		raw.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	if !strings.Contains(strings.ToLower(raw.String()), "x-herold-spam") {
		t.Fatalf("delivered blob's own Authentication-Results carries no x-herold-spam stamp; test setup is not exercising the reported scenario")
	}
}
