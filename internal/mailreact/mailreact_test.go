package mailreact_test

// Tests for the outbound reaction email dispatch (REQ-FLOW-100..103).

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailreact"
	"github.com/hanshuebner/herold/internal/queue"
)

// fakeDispatcher captures submitted queue items for inspection.
type fakeDispatcher struct {
	mu          sync.Mutex
	submissions []queue.Submission
	bodies      []string
}

func (f *fakeDispatcher) Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	// Read body now so caller can close the reader later.
	b, _ := io.ReadAll(msg.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	msg.Body = bytes.NewReader(b)
	f.submissions = append(f.submissions, msg)
	f.bodies = append(f.bodies, string(b))
	return "", nil
}

func (f *fakeDispatcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submissions)
}

func (f *fakeDispatcher) get(i int) (queue.Submission, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submissions[i], f.bodies[i]
}

// makeMailer builds a Mailer with the given local domain set.
func makeMailer(t *testing.T, localDomains []string, disp mailreact.Dispatcher) *mailreact.Mailer {
	t.Helper()
	local := make(map[string]bool, len(localDomains))
	for _, d := range localDomains {
		local[strings.ToLower(d)] = true
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	return mailreact.New(mailreact.Options{
		LocalDomainFn: func(_ context.Context, domain string) bool {
			return local[strings.ToLower(domain)]
		},
		Dispatcher: disp,
		Hostname:   "mail.example.test",
		Clock:      clk,
	})
}

// TestMailreact_AllLocalRecipients verifies that when every recipient is on a
// local domain, no queue item is enqueued (REQ-FLOW-100).
func TestMailreact_AllLocalRecipients(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	reactor := mailreact.ReactorInfo{
		PrincipalID: 1,
		Address:     "alice@local.test",
		DisplayName: "Alice",
		Domain:      "local.test",
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:     "abc123@local.test",
		Subject:       "Hello",
		AllRecipients: []string{"bob@local.test", "carol@local.test"},
	}

	n, err := m.BuildAndEnqueue(context.Background(), reactor, "thumbs-up", orig)
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 enqueued, got %d", n)
	}
	if disp.count() != 0 {
		t.Errorf("want 0 submissions, got %d", disp.count())
	}
}

// TestMailreact_AllExternalRecipients verifies that all external recipients
// receive a reaction email in one Submit call (REQ-FLOW-101).
func TestMailreact_AllExternalRecipients(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	reactor := mailreact.ReactorInfo{
		PrincipalID: 1,
		Address:     "alice@local.test",
		DisplayName: "Alice",
		Domain:      "local.test",
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:     "orig-id@local.test",
		Subject:       "Hi there",
		AllRecipients: []string{"ext1@remote1.test", "ext2@remote2.test"},
	}

	n, err := m.BuildAndEnqueue(context.Background(), reactor, "heart", orig)
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 enqueued, got %d", n)
	}
	if disp.count() != 1 {
		// BuildAndEnqueue makes a single Submission with all recipients.
		t.Fatalf("want 1 submission, got %d", disp.count())
	}
	sub, body := disp.get(0)
	if sub.MailFrom != reactor.Address {
		t.Errorf("MailFrom = %q, want %q", sub.MailFrom, reactor.Address)
	}
	if len(sub.Recipients) != 2 {
		t.Errorf("recipients = %v, want 2", sub.Recipients)
	}
	if !sub.Sign {
		t.Error("Sign should be true")
	}
	if sub.SigningDomain != "local.test" {
		t.Errorf("SigningDomain = %q, want local.test", sub.SigningDomain)
	}

	// Body must contain the X-Herold-Reaction-* headers.
	if !strings.Contains(body, "X-Herold-Reaction-To: <orig-id@local.test>") {
		t.Errorf("body missing X-Herold-Reaction-To: %s", body)
	}
	if !strings.Contains(body, "X-Herold-Reaction-Emoji: heart") {
		t.Errorf("body missing X-Herold-Reaction-Emoji: %s", body)
	}
	if !strings.Contains(body, "X-Herold-Reaction-Action: add") {
		t.Errorf("body missing X-Herold-Reaction-Action: %s", body)
	}
}

