package protosmtp_test

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeBouncePoster captures BouncePoster calls so tests can assert
// the inbound DSN-emission contract from REQ-FLOW-ATTPOL-02 without
// dragging in the queue subsystem.
type fakeBouncePoster struct {
	mu     sync.Mutex
	calls  []protosmtp.BounceInput
	failOn map[string]bool // FinalRcpt -> always fail
}

func (f *fakeBouncePoster) PostBounce(_ context.Context, in protosmtp.BounceInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.failOn != nil && f.failOn[in.FinalRcpt] {
		return errInjectedBounce
	}
	return nil
}

func (f *fakeBouncePoster) Calls() []protosmtp.BounceInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protosmtp.BounceInput, len(f.calls))
	copy(out, f.calls)
	return out
}

var errInjectedBounce = simpleErr("injected bounce failure")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

// attpolFixture wraps the standard server_test fixture and additionally
// configures a BouncePoster so the post-acceptance walker has somewhere
// to enqueue.
type attpolFixture struct {
	*fixture
	bp *fakeBouncePoster
}

func newAttPolFixture(t *testing.T) *attpolFixture {
	t.Helper()
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	bp := &fakeBouncePoster{}
	f.srv.SetBouncePoster(bp)
	return &attpolFixture{fixture: f, bp: bp}
}

// configureAttPol writes a per-recipient policy row.
func (f *attpolFixture) configureAttPol(t *testing.T, addr string, p store.InboundAttachmentPolicy) {
	t.Helper()
	err := f.ha.Store.Meta().SetInboundAttachmentPolicyRecipient(
		context.Background(), addr,
		store.InboundAttachmentPolicyRow{Policy: p},
	)
	if err != nil {
		t.Fatalf("SetInboundAttachmentPolicyRecipient: %v", err)
	}
}

// configureAttPolDomain writes a per-domain policy row.
func (f *attpolFixture) configureAttPolDomain(t *testing.T, domain string, p store.InboundAttachmentPolicy) {
	t.Helper()
	err := f.ha.Store.Meta().SetInboundAttachmentPolicyDomain(
		context.Background(), domain,
		store.InboundAttachmentPolicyRow{Policy: p},
	)
	if err != nil {
		t.Fatalf("SetInboundAttachmentPolicyDomain: %v", err)
	}
}

// drive runs one inbound DATA exchange from MAIL FROM through the
// final post-DATA reply. Returns the (code, text) of the final reply.
func (f *attpolFixture) drive(t *testing.T, mailFrom string, rcpts []string, body string) (int, string) {
	t.Helper()
	c, err := f.ha.DialSMTPByName(context.Background(), f.listener)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<"+mailFrom+">")
	mustOK(t, cli, 250)
	for _, rc := range rcpts {
		cli.send(t, "RCPT TO:<"+rc+">")
		mustOK(t, cli, 250)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte(body))
	cli.sendRaw(t, []byte(".\r\n"))
	code, text := cli.readReply(t)
	cli.send(t, "QUIT")
	return code, text
}

func bodyMultipartMixed() string {
	return "From: bob@sender.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: with attachment\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n" +
		"\r\n" +
		"--b\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"text body\r\n" +
		"--b\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"x.pdf\"\r\n" +
		"\r\n" +
		"PDF-bytes-here\r\n" +
		"--b--\r\n"
}

func bodyTextPlain() string {
	return "From: bob@sender.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: just text\r\n" +
		"\r\n" +
		"plain body\r\n"
}

func bodyNestedHiddenAttachment() string {
	return "From: bob@sender.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: nested\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"plain\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/mixed; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>html</p>\r\n" +
		"--inner\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"hidden.pdf\"\r\n" +
		"\r\n" +
		"PDF-bytes\r\n" +
		"--inner--\r\n" +
		"--outer--\r\n"
}

