package spam

// backscatter_test.go covers re #513: a delivery-status report
// (multipart/report; report-type=delivery-status, RFC 3464) enclosing a
// message the owner never sent must resolve to Spam deterministically,
// without depending on the model's reading of the notice text (message
// 4247's reproduction: verdict ham, score 0.05, no spam_signals at all).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// backscatterReportRaw mirrors message 4247's shape: a delivery-status
// report from a forwarder's Exim, enclosing (as message/rfc822) a
// marketing message from a foreign sender that herold itself rejected
// at DATA. The enclosed original's From and Return-Path both name
// bounces@ingeasoto.com, which is not one of the principal's own
// addresses.
const backscatterReportRaw = "From: Mail Delivery System <Mailer-Daemon@www97.your-server.de>\r\n" +
	"To: hans@netzhansa.com\r\n" +
	"Auto-Submitted: auto-replied\r\n" +
	"Subject: Mail delivery failed: returning message to sender\r\n" +
	"Date: Wed, 07 Oct 2026 05:18:00 +0000\r\n" +
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
	"Reporting-MTA: dns; www97.your-server.de\r\n" +
	"\r\n" +
	"Action: failed\r\n" +
	"Status: 5.6.0\r\n" +
	"Remote-MTA: dns; mx.netzhansa.com\r\n" +
	"Diagnostic-Code: smtp; 554 5.6.0 message parse failed: truncated\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: CMarketing PERU <bounces@ingeasoto.com>\r\n" +
	"To: hans@netzhansa.com\r\n" +
	"Return-Path: <bounces@ingeasoto.com>\r\n" +
	"Subject: Oferta especial MBA\r\n" +
	"Date: Wed, 07 Oct 2026 05:17:00 +0000\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Compre ahora nuestro MBA especial.\r\n" +
	"--outer--\r\n"

// ownSentBounceReportRaw is shaped exactly like backscatterReportRaw
// except the enclosed original's From and Return-Path are the
// principal's own address: a bounce of mail the owner actually sent.
const ownSentBounceReportRaw = "From: Mail Delivery System <Mailer-Daemon@mx.example.net>\r\n" +
	"To: hans@netzhansa.com\r\n" +
	"Auto-Submitted: auto-replied\r\n" +
	"Subject: Mail delivery failed: returning message to sender\r\n" +
	"Date: Wed, 07 Oct 2026 05:18:00 +0000\r\n" +
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
	"From: Hans <hans@netzhansa.com>\r\n" +
	"To: nobody@example.net\r\n" +
	"Return-Path: <hans@netzhansa.com>\r\n" +
	"Subject: Re: project update\r\n" +
	"Date: Wed, 07 Oct 2026 05:17:00 +0000\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Please see the attached update.\r\n" +
	"--outer--\r\n"

// noEnclosedReportRaw is a delivery-status report with no message/rfc822
// part at all -- only the human-readable notice and the structured
// message/delivery-status fields.
const noEnclosedReportRaw = "From: Mail Delivery System <Mailer-Daemon@mx.example.net>\r\n" +
	"To: hans@netzhansa.com\r\n" +
	"Auto-Submitted: auto-replied\r\n" +
	"Subject: Mail delivery failed: returning message to sender\r\n" +
	"Date: Wed, 07 Oct 2026 05:18:00 +0000\r\n" +
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
	"--outer--\r\n"

var backscatterOwnAddresses = []string{"hans@netzhansa.com", "hans@huebner.org"}

// TestBuildRequest_DeliveryStatusReportShape is the request-shape test
// (re #513): BuildRequest must carry the enclosed original's curated
// headers and a text excerpt of it, plus the delivery-status fields
// (Action, Status, the remote-MTA diagnostic), for a multipart/report;
// report-type=delivery-status message.
func TestBuildRequest_DeliveryStatusReportShape(t *testing.T) {
	req := BuildRequest(buildMessage(t, backscatterReportRaw), nil)
	ds := req.DeliveryStatus
	if ds == nil {
		t.Fatalf("DeliveryStatus = nil, want populated for a delivery-status report")
	}
	if !strings.Contains(ds.EnclosedFrom, "bounces@ingeasoto.com") {
		t.Fatalf("EnclosedFrom = %q, want it to contain the enclosed original's From address", ds.EnclosedFrom)
	}
	if ds.EnclosedReturnPath != "<bounces@ingeasoto.com>" {
		t.Fatalf("EnclosedReturnPath = %q, want <bounces@ingeasoto.com>", ds.EnclosedReturnPath)
	}
	if !strings.Contains(ds.EnclosedTo, "hans@netzhansa.com") {
		t.Fatalf("EnclosedTo = %q, want it to contain hans@netzhansa.com", ds.EnclosedTo)
	}
	if ds.EnclosedSubject != "Oferta especial MBA" {
		t.Fatalf("EnclosedSubject = %q, want %q", ds.EnclosedSubject, "Oferta especial MBA")
	}
	if ds.EnclosedDate == "" {
		t.Fatalf("EnclosedDate is empty, want the enclosed original's Date")
	}
	if !strings.Contains(ds.EnclosedExcerpt, "MBA") {
		t.Fatalf("EnclosedExcerpt = %q, want it to contain the enclosed body's text", ds.EnclosedExcerpt)
	}
	if ds.Action != "failed" {
		t.Fatalf("Action = %q, want %q", ds.Action, "failed")
	}
	if ds.Status != "5.6.0" {
		t.Fatalf("Status = %q, want %q", ds.Status, "5.6.0")
	}
	if !strings.Contains(ds.Diagnostic, "mx.netzhansa.com") || !strings.Contains(ds.Diagnostic, "554 5.6.0") {
		t.Fatalf("Diagnostic = %q, want it to name the Remote-MTA and the Diagnostic-Code", ds.Diagnostic)
	}
}

