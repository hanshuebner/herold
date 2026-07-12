package protosmtp_test

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/srs"
)

// TestSRSBounce_ValidAddress_RelaysToExternalOriginalSender verifies the
// decode half of issue #204: an inbound message (typically an RFC 3464
// DSN bounce) addressed to a valid SRS return-path this server issued
// decodes to the original sender and, when that sender is outside this
// deployment, is relayed there via queueForward. The bounce's own null
// return-path ("MAIL FROM:<>") is preserved unchanged (SRS never wraps
// a null sender).
func TestSRSBounce_ValidAddress_RelaysToExternalOriginalSender(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{envID: "env-srs-bounce-1"}
	f.srv.SetSubmissionQueue(q)

	ctx := context.Background()
	secretRow, err := f.ha.Store.Meta().InsertSRSSecret(ctx, []byte("srs-bounce-test-secret-material!"))
	if err != nil {
		t.Fatalf("InsertSRSSecret: %v", err)
	}
	srsLocal := srs.EncodeSRS0(secretRow.Secret, f.ha.Clock.Now(), "originalsender", "external.example")

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<"+srsLocal+"@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: mailer-daemon@relay.example.com\r\nTo: " + srsLocal + "@example.test\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n\r\nBounce body.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.MailFrom != "" {
		t.Errorf("MailFrom = %q, want empty (null sender preserved for a relayed bounce)", c.MailFrom)
	}
	if len(c.Recipients) != 1 || c.Recipients[0] != "originalsender@external.example" {
		t.Errorf("Recipients = %v, want [originalsender@external.example]", c.Recipients)
	}
}

// TestSRSBounce_ValidAddress_DeliversToLocalOriginalSender verifies that
// when the decoded original sender is one of THIS server's own
// principals (a local user's own address was the one that got forwarded
// out and bounced back), the bounce is delivered to their INBOX rather
// than relayed externally.
func TestSRSBounce_ValidAddress_DeliversToLocalOriginalSender(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)

	ctx := context.Background()
	secretRow, err := f.ha.Store.Meta().InsertSRSSecret(ctx, []byte("srs-bounce-local-secret-material"))
	if err != nil {
		t.Fatalf("InsertSRSSecret: %v", err)
	}
	// alice@example.test is the fixture's own principal (see newFixture).
	srsLocal := srs.EncodeSRS0(secretRow.Secret, f.ha.Clock.Now(), "alice", "example.test")

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<"+srsLocal+"@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: mailer-daemon@relay.example.com\r\nTo: " + srsLocal + "@example.test\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n\r\nBounce for alice.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if calls := q.Calls(); len(calls) != 0 {
		t.Errorf("a decoded LOCAL original sender must deliver locally, not relay via queueForward; got %d queue calls", len(calls))
	}
	assertMessageInMailbox(t, f, f.principal, "INBOX", "Undelivered Mail Returned to Sender", "Bounce for alice.")
}

// TestSRSBounce_InvalidAddress_Rejected verifies that a forged (bad
// hash) SRS-shaped recipient is rejected at RCPT time (550) rather than
// falling through to some other resolution or being silently accepted.
func TestSRSBounce_InvalidAddress_Rejected(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<SRS0=XXXX=AA=external.example=someone@example.test>")
	mustOK(t, cli, 550)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if calls := q.Calls(); len(calls) != 0 {
		t.Errorf("invalid SRS address must not queue anything, got %d calls", len(calls))
	}
}