// TestAttPol_HeaderOnly_RejectsMultipartMixed exercises the
// REQ-FLOW-ATTPOL-01 happy path: alice has reject_at_data set, the
// inbound message is multipart/mixed with a PDF attachment, the
// header-only check refuses with 552 5.3.4 and the message is NOT
// delivered.
func TestAttPol_HeaderOnly_RejectsMultipartMixed(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPol(t, "alice@example.test", store.AttPolicyRejectAtData)

	code, text := f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyMultipartMixed())
	if code != 552 {
		t.Fatalf("expected 552, got %d: %s", code, text)
	}
	if !strings.Contains(text, "5.3.4") {
		t.Errorf("expected enhanced status 5.3.4 in reply: %s", text)
	}
	if !strings.Contains(text, "attachments not accepted") {
		t.Errorf("expected default reject text in reply: %s", text)
	}

	mb, err := f.ha.Store.Meta().GetMailboxByName(context.Background(), f.principal, "INBOX")
	if err == nil {
		msgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{Limit: 5})
		if len(msgs) != 0 {
			t.Errorf("expected no delivered message; got %d", len(msgs))
		}
	}

	got := testutil.ToFloat64(observe.SMTPInboundAttachmentPolicyTotal.WithLabelValues(
		"example.test", "refused_at_data"))
	if got != 1 {
		t.Errorf("refused_at_data counter = %v; want 1", got)
	}
}

// TestAttPol_HeaderOnly_AcceptsTextPlain confirms a plain text
// message passes the header-only check even when the recipient has
// reject_at_data set, and that the "passed" outcome is recorded.
func TestAttPol_HeaderOnly_AcceptsTextPlain(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPol(t, "alice@example.test", store.AttPolicyRejectAtData)

	code, text := f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyTextPlain())
	if code != 250 {
		t.Fatalf("expected 250, got %d: %s", code, text)
	}

	got := testutil.ToFloat64(observe.SMTPInboundAttachmentPolicyTotal.WithLabelValues(
		"example.test", "passed"))
	if got != 1 {
		t.Errorf("passed counter = %v; want 1", got)
	}
}

// TestAttPol_PostAcceptance_RejectsHiddenAttachment exercises
// REQ-FLOW-ATTPOL-02: the header-only check passes (top-level is
// multipart/alternative), but the deep walker finds an attachment
// nested under multipart/mixed and refuses the recipient. The
// message-wide DATA still returns 250 (accepted at the protocol
// layer), the per-recipient delivery is dropped, the BouncePoster
// receives one call, and the audit + metric record
// refused_post_acceptance.
func TestAttPol_PostAcceptance_RejectsHiddenAttachment(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPol(t, "alice@example.test", store.AttPolicyRejectAtData)

	code, text := f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyNestedHiddenAttachment())
	if code != 250 {
		t.Fatalf("expected 250 (DATA accepted), got %d: %s", code, text)
	}

	calls := f.bp.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 BouncePoster call, got %d", len(calls))
	}
	c := calls[0]
	if c.MailFrom != "bob@sender.test" {
		t.Errorf("BouncePoster.MailFrom = %q; want bob@sender.test", c.MailFrom)
	}
	if c.FinalRcpt != "alice@example.test" {
		t.Errorf("BouncePoster.FinalRcpt = %q; want alice@example.test", c.FinalRcpt)
	}
	if !strings.Contains(c.DiagnosticCode, "5.3.4") {
		t.Errorf("BouncePoster.DiagnosticCode = %q; want substring 5.3.4", c.DiagnosticCode)
	}

	mb, err := f.ha.Store.Meta().GetMailboxByName(context.Background(), f.principal, "INBOX")
	if err == nil {
		msgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{Limit: 5})
		if len(msgs) != 0 {
			t.Errorf("expected no delivered message; got %d", len(msgs))
		}
	}

	got := testutil.ToFloat64(observe.SMTPInboundAttachmentPolicyTotal.WithLabelValues(
		"example.test", "refused_post_acceptance"))
	if got != 1 {
		t.Errorf("refused_post_acceptance counter = %v; want 1", got)
	}
}

// TestAttPol_DomainInheritance_RecipientFallsBackToDomain confirms
// that a per-domain policy applies to recipients without an
// explicit per-recipient row.
func TestAttPol_DomainInheritance_RecipientFallsBackToDomain(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPolDomain(t, "example.test", store.AttPolicyRejectAtData)

	code, text := f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyMultipartMixed())
	if code != 552 {
		t.Fatalf("expected 552, got %d: %s", code, text)
	}
}

// TestAttPol_RecipientOverridesDomain confirms explicit > inherited
// precedence: an "accept" recipient row overrides a "reject_at_data"
// domain row.
func TestAttPol_RecipientOverridesDomain(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPolDomain(t, "example.test", store.AttPolicyRejectAtData)
	f.configureAttPol(t, "alice@example.test", store.AttPolicyAccept)

	code, text := f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyMultipartMixed())
	if code != 250 {
		t.Fatalf("expected 250 (recipient overrides domain to accept), got %d: %s", code, text)
	}
}

