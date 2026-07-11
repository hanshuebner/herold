package protosmtp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// TestAliasExternalTarget_QueuesOutbound verifies the full re #181 flow:
// inbound mail to a local address whose alias targets an address outside
// this deployment is re-injected into the outbound queue (riding the #63
// redirect-to-queue substrate) rather than delivered to any local
// mailbox. The envelope sender is preserved (see the #181 issue comment
// for the return-path analysis) and the message is not re-signed.
func TestAliasExternalTarget_QueuesOutbound(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{envID: "env-alias-external-1"}
	f.srv.SetSubmissionQueue(q)

	ctx := context.Background()
	if _, err := f.ha.Store.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:     "sales",
		Domain:        "example.test",
		TargetAddress: "sales@external.example",
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<customer@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<sales@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: customer@sender.test\r\nTo: sales@example.test\r\n" +
		"Subject: quote request\r\nMessage-ID: <alias-ext-1@sender.test>\r\n" +
		"\r\nPlease send a quote.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.MailFrom != "customer@sender.test" {
		t.Errorf("MailFrom = %q, want customer@sender.test (envelope sender preserved)", c.MailFrom)
	}
	if len(c.Recipients) != 1 || c.Recipients[0] != "sales@external.example" {
		t.Errorf("Recipients = %v, want [sales@external.example]", c.Recipients)
	}
	if c.Sign {
		t.Errorf("Sign should be false for alias forwarding (do not re-sign forwarded messages)")
	}
	if c.PrincipalID != nil {
		t.Errorf("PrincipalID should be nil (forwarding path, no owning principal)")
	}
	if c.DSNNotify != store.DSNNotifyFailure {
		t.Errorf("DSNNotify = %v, want DSNNotifyFailure (herold-local failure DSN to the original sender)", c.DSNNotify)
	}

	// No local mailbox should receive the message: there is no
	// principal behind an external-target alias.
	mbs, _ := f.ha.Store.Meta().ListMailboxes(ctx, f.principal)
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			msgs, _ := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 5})
			if len(msgs) != 0 {
				t.Errorf("alias-external forward must not deliver locally, found %d messages in alice's INBOX", len(msgs))
			}
		}
	}
}

// TestAliasExternalTarget_LoopGuard verifies that the #63 loop guard
// (Received: header count) also suppresses an alias-external forward.
// Unlike a Sieve redirect (which falls back to the owning principal's
// INBOX), an external-target alias has no local mailbox to fall back
// to, so the suppressed forward makes the whole RCPT a delivery
// failure: the server returns a transient 451 rather than silently
// accepting and dropping the message. This is a deliberate fail-safe
// (the sending MTA retries, and eventually bounces to its own user, if
// the loop guard keeps tripping) documented here so a future change is
// a visible diff rather than a silent regression toward data loss.
func TestAliasExternalTarget_LoopGuard(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)

	ctx := context.Background()
	if _, err := f.ha.Store.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:     "sales",
		Domain:        "example.test",
		TargetAddress: "sales@external.example",
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}

	var hdrBuf strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&hdrBuf,
			"Received: from hop%d.example.com by mx%d.example.com; Mon, 01 Jan 2024 00:00:00 +0000\r\n",
			i, i)
	}
	body := hdrBuf.String() +
		"From: customer@sender.test\r\nTo: sales@example.test\r\n" +
		"Subject: loop buster\r\n\r\nLoop body.\r\n.\r\n"

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<customer@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<sales@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	// The loop guard suppresses the only recipient's forward and there
	// is no local mailbox to fall back to, so DATA fails for the whole
	// message (451, transient — the sender's MTA will retry).
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 451)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if calls := q.Calls(); len(calls) != 0 {
		t.Errorf("loop guard: queue.Submit called %d times, want 0", len(calls))
	}
}
