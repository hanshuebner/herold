package protosmtp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// TestSieveRedirect_QueuesOutbound verifies that a Sieve redirect action
// submits the message to the outbound queue with the redirect target as
// the sole recipient and an SRS-rewritten (issue #204) envelope sender
// in the recipient's own domain, decoding back to the original MAIL FROM
// (RFC 5228 §4.2, re #63). With redirect and no :copy, the message must
// not be in the local INBOX.
func TestSieveRedirect_QueuesOutbound(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{envID: "env-redirect-1"}
	f.srv.SetSubmissionQueue(q)

	if err := f.ha.Store.Meta().SetSieveScript(
		context.Background(), f.principal,
		`redirect "external@example.com";`,
	); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

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
	body := "From: sender@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: forward me\r\nMessage-ID: <redirect-test-1@sender.test>\r\n" +
		"\r\nBody text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if !strings.HasPrefix(c.MailFrom, "SRS0=") || !strings.HasSuffix(c.MailFrom, "@example.test") {
		t.Errorf("MailFrom = %q, want an SRS0= address in example.test", c.MailFrom)
	}
	if decoded := decodeSRSFromStore(t, f.ha.Store, f.ha.Clock, c.MailFrom); decoded != "sender@sender.test" {
		t.Errorf("decoded MailFrom = %q, want sender@sender.test", decoded)
	}
	if len(c.Recipients) != 1 || c.Recipients[0] != "external@example.com" {
		t.Errorf("Recipients = %v, want [external@example.com]", c.Recipients)
	}
	if c.Sign {
		t.Errorf("Sign should be false for Sieve redirect (do not re-sign forwarded messages)")
	}
	if c.PrincipalID != nil {
		t.Errorf("PrincipalID should be nil for Sieve redirect (forwarding path, not authenticated submission)")
	}

	// redirect without :copy must not deliver to local INBOX (RFC 5228 §2.10.1).
	ctx := context.Background()
	mbs, _ := f.ha.Store.Meta().ListMailboxes(ctx, f.principal)
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			msgs, _ := f.ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 5})
			if len(msgs) != 0 {
				t.Errorf("redirect without :copy: message must not be in INBOX, found %d messages", len(msgs))
			}
		}
	}
}

// TestSieveRedirect_FiltersUI_ManagedScript verifies that the Filters UI
// "Forward to" path (which compiles to a managed Sieve redirect) also
// delivers via the outbound queue. The script is the same compiled form
// as internal/sieve/compile_managed.go emits for a Forward action.
func TestSieveRedirect_FiltersUI_ManagedScript(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{envID: "env-redirect-ui"}
	f.srv.SetSubmissionQueue(q)

	// This is the compiled form of a Filters UI "Forward to" rule with
	// no conditions (always-forward). compile_managed.go emits exactly
	// this shape for a ForwardAction.
	script := `require ["fileinto","reject","vacation","imap4flags","copy"];
if true {
  redirect "forward-target@example.org";
  stop;
}
`
	if err := f.ha.Store.Meta().SetSieveScript(
		context.Background(), f.principal, script,
	); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: filters forward\r\n\r\nManagedForward.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1 (Filters UI Forward path)", len(calls))
	}
	if calls[0].Recipients[0] != "forward-target@example.org" {
		t.Errorf("redirect target = %q, want forward-target@example.org", calls[0].Recipients[0])
	}
	if !strings.HasPrefix(calls[0].MailFrom, "SRS0=") || !strings.HasSuffix(calls[0].MailFrom, "@example.test") {
		t.Errorf("MailFrom = %q, want an SRS0= address in example.test", calls[0].MailFrom)
	}
	if decoded := decodeSRSFromStore(t, f.ha.Store, f.ha.Clock, calls[0].MailFrom); decoded != "bob@sender.test" {
		t.Errorf("decoded MailFrom = %q, want bob@sender.test", decoded)
	}
}

// TestSieveRedirect_Copy_QueuesAndDeliversLocally verifies that redirect
// with :copy submits to the outbound queue AND delivers to the local INBOX
// (RFC 3894 §2, re #63).
func TestSieveRedirect_Copy_QueuesAndDeliversLocally(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{envID: "env-redirect-copy"}
	f.srv.SetSubmissionQueue(q)

	if err := f.ha.Store.Meta().SetSieveScript(
		context.Background(), f.principal,
		`require ["copy"]; redirect :copy "cc@example.com";`,
	); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

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
	body := "From: sender@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: copy forward\r\n\r\nKeep a local copy.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// Outbound queue must have received the redirect.
	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1", len(calls))
	}
	if calls[0].Recipients[0] != "cc@example.com" {
		t.Errorf("redirect target = %q, want cc@example.com", calls[0].Recipients[0])
	}

	// :copy keeps the local message in INBOX.
	assertMessageInMailbox(t, f, f.principal, "INBOX", "copy forward", "Keep a local copy.")
}

// TestSieveRedirect_LoopGuard verifies that redirect is suppressed when
// the message already carries maxSieveRedirectHops or more Received:
// headers. The suppressed message falls back to local INBOX delivery so
// no message is silently lost.
func TestSieveRedirect_LoopGuard(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)

	if err := f.ha.Store.Meta().SetSieveScript(
		context.Background(), f.principal,
		`redirect "external@example.com";`,
	); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}

	// Build a message with 25 Received: headers. The server adds one
	// more when storing the message, bringing the total to 26, which
	// exceeds maxSieveRedirectHops (25) and trips the loop guard.
	var hdrBuf strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&hdrBuf,
			"Received: from hop%d.example.com by mx%d.example.com; Mon, 01 Jan 2024 00:00:00 +0000\r\n",
			i, i)
	}
	body := hdrBuf.String() +
		"From: sender@sender.test\r\nTo: alice@example.test\r\n" +
		"Subject: loop buster\r\n\r\nLoop body.\r\n.\r\n"

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
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// Loop guard must suppress the redirect.
	if calls := q.Calls(); len(calls) != 0 {
		t.Errorf("loop guard: queue.Submit called %d times, want 0", len(calls))
	}

	// The suppressed redirect must not lose the message: fall back to
	// local INBOX delivery.
	assertMessageInMailbox(t, f, f.principal, "INBOX", "loop buster", "Loop body.")
}
