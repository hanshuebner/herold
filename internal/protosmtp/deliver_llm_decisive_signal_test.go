package protosmtp_test

// deliver_llm_decisive_signal_test.go covers re #396, second round, item 4:
// the two acceptance-evaluation cases from the maintainer's hand-back on the
// first round's fix (comment 4833) -- a cold marketing pitch (store message
// 3570) and a relayed auto-reply to an address the principal does not own
// (store message 3637) -- both classified ham by the classifier plugin
// alongside a decisive spam_signals entry, both now expected to be
// delivered to Junk rather than the Inbox. Flagging the contradiction alone
// (the first round's fix, internal/spam.Classification.Inconsistent) left
// every one of these delivered to the Inbox; the maintainer reported this
// directly from production (29 flagged rows, all four ham-verdict-with-
// spam-signals rows still in the Inbox).
//
// Each case runs on both backends (SQLite and Postgres), matching the
// hand-back's item 4 requirement.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_ColdMarketingPitchWithSpamSignalsGoesToJunk_SQLite(t *testing.T) {
	testDeliveryColdMarketingPitchWithSpamSignalsGoesToJunk(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_ColdMarketingPitchWithSpamSignalsGoesToJunk_Postgres(t *testing.T) {
	testDeliveryColdMarketingPitchWithSpamSignalsGoesToJunk(t, newPGStoreFactory)
}

// testDeliveryColdMarketingPitchWithSpamSignalsGoesToJunk is the #396
// evaluation case for store message 3570: a cold commercial pitch from an
// unknown sender, DMARC-aligned pass, a List-Unsubscribe header present.
// The (fake) classifier plugin returns exactly the reported shape --
// verdict=ham, score=0.15, spam_signals naming unsolicited_bulk_marketing
// -- and delivery must land the message in Junk.
func testDeliveryColdMarketingPitchWithSpamSignalsGoesToJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.15,"reason":"promotional content with an unsubscribe link and passing authentication","spam_signals":["unsolicited_bulk_marketing"],"ham_signals":[]}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<sales@chopscarnesyparrillas.com>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: Creativa Marketing <sales@chopscarnesyparrillas.com>\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Boost your business with our email marketing campaigns\r\n" +
		"List-Unsubscribe: <mailto:unsubscribe@chopscarnesyparrillas.com>\r\n\r\n" +
		"We noticed your business could benefit from our email marketing services.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	junk, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Junk")
	if err != nil {
		t.Fatalf("GetMailboxByName(Junk): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in Junk = %d, want 1 (a ham verdict naming a decisive spam signal must resolve to Junk)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want spam (the applied verdict)", rec.SpamVerdict)
	}
	if rec.SpamModelVerdict == nil || *rec.SpamModelVerdict != "ham" {
		t.Fatalf("SpamModelVerdict = %v, want ham (the plugin's own original verdict)", rec.SpamModelVerdict)
	}
}

func TestDelivery_RelayedAutoReplyToNonOwnedAddressGoesToJunk_SQLite(t *testing.T) {
	testDeliveryRelayedAutoReplyToNonOwnedAddressGoesToJunk(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_RelayedAutoReplyToNonOwnedAddressGoesToJunk_Postgres(t *testing.T) {
	testDeliveryRelayedAutoReplyToNonOwnedAddressGoesToJunk(t, newPGStoreFactory)
}

// testDeliveryRelayedAutoReplyToNonOwnedAddressGoesToJunk is the #396
// evaluation case for store message 3637: an auto-reply relayed through a
// Google Group to an address the principal does not own (the SMTP envelope
// recipient is alice's own mailbox, but the message's own To header is the
// group address, matching the reported shape where the group address is
// not one of the principal's own). The (fake) classifier plugin returns the
// reported shape -- verdict=ham, score=0.08, spam_signals naming
// recipient_not_own -- and delivery must land the message in Junk.
func testDeliveryRelayedAutoReplyToNonOwnedAddressGoesToJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	var capturedRecipientNotOwn bool
	f.spamPlug.Handle("spam.classify", func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var req struct {
			RecipientNotOwn bool `json:"recipient_not_own"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("decode spam.classify request: %v", err)
		}
		capturedRecipientNotOwn = req.RecipientNotOwn
		return json.RawMessage(`{"verdict":"ham","score":0.08,"reason":"automated acknowledgment from a legitimate business","spam_signals":["recipient_not_own"],"ham_signals":[]}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<3rc@xxdz88.com>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: Jamestown <no-reply@jamestown.example>\r\n" +
		"To: 3rc@xxdz88.com\r\n" +
		"List-Id: <3rc.xxdz88.com>\r\n" +
		"Precedence: list\r\n" +
		"Subject: Ihr Anliegen\r\n\r\n" +
		"Vielen Dank fuer Ihre Anfrage.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if !capturedRecipientNotOwn {
		t.Fatalf("request recipient_not_own = false, want true (To: 3rc@xxdz88.com is not one of alice's own addresses)")
	}

	ctx := context.Background()
	junk, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Junk")
	if err != nil {
		t.Fatalf("GetMailboxByName(Junk): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in Junk = %d, want 1 (a ham verdict naming recipient_not_own must resolve to Junk)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want spam (the applied verdict)", rec.SpamVerdict)
	}
	if rec.SpamModelVerdict == nil || *rec.SpamModelVerdict != "ham" {
		t.Fatalf("SpamModelVerdict = %v, want ham (the plugin's own original verdict)", rec.SpamModelVerdict)
	}
}
