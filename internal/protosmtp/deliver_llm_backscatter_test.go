package protosmtp_test

// deliver_llm_backscatter_test.go covers re #513: a delivery-status
// report (multipart/report; report-type=delivery-status, RFC 3464)
// enclosing a message the recipient never sent must be classified spam
// and delivered to Junk, deterministically, regardless of what the
// classifier plugin itself reports -- the reported defect (message
// 4247) had the plugin return ham at score 0.05 with no spam_signals at
// all, and the report was delivered to the Inbox. A bounce of a message
// the recipient actually sent must keep reaching the Inbox.
//
// Each case runs on both backends (SQLite and Postgres), matching this
// repo's store-change convention.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_BackscatterReportGoesToJunk_SQLite(t *testing.T) {
	testDeliveryBackscatterReportGoesToJunk(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_BackscatterReportGoesToJunk_Postgres(t *testing.T) {
	testDeliveryBackscatterReportGoesToJunk(t, newPGStoreFactory)
}

// testDeliveryBackscatterReportGoesToJunk reproduces message 4247's
// shape: a delivery-status report enclosing a marketing message from a
// foreign sender (bounces@ingeasoto.com, not alice's address) that was
// never sent by the recipient. The fake classifier plugin returns
// exactly the reported shape -- verdict=ham, score=0.05, no
// spam_signals at all -- and delivery must still land the message in
// Junk with the server-computed backscatter fact decisive.
func testDeliveryBackscatterReportGoesToJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"legitimate bounce notice for mail sent from the owner's own address"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<Mailer-Daemon@forwarder.example.net>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: Mail Delivery System <Mailer-Daemon@forwarder.example.net>\r\n" +
		"To: alice@example.test\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Subject: Mail delivery failed: returning message to sender\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
		" boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"This message was created automatically by mail delivery software.\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: message/delivery-status\r\n" +
		"\r\n" +
		"Reporting-MTA: dns; forwarder.example.net\r\n" +
		"\r\n" +
		"Action: failed\r\n" +
		"Status: 5.6.0\r\n" +
		"Remote-MTA: dns; mx.example.test\r\n" +
		"Diagnostic-Code: smtp; 554 5.6.0 message parse failed: truncated\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"\r\n" +
		"From: CMarketing PERU <bounces@ingeasoto.com>\r\n" +
		"To: alice@example.test\r\n" +
		"Return-Path: <bounces@ingeasoto.com>\r\n" +
		"Subject: Oferta especial MBA\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"Compre ahora nuestro MBA especial.\r\n" +
		"--outer--\r\n" +
		".\r\n"
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
		t.Fatalf("messages in Junk = %d, want 1 (a backscatter report must resolve to Junk)", len(msgs))
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
	if rec.SpamDecisiveSignalMatch == nil || *rec.SpamDecisiveSignalMatch != "backscatter" {
		t.Fatalf("SpamDecisiveSignalMatch = %v, want backscatter", rec.SpamDecisiveSignalMatch)
	}
}

func TestDelivery_BounceOfOwnSentMessageStaysInInbox_SQLite(t *testing.T) {
	testDeliveryBounceOfOwnSentMessageStaysInInbox(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_BounceOfOwnSentMessageStaysInInbox_Postgres(t *testing.T) {
	testDeliveryBounceOfOwnSentMessageStaysInInbox(t, newPGStoreFactory)
}

// testDeliveryBounceOfOwnSentMessageStaysInInbox verifies a
// delivery-status report enclosing a message alice actually sent (her
// own address as the enclosed original's From and Return-Path) is never
// treated as backscatter: the classifier's ham verdict stands and the
// report reaches the Inbox.
func testDeliveryBounceOfOwnSentMessageStaysInInbox(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"legitimate bounce of mail the owner actually sent"}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<Mailer-Daemon@mx.example.net>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: Mail Delivery System <Mailer-Daemon@mx.example.net>\r\n" +
		"To: alice@example.test\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Subject: Mail delivery failed: returning message to sender\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
		" boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"This message was created automatically by mail delivery software.\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: message/delivery-status\r\n" +
		"\r\n" +
		"Reporting-MTA: dns; mx.example.net\r\n" +
		"\r\n" +
		"Action: failed\r\n" +
		"Status: 5.1.1\r\n" +
		"Remote-MTA: dns; mx.example.net\r\n" +
		"Diagnostic-Code: smtp; 550 5.1.1 user unknown\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"\r\n" +
		"From: Alice <alice@example.test>\r\n" +
		"To: nobody@example.net\r\n" +
		"Return-Path: <alice@example.test>\r\n" +
		"Subject: Re: project update\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"Please see the attached update.\r\n" +
		"--outer--\r\n" +
		".\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	inbox, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName(INBOX): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(INBOX): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in INBOX = %d, want 1 (a bounce of the owner's own mail must keep reaching the Inbox)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
		t.Fatalf("SpamVerdict = %v, want ham (the model's verdict must stand)", rec.SpamVerdict)
	}
	if rec.SpamDecisiveSignalMatch != nil {
		t.Fatalf("SpamDecisiveSignalMatch = %v, want nil (no resolution should have happened)", *rec.SpamDecisiveSignalMatch)
	}
}
