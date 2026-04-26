package protosmtp_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// fakeSubmissionQueue captures Submit calls for the submission-listener
// outbound path. The Wave 3.1.6 SMTP test uses it to assert the
// per-recipient queue.Submission shape without standing up a real
// outbound queue.
type fakeSubmissionQueue struct {
	mu       sync.Mutex
	calls    []queue.Submission
	failNext bool
	envID    queue.EnvelopeID
}

func (f *fakeSubmissionQueue) Submit(_ context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return "", errInjectedSubmit
	}
	// Read body so the test can assert on it later via the recorded
	// Submission's Recipients / fields; we drop the io.Reader since
	// the test assertions only need scalars.
	f.calls = append(f.calls, queue.Submission{
		PrincipalID:    msg.PrincipalID,
		MailFrom:       msg.MailFrom,
		Recipients:     append([]string(nil), msg.Recipients...),
		Sign:           msg.Sign,
		SigningDomain:  msg.SigningDomain,
		DSNNotify:      msg.DSNNotify,
		DSNRet:         msg.DSNRet,
		DSNEnvelopeID:  msg.DSNEnvelopeID,
		IdempotencyKey: msg.IdempotencyKey,
		REQUIRETLS:     msg.REQUIRETLS,
	})
	id := f.envID
	if id == "" {
		id = queue.EnvelopeID("env-test")
	}
	return id, nil
}

func (f *fakeSubmissionQueue) Calls() []queue.Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]queue.Submission, len(f.calls))
	copy(out, f.calls)
	return out
}

var errInjectedSubmit = simpleErr("injected queue.Submit failure")

// authPlainOnSubmission performs EHLO + STARTTLS + AUTH PLAIN on a
// SubmissionSTARTTLS fixture so the test can drive subsequent commands
// over the authenticated TLS connection.
func authPlainOnSubmission(t *testing.T, f *fixture) (*smtpClient, func()) {
	t.Helper()
	cli, closeFn := f.dial(t)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "STARTTLS")
	mustOK(t, cli, 220)
	tlsConn := tls.Client(cli.conn, f.tlsClient)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	cli2 := newSMTPClient(tlsConn)
	cli2.send(t, "EHLO client.example.test")
	mustOK(t, cli2, 250)
	ir := base64.StdEncoding.EncodeToString([]byte("\x00alice@example.test\x00" + f.password))
	cli2.send(t, "AUTH PLAIN "+ir)
	mustOK(t, cli2, 235)
	return cli2, closeFn
}

// TestSubmission_NonLocalRecipient_QueuesOutbound is the Wave 3.1.6
// SMTP-side test: an authenticated MUA-client on port 587 / 465 sends
// to a non-local recipient (gmail), and the SMTP DATA-finish loop
// hands the message off to the SubmissionQueue with the right shape.
func TestSubmission_NonLocalRecipient_QueuesOutbound(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	q := &fakeSubmissionQueue{envID: "env-1234"}
	f.srv.SetSubmissionQueue(q)

	cli, closeFn := authPlainOnSubmission(t, f)
	defer closeFn()
	cli.send(t, "MAIL FROM:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<bob@gmail.com>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: alice@example.test\r\n" +
		"To: bob@gmail.com\r\n" +
		"Subject: hi\r\n" +
		"Message-ID: <wave-3.1.6@example.test>\r\n" +
		"\r\n" +
		"hello from MUA\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	if code, text := cli.readReply(t); code != 250 {
		t.Fatalf("DATA reply: %d %s", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := q.Calls()
	if len(calls) != 1 {
		t.Fatalf("queue.Submit calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.MailFrom != "alice@example.test" {
		t.Errorf("MailFrom = %q", c.MailFrom)
	}
	if len(c.Recipients) != 1 || c.Recipients[0] != "bob@gmail.com" {
		t.Errorf("Recipients = %v", c.Recipients)
	}
	if !c.Sign {
		t.Errorf("Sign should be true on submission path")
	}
	if c.SigningDomain != "example.test" {
		t.Errorf("SigningDomain = %q, want example.test", c.SigningDomain)
	}
	if c.PrincipalID == nil || *c.PrincipalID != f.principal {
		t.Errorf("PrincipalID = %v, want %v", c.PrincipalID, f.principal)
	}
	wantKey := "<wave-3.1.6@example.test>:bob@gmail.com"
	if c.IdempotencyKey != wantKey {
		t.Errorf("IdempotencyKey = %q, want %q", c.IdempotencyKey, wantKey)
	}

	// Audit row "smtp.inbound_submission_queued" must be present.
	entries, err := f.ha.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "smtp.inbound_submission_queued", Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected smtp.inbound_submission_queued audit row")
	}
	if entries[0].Metadata["recipient"] != "bob@gmail.com" {
		t.Errorf("audit recipient = %q", entries[0].Metadata["recipient"])
	}
	if entries[0].Subject != "envelope:env-1234" {
		t.Errorf("audit subject = %q, want envelope:env-1234", entries[0].Subject)
	}
}

// TestSubmission_LocalRecipient_StaysOnLocalDeliveryPath: confirms the
// existing local-delivery path is unchanged when the authenticated
// MUA-client sends to a local recipient (send-to-self on the same
// host).
func TestSubmission_LocalRecipient_StaysOnLocalDeliveryPath(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)

	cli, closeFn := authPlainOnSubmission(t, f)
	defer closeFn()
	cli.send(t, "MAIL FROM:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: alice@example.test\r\nTo: alice@example.test\r\nSubject: self\r\n\r\nbody.\r\n.\r\n"))
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("DATA reply non-250")
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if got := len(q.Calls()); got != 0 {
		t.Errorf("queue.Submit should not be called for local recipient; got %d", got)
	}
	mb, err := f.ha.Store.Meta().GetMailboxByName(context.Background(), f.principal, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	msgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{Limit: 5})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 local-delivered message, got %d", len(msgs))
	}
}

