package protosmtp_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

// fakeWebhookDispatcher captures DispatchSynthetic calls so tests can
// assert the SMTP DATA-phase hand-off without standing up a real
// protowebhook.Dispatcher.
type fakeWebhookDispatcher struct {
	mu       sync.Mutex
	hooks    []store.Webhook
	calls    []protosmtp.SyntheticDispatch
	noMatch  bool
	failNext bool
}

func (f *fakeWebhookDispatcher) MatchingSyntheticHooks(_ context.Context, domain string) []store.Webhook {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noMatch {
		return nil
	}
	out := make([]store.Webhook, len(f.hooks))
	copy(out, f.hooks)
	_ = domain
	return out
}

func (f *fakeWebhookDispatcher) DispatchSynthetic(_ context.Context, in protosmtp.SyntheticDispatch, hooks []store.Webhook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return errInjectedDispatch
	}
	f.calls = append(f.calls, in)
	_ = hooks
	return nil
}

func (f *fakeWebhookDispatcher) Calls() []protosmtp.SyntheticDispatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protosmtp.SyntheticDispatch, len(f.calls))
	copy(out, f.calls)
	return out
}

var errInjectedDispatch = simpleErr("injected dispatch error")

// TestSynthetic_DispatchHandsOffToWebhookDispatcher exercises the
// Wave 3.5c-Z DATA-phase synthetic-recipient path: a plugin-accepted
// synthetic recipient with no principal_id triggers DispatchSynthetic
// with the route_tag, recipient, mail-from, and parsed body propagated
// to the webhook subsystem.
func TestSynthetic_DispatchHandsOffToWebhookDispatcher(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:42"}, nil
	}
	wd := &fakeWebhookDispatcher{
		hooks: []store.Webhook{{ID: 1, Active: true, OwnerKind: store.WebhookOwnerDomain, OwnerID: "app.example.com"}},
	}
	f.srv.SetWebhookDispatcher(wd)

	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("synthetic accept: got %d", code)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\n" +
		"To: reply+42@app.example.com\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"hello synthetic.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	if code, text := cli.readReply(t); code != 250 {
		t.Fatalf("DATA accept: got %d %s", code, text)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := wd.Calls()
	if len(calls) != 1 {
		t.Fatalf("DispatchSynthetic calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Recipient != "reply+42@app.example.com" {
		t.Errorf("Recipient = %q", c.Recipient)
	}
	if c.RouteTag != "ticket:42" {
		t.Errorf("RouteTag = %q", c.RouteTag)
	}
	if c.MailFrom != "bob@sender.test" {
		t.Errorf("MailFrom = %q", c.MailFrom)
	}
	if c.Domain != "app.example.com" {
		t.Errorf("Domain = %q", c.Domain)
	}
	if c.BlobHash == "" {
		t.Errorf("BlobHash empty")
	}
	if c.Size == 0 {
		t.Errorf("Size = 0")
	}
	text, _ := mailparse.ExtractBodyText(c.Parsed)
	if !strings.Contains(text, "hello synthetic") {
		t.Errorf("parsed body did not carry expected text: %q", text)
	}
}

// TestSynthetic_NoSubscription_AcceptsAndLogsAudit: the dispatcher
// returns an empty hook list; the SMTP layer still 250s and writes an
// audit row tagged "no_subscription".
func TestSynthetic_NoSubscription_AcceptsAndLogsAudit(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:42"}, nil
	}
	wd := &fakeWebhookDispatcher{noMatch: true}
	f.srv.SetWebhookDispatcher(wd)

	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("synthetic accept: got %d", code)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: bob@sender.test\r\nTo: reply+42@app.example.com\r\nSubject: x\r\n\r\nbody.\r\n.\r\n"))
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("DATA accept: got %d", code)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if got := len(wd.Calls()); got != 0 {
		t.Errorf("expected no DispatchSynthetic calls, got %d", got)
	}

	// Audit row "smtp.synthetic_accept" must record outcome=no_subscription.
	entries, err := f.ha.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "smtp.synthetic_accept", Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected smtp.synthetic_accept audit row")
	}
	if got := entries[0].Metadata["dispatch_outcome"]; got != "no_subscription" {
		t.Errorf("dispatch_outcome = %q, want no_subscription", got)
	}
}

// TestSynthetic_NoDispatcherWired_AcceptsAndLogsAudit: when the
// operator hasn't wired SetWebhookDispatcher (degraded deployment),
// the SMTP layer still 250s with a clearer audit signal.
func TestSynthetic_NoDispatcherWired_AcceptsAndLogsAudit(t *testing.T) {
	f := newRcptFixture(t, nil)
	f.inv.fn = func(_ context.Context, _ string, _ directory.ResolveRcptRequest) (directory.ResolveRcptResponse, error) {
		return directory.ResolveRcptResponse{Action: "accept", RouteTag: "ticket:42"}, nil
	}
	// No SetWebhookDispatcher call.

	cli := startRelayInSession(t, f)
	cli.send(t, "RCPT TO:<reply+42@app.example.com>")
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("synthetic accept: got %d", code)
	}
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte("From: bob@sender.test\r\nTo: reply+42@app.example.com\r\nSubject: x\r\n\r\nbody.\r\n.\r\n"))
	if code, _ := cli.readReply(t); code != 250 {
		t.Fatalf("DATA accept: got %d", code)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	entries, err := f.ha.Store.Meta().ListAuditLog(context.Background(),
		store.AuditLogFilter{Action: "smtp.synthetic_accept", Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected smtp.synthetic_accept audit row")
	}
	if got := entries[0].Metadata["dispatch_outcome"]; got != "no_dispatcher_wired" {
		t.Errorf("dispatch_outcome = %q, want no_dispatcher_wired", got)
	}
}
