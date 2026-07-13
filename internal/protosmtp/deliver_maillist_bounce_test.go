package protosmtp_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/dsn"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// capturingBounceProcessor wraps a real *maillist.BounceProcessor so
// tests exercise the actual token-verify/DSN-parse/classify pipeline
// end to end while still observing the result the SMTP layer got back.
type capturingBounceProcessor struct {
	inner *maillist.BounceProcessor
	mu    sync.Mutex
	calls []maillist.BounceResult
}

func (c *capturingBounceProcessor) ProcessBounce(ctx context.Context, in maillist.BounceInput) (maillist.BounceResult, error) {
	res, err := c.inner.ProcessBounce(ctx, in)
	c.mu.Lock()
	c.calls = append(c.calls, res)
	c.mu.Unlock()
	return res, err
}

func (c *capturingBounceProcessor) Calls() []maillist.BounceResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]maillist.BounceResult, len(c.calls))
	copy(out, c.calls)
	return out
}

const testBounceDSNBody = "Content-Type: multipart/report; report-type=delivery-status; boundary=\"B\"\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Delivery failed.\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mx.example.net\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; member1@example.net\r\n" +
	"Action: failed\r\n" +
	"Status: 5.1.1\r\n" +
	"Diagnostic-Code: smtp; 550 5.1.1 unknown user\r\n" +
	"\r\n" +
	"--B--\r\n"

// TestMailingListBounce_RCPT_RoutesToProcessorNotMailbox is the
// REQ-MLIST-50/51 end-to-end acceptance test: an inbound DSN addressed
// to a per-member VERP bounce token attributes to exactly that member
// and classifies the failure, driven through the real SMTP session
// state machine, and never lands in any principal's mailbox.
func TestMailingListBounce_RCPT_RoutesToProcessorNotMailbox(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ml := mustInsertTestMailingList(t, f.ha.Store, "announce@example.test")
	memberAddr := "member1@example.net"
	member, err := f.ha.Store.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:          ml.ID,
		ExternalAddress: &memberAddr,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	memberID := member.ID

	ts := maillist.NewTokenSigner([]byte("0123456789abcdef0123456789abcdef"))
	bp := &capturingBounceProcessor{inner: maillist.NewBounceProcessor(f.ha.Store.Meta(), ts, f.ha.Clock, f.ha.Logger)}
	f.srv.SetMailingListBounceProcessor(bp)

	verpAddr, err := maillist.VERPBounceAddress(ts, ml, memberID)
	if err != nil {
		t.Fatalf("VERPBounceAddress: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<"+verpAddr+">")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: mailer-daemon@relay.example.com\r\n" +
		"To: " + verpAddr + "\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n" +
		testBounceDSNBody + ".\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := bp.Calls()
	if len(calls) != 1 {
		t.Fatalf("ProcessBounce calls = %d, want 1", len(calls))
	}
	res := calls[0]
	if !res.Attributed {
		t.Fatalf("Attributed = false, want true")
	}
	if res.MemberID != memberID {
		t.Fatalf("MemberID = %d, want %d", res.MemberID, memberID)
	}
	if res.Classification != dsn.ClassificationHard {
		t.Fatalf("Classification = %v, want Hard", res.Classification)
	}

	// Must never deliver to any principal's mailbox.
	mbs, _ := f.ha.Store.Meta().ListMailboxes(context.Background(), f.principal)
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			msgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{Limit: 5})
			if len(msgs) != 0 {
				t.Errorf("a VERP bounce must not deliver locally to alice's INBOX, found %d messages", len(msgs))
			}
		}
	}
}

// TestMailingListBounce_ForgedToken_NoAttribution exercises the
// REQ-MLIST-52 fail-closed contract end to end: a bounce address whose
// token is well-formed base64 but does not verify (a forged/guessed
// token, or one signed with a different key) is accepted (250, no SMTP
// error surfaced -- rejecting would let a prober distinguish valid from
// invalid bounce addresses) but produces Attributed=false.
func TestMailingListBounce_ForgedToken_NoAttribution(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ml := mustInsertTestMailingList(t, f.ha.Store, "announce@example.test")
	mustAddTestListMember(t, f.ha.Store, ml.ID, "member1@example.net")

	ts := maillist.NewTokenSigner([]byte("0123456789abcdef0123456789abcdef"))
	otherTS := maillist.NewTokenSigner([]byte("ffffffffffffffffffffffffffffffff"))
	bp := &capturingBounceProcessor{inner: maillist.NewBounceProcessor(f.ha.Store.Meta(), ts, f.ha.Clock, f.ha.Logger)}
	f.srv.SetMailingListBounceProcessor(bp)

	// Minted with a DIFFERENT key -- must not verify against ts.
	forgedAddr, err := maillist.VERPBounceAddress(otherTS, ml, 1)
	if err != nil {
		t.Fatalf("VERPBounceAddress: %v", err)
	}

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<"+forgedAddr+">")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: mailer-daemon@relay.example.com\r\n" +
		"To: " + forgedAddr + "\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n" +
		testBounceDSNBody + ".\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := bp.Calls()
	if len(calls) != 1 {
		t.Fatalf("ProcessBounce calls = %d, want 1", len(calls))
	}
	if calls[0].Attributed {
		t.Fatalf("Attributed = true for a forged token, want false")
	}
}

// TestMailingListBounce_S1BounceAddress_NotRoutedAsVERP verifies the
// plain S1 "<list>+bounce@domain" address (no token suffix) is NOT
// recognised as a VERP bounce shape -- it falls through to ordinary
// resolution rather than reaching the bounce processor, preserving the
// existing S1 behavior for deployments that have not enabled VERP.
func TestMailingListBounce_S1BounceAddress_NotRoutedAsVERP(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	mustInsertTestMailingList(t, f.ha.Store, "announce@example.test")

	ts := maillist.NewTokenSigner([]byte("0123456789abcdef0123456789abcdef"))
	bp := &capturingBounceProcessor{inner: maillist.NewBounceProcessor(f.ha.Store.Meta(), ts, f.ha.Clock, f.ha.Logger)}
	f.srv.SetMailingListBounceProcessor(bp)

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO relay.example.com")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<>")
	mustOK(t, cli, 250)
	// The S1 address has no "-<token>" suffix, so ParseVERPBounceLocalPart
	// does not recognise it; it falls through to the ordinary resolution
	// chain (which happens to accept it here via the RFC 5233
	// sub-addressing fallback onto the list's own backing Group
	// principal -- an S1 behavior this change does not touch). The point
	// of this test is that the bounce processor is never consulted.
	cli.send(t, "RCPT TO:<announce+bounce@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if calls := bp.Calls(); len(calls) != 0 {
		t.Fatalf("ProcessBounce calls = %d, want 0 (S1 address must not match the VERP shape)", len(calls))
	}
}