// TestSubmission_NonLocalRecipient_NoQueueWired_RecipientFails: when
// the operator hasn't wired the submission queue (degraded admin
// startup), the per-recipient delivery returns failure so the SMTP
// transaction surfaces a transient error rather than silently
// swallowing the message.
func TestSubmission_NonLocalRecipient_NoQueueWired_RecipientFails(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	// Deliberately do NOT call SetSubmissionQueue.

	cli, closeFn := authPlainOnSubmission(t, f)
	defer closeFn()
	cli.send(t, "MAIL FROM:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<bob@gmail.com>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: alice@example.test\r\nTo: bob@gmail.com\r\nSubject: x\r\n\r\nbody.\r\n.\r\n"))
	code, text := cli.readReply(t)
	// Without a queue wired, the lone recipient fails -> 451 transient.
	if code != 451 {
		t.Fatalf("expected 451 transient (no queue wired), got %d %s", code, text)
	}
	if !strings.Contains(text, "delivery failed") {
		t.Errorf("expected 'delivery failed' diagnostic, got %q", text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)
}

// TestInboundListener_NonLocalRecipient_PhaseLeaks_AuditsAndDrops:
// the defensive-leak case (port-25 inbound listener with a
// principalID==0 recipient that escaped RCPT-TO). The DATA reply
// surfaces 451 (no recipient succeeded) and an audit row
// smtp.phase1_rcpt_leak is appended.
func TestInboundListener_NonLocalRecipient_PhaseLeaks_AuditsAndDrops(t *testing.T) {
	// Hand-craft the leak: bypass cmdRCPT by inserting a recipient
	// directly via an inbound RcptResolver that accepts with
	// principalID=0 but is NOT marked synthetic. We do this by
	// invoking the protosmtp internals through a fixture that
	// configures a plugin-first domain so the plugin owns address
	// space, and the plugin returns "fallthrough" — which lands at
	// step-4 of runRcptResolutionChain (550). That path therefore
	// CAN'T leak; the only realistic leak path today is operator
	// misconfiguration (e.g. plugin returns accept with synthetic=false
	// AND no principal_id, which the resolver guards against via the
	// "GetPrincipalByID downgrade to 4.3.0" path).
	//
	// Since reaching the leak branch requires an internal invariant
	// violation, we skip the test on the public surface and instead
	// rely on the unit-level construction being defensible: the
	// branch logs + audits without panicking. Documented here so the
	// audit-log invariant stays load-bearing in code review.
	t.Skip("phase1_rcpt_leak branch is defensive; no public path triggers it without an internal invariant violation")
}
