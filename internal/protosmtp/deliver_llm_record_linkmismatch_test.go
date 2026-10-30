package protosmtp_test

// deliver_llm_record_linkmismatch_test.go covers re #552: the
// link-host-mismatch fact is part of the stored request
// (spam_prompt_applied), which Email/llmInspect returns as promptApplied.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_LLMRecord_StoresLinkHostMismatch_SQLite(t *testing.T) {
	testDeliveryLLMRecordStoresLinkHostMismatch(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_LLMRecord_StoresLinkHostMismatch_Postgres(t *testing.T) {
	testDeliveryLLMRecordStoresLinkHostMismatch(t, newPGStoreFactory)
}

func testDeliveryLLMRecordStoresLinkHostMismatch(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1,"reason":"ok"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<link-check@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: link-check@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: link check\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n" +
		"<p>Hello</p><a href=\"https://files.other-host.test/d\">Download</a>\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	hits, err := f.ha.Store.Meta().SearchAdminMessages(ctx, store.AdminMessageFilter{Sender: "link-check@sender.test", Limit: 10})
	if err != nil {
		t.Fatalf("SearchAdminMessages: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SearchAdminMessages hits = %d, want 1", len(hits))
	}
	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, hits[0].MessageID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamPromptApplied == nil {
		t.Fatalf("SpamPromptApplied is nil")
	}
	var stored struct {
		LinkHostMismatch bool   `json:"link_host_mismatch"`
		BodyExcerpt      string `json:"body_excerpt"`
	}
	if err := json.Unmarshal([]byte(*rec.SpamPromptApplied), &stored); err != nil {
		t.Fatalf("decode spam_prompt_applied: %v (raw=%s)", err, *rec.SpamPromptApplied)
	}
	if !stored.LinkHostMismatch {
		t.Fatalf("stored request lacks link_host_mismatch: %s", *rec.SpamPromptApplied)
	}
}