// TestAttPol_MultiRecipient_MixedAcceptReject exercises the
// per-recipient SMTP-552 contract from the spec: with two
// recipients, one whose policy is reject_at_data, the message-wide
// DATA still returns 250 (one accepting recipient is enough), the
// rejecting recipient gets a bounce DSN, and the accepting
// recipient's mailbox sees the message delivered.
func TestAttPol_MultiRecipient_MixedAcceptReject(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	// Add a second principal: bob@example.test has the default
	// (accept) policy; alice has reject_at_data.
	dir := directory.New(f.ha.Store.Meta(), f.ha.Logger, f.ha.Clock, rand.Reader)
	bobPID, err := dir.CreatePrincipal(context.Background(), "bob@example.test",
		"correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal bob: %v", err)
	}
	f.configureAttPol(t, "alice@example.test", store.AttPolicyRejectAtData)

	code, text := f.drive(t, "sender@external.test",
		[]string{"alice@example.test", "bob@example.test"}, bodyMultipartMixed())
	if code != 250 {
		t.Fatalf("expected 250 (mixed acceptance), got %d: %s", code, text)
	}

	// Bounce DSN was enqueued for alice.
	calls := f.bp.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 BouncePoster call, got %d", len(calls))
	}
	if calls[0].FinalRcpt != "alice@example.test" {
		t.Errorf("BouncePoster.FinalRcpt = %q; want alice@example.test", calls[0].FinalRcpt)
	}

	// Bob's INBOX has the message; alice's does not.
	bobMB, err := f.ha.Store.Meta().GetMailboxByName(context.Background(), bobPID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName bob: %v", err)
	}
	bobMsgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), bobMB.ID, store.MessageFilter{Limit: 5})
	if len(bobMsgs) != 1 {
		t.Errorf("bob's INBOX = %d messages; want 1", len(bobMsgs))
	}
	aliceMB, err := f.ha.Store.Meta().GetMailboxByName(context.Background(), f.principal, "INBOX")
	if err == nil {
		aliceMsgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), aliceMB.ID, store.MessageFilter{Limit: 5})
		if len(aliceMsgs) != 0 {
			t.Errorf("alice's INBOX = %d messages; want 0", len(aliceMsgs))
		}
	}
}

// TestAttPol_AuditLog_RecordsOutcome verifies the audit log carries
// one row per message with attpol_outcome set.
func TestAttPol_AuditLog_RecordsOutcome(t *testing.T) {
	observe.RegisterSMTPAttachmentPolicyMetrics()
	resetAttPolCounter(t, "example.test")

	f := newAttPolFixture(t)
	f.configureAttPol(t, "alice@example.test", store.AttPolicyRejectAtData)

	_, _ = f.drive(t, "bob@sender.test",
		[]string{"alice@example.test"}, bodyMultipartMixed())

	entries, err := f.ha.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "smtp.attpol", Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one smtp.attpol audit row")
	}
	got := entries[0].Metadata["attpol_outcome"]
	if got != "refused_at_data" {
		t.Errorf("attpol_outcome = %q; want refused_at_data", got)
	}
	if entries[0].Metadata["recipient_domain"] != "example.test" {
		t.Errorf("recipient_domain = %q; want example.test", entries[0].Metadata["recipient_domain"])
	}
	if entries[0].Outcome != store.OutcomeFailure {
		t.Errorf("audit Outcome = %v; want OutcomeFailure", entries[0].Outcome)
	}
}

// resetAttPolCounter zeroes the per-domain counter for one outcome
// triple so tests that share the process Registry stay isolated.
// Resetting individual labels would require a full Reset; instead we
// rely on the test ordering and the fact that newFixture creates a
// fresh harness, which still shares the global Registry. Using
// testutil.CollectAndCount is enough for the assertions above.
func resetAttPolCounter(t *testing.T, domain string) {
	t.Helper()
	if observe.SMTPInboundAttachmentPolicyTotal == nil {
		return
	}
	observe.SMTPInboundAttachmentPolicyTotal.DeleteLabelValues(domain, "passed")
	observe.SMTPInboundAttachmentPolicyTotal.DeleteLabelValues(domain, "refused_at_data")
	observe.SMTPInboundAttachmentPolicyTotal.DeleteLabelValues(domain, "refused_post_acceptance")
}