// TestBuildRequest_NonReportMessageHasNoDeliveryStatus verifies an
// ordinary message (not multipart/report) never populates
// Request.DeliveryStatus.
func TestBuildRequest_NonReportMessageHasNoDeliveryStatus(t *testing.T) {
	req := BuildRequest(buildMessage(t, canonMsg), nil)
	if req.DeliveryStatus != nil {
		t.Fatalf("DeliveryStatus = %+v, want nil for a non-report message", req.DeliveryStatus)
	}
}

// TestClassify_BackscatterReportResolvedToSpam is the #513 regression
// test for the reported defect itself: a delivery-status report
// enclosing a message the owner never sent, classified ham at low
// confidence with no spam_signals at all (message 4247's exact
// reproduction), must be resolved to Spam by the server-computed
// backscatter fact -- not left to the model's reading of the notice
// text.
func TestClassify_BackscatterReportResolvedToSpam(t *testing.T) {
	msg := buildMessage(t, backscatterReportRaw)
	invoker := newFakeInvoker()
	invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"legitimate bounce notice"}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), msg, nil, "p", ClassifyContext{},
		OwnAddressInfo{Addresses: backscatterOwnAddresses, Complete: true})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Spam {
		t.Fatalf("verdict = %v, want Spam (a backscatter report must resolve to Spam regardless of the model's ham verdict)", r.Verdict)
	}
	if r.ModelVerdict != Ham {
		t.Fatalf("ModelVerdict = %v, want Ham (the plugin's own verdict is preserved for the transparency record)", r.ModelVerdict)
	}
	if r.DecisiveSignalMatch != "backscatter" {
		t.Fatalf("DecisiveSignalMatch = %q, want %q", r.DecisiveSignalMatch, "backscatter")
	}
	if !HasSignal(r.SpamSignals, "backscatter") {
		t.Fatalf("SpamSignals = %v, want it to contain backscatter", r.SpamSignals)
	}
}

// TestClassify_BounceOfOwnSentMessageStaysOnModelVerdict verifies a
// bounce of a message the owner actually sent (the enclosed original's
// From and Return-Path are an own address) is never treated as
// backscatter: the model's verdict stands, and no backscatter fact is
// recorded.
func TestClassify_BounceOfOwnSentMessageStaysOnModelVerdict(t *testing.T) {
	msg := buildMessage(t, ownSentBounceReportRaw)
	invoker := newFakeInvoker()
	invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"legitimate bounce of the owner's own mail"}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), msg, nil, "p", ClassifyContext{},
		OwnAddressInfo{Addresses: backscatterOwnAddresses, Complete: true})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Ham {
		t.Fatalf("verdict = %v, want Ham (a bounce of the owner's own mail must keep reaching the Inbox)", r.Verdict)
	}
	if r.ModelVerdict != Unclassified {
		t.Fatalf("ModelVerdict = %v, want Unclassified (no resolution should have happened)", r.ModelVerdict)
	}
	if HasSignal(r.SpamSignals, "backscatter") {
		t.Fatalf("SpamSignals = %v, want no backscatter for a bounce of the owner's own mail", r.SpamSignals)
	}
}

// TestClassify_DeliveryStatusReportWithNoEnclosedOriginalSetsNoFact
// verifies a delivery-status report carrying no message/rfc822 part
// never sets the backscatter fact: there is nothing to judge ownership
// of either way, so the model's verdict stands.
func TestClassify_DeliveryStatusReportWithNoEnclosedOriginalSetsNoFact(t *testing.T) {
	msg := buildMessage(t, noEnclosedReportRaw)
	invoker := newFakeInvoker()
	invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1,"reason":"bounce notice with no enclosed original"}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), msg, nil, "p", ClassifyContext{},
		OwnAddressInfo{Addresses: backscatterOwnAddresses, Complete: true})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Ham {
		t.Fatalf("verdict = %v, want Ham (no enclosed original to judge)", r.Verdict)
	}
	if HasSignal(r.SpamSignals, "backscatter") {
		t.Fatalf("SpamSignals = %v, want no backscatter when there is no enclosed original", r.SpamSignals)
	}
}

// TestClassify_BackscatterNotAppliedWhenOwnAddressSetIncomplete verifies
// the own-address-set-completeness gate (re #513, mirroring
// recipient_not_own): an incomplete own-address set must not let a
// backscatter fact resolve the verdict.
func TestClassify_BackscatterNotAppliedWhenOwnAddressSetIncomplete(t *testing.T) {
	msg := buildMessage(t, backscatterReportRaw)
	invoker := newFakeInvoker()
	invoker.handle("p", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"legitimate bounce notice"}`), nil
	})
	c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
	r, err := c.Classify(context.Background(), msg, nil, "p", ClassifyContext{},
		OwnAddressInfo{Addresses: backscatterOwnAddresses, Complete: false})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if r.Verdict != Ham {
		t.Fatalf("verdict = %v, want Ham (an incomplete own-address set must not make backscatter decisive)", r.Verdict)
	}
	if HasSignal(r.SpamSignals, "backscatter") {
		t.Fatalf("SpamSignals = %v, want no backscatter when the own-address set is known incomplete", r.SpamSignals)
	}
}