// TestMailreact_MixedRecipients verifies that only external recipients are
// enqueued while local ones are skipped (REQ-FLOW-100 + REQ-FLOW-101).
func TestMailreact_MixedRecipients(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	reactor := mailreact.ReactorInfo{
		PrincipalID: 2,
		Address:     "bob@local.test",
		DisplayName: "Bob",
		Domain:      "local.test",
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:     "mixed@local.test",
		Subject:       "Mixed",
		AllRecipients: []string{"local@local.test", "remote@remote.test"},
	}

	n, err := m.BuildAndEnqueue(context.Background(), reactor, "fire", orig)
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 enqueued (only the remote one), got %d", n)
	}
	if disp.count() != 1 {
		t.Fatalf("want 1 submission, got %d", disp.count())
	}
	sub, _ := disp.get(0)
	if len(sub.Recipients) != 1 || sub.Recipients[0] != "remote@remote.test" {
		t.Errorf("recipients = %v, want [remote@remote.test]", sub.Recipients)
	}
}

// TestMailreact_ReactionBodyContents verifies the RFC 5322 body structure:
// multipart/alternative with text/plain and text/html parts, correct
// In-Reply-To, References, Subject prefix.
func TestMailreact_ReactionBodyContents(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	reactor := mailreact.ReactorInfo{
		PrincipalID: 3,
		Address:     "carol@local.test",
		DisplayName: "Carol",
		Domain:      "local.test",
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:     "origin@elsewhere.test",
		Subject:       "Original Subject",
		References:    "<prev@elsewhere.test>",
		AllRecipients: []string{"ext@other.test"},
	}

	_, err := m.BuildAndEnqueue(context.Background(), reactor, "wave", orig)
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	if disp.count() != 1 {
		t.Fatalf("want 1 submission, got %d", disp.count())
	}
	_, body := disp.get(0)

	checks := []string{
		"In-Reply-To: <origin@elsewhere.test>",
		"References: <prev@elsewhere.test> <origin@elsewhere.test>",
		"Subject: Re: Original Subject",
		"From: carol@local.test",
		"text/plain",
		"text/html",
		"multipart/alternative",
		"Carol reacted with wave",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; body:\n%s", want, body)
		}
	}
}

// TestMailreact_SubjectAlreadyHasRePrefix verifies the subject is not
// double-prefixed when it already starts with "Re:".
func TestMailreact_SubjectAlreadyHasRePrefix(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	reactor := mailreact.ReactorInfo{
		PrincipalID: 4,
		Address:     "dave@local.test",
		DisplayName: "Dave",
		Domain:      "local.test",
	}
	orig := mailreact.OriginalEmailInfo{
		MessageID:     "re-test@local.test",
		Subject:       "Re: Already reply",
		AllRecipients: []string{"ext@other.test"},
	}

	_, err := m.BuildAndEnqueue(context.Background(), reactor, "ok", orig)
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	_, body := disp.get(0)
	if strings.Contains(body, "Re: Re:") {
		t.Errorf("subject double-prefixed: %s", body)
	}
	if !strings.Contains(body, "Subject: Re: Already reply") {
		t.Errorf("subject not preserved: %s", body)
	}
}

// TestMailreact_EmptyRecipientList verifies that an empty AllRecipients
// slice returns 0 and does not panic.
func TestMailreact_EmptyRecipientList(t *testing.T) {
	disp := &fakeDispatcher{}
	m := makeMailer(t, []string{"local.test"}, disp)

	n, err := m.BuildAndEnqueue(context.Background(), mailreact.ReactorInfo{
		PrincipalID: 5,
		Address:     "eve@local.test",
		Domain:      "local.test",
	}, "star", mailreact.OriginalEmailInfo{
		MessageID:     "empty@local.test",
		AllRecipients: nil,
	})
	if err != nil {
		t.Fatalf("BuildAndEnqueue: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0, got %d", n)
	}
}
