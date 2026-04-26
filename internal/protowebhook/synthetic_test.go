package protowebhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/store"
)

// TestDispatchSynthetic_NoMailboxRow_DispatchesAndCarriesRouteTag is the
// Wave 3.5c-Z direct-dispatch test: a synthetic recipient (no mailbox,
// no principal_id) is delivered to a target_kind=synthetic webhook
// without flowing through the change feed.  The payload carries the
// route_tag, the envelope, and the extracted body text.
func TestDispatchSynthetic_NoMailboxRow_DispatchesAndCarriesRouteTag(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	hook := h.insertExtractedHookForDomain(t, "tickets.example", false, 0)

	raw := "From: alice@sender.test\r\n" +
		"To: reply+42@tickets.example\r\n" +
		"Subject: synthetic\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"hello synthetic\r\n"
	ref, err := h.store.Blobs().Put(h.ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	parsed, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hooks := h.dispatcher.MatchingSyntheticHooks(h.ctx, "tickets.example")
	if len(hooks) != 1 {
		t.Fatalf("MatchingSyntheticHooks = %d, want 1", len(hooks))
	}
	if hooks[0].ID != hook.ID {
		t.Fatalf("hook id mismatch: got %d want %d", hooks[0].ID, hook.ID)
	}

	in := protowebhook.SyntheticDispatch{
		Domain:    "tickets.example",
		Recipient: "reply+42@tickets.example",
		MailFrom:  "alice@sender.test",
		RouteTag:  "ticket:42",
		BlobHash:  ref.Hash,
		Size:      ref.Size,
		Parsed:    parsed,
	}
	if err := h.dispatcher.DispatchSynthetic(h.ctx, in, hooks); err != nil {
		t.Fatalf("DispatchSynthetic: %v", err)
	}

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.RouteTag != "ticket:42" {
		t.Fatalf("payload route_tag = %q, want %q", pl.RouteTag, "ticket:42")
	}
	if pl.Body.Mode != "extracted" {
		t.Fatalf("body.mode = %q, want extracted", pl.Body.Mode)
	}
	if pl.Body.TextOrigin != "native" {
		t.Fatalf("body.text_origin = %q, want native", pl.Body.TextOrigin)
	}
	if !strings.Contains(pl.Body.Text, "hello synthetic") {
		t.Fatalf("body.text = %q", pl.Body.Text)
	}
	if pl.PrincipalID != "" {
		t.Fatalf("synthetic delivery payload.principal_id = %q, want empty", pl.PrincipalID)
	}
	if pl.MailboxID != "" {
		t.Fatalf("synthetic delivery payload.mailbox_id = %q, want empty", pl.MailboxID)
	}
	if pl.MessageID != "" {
		t.Fatalf("synthetic delivery payload.message_id = %q, want empty", pl.MessageID)
	}
	if len(pl.Envelope.To) != 1 || pl.Envelope.To[0] != "reply+42@tickets.example" {
		t.Fatalf("envelope.to = %v", pl.Envelope.To)
	}
	if pl.Envelope.From != "alice@sender.test" {
		t.Fatalf("envelope.from = %q, want alice@sender.test", pl.Envelope.From)
	}
}

// TestDispatchSynthetic_NoMatchingSubscription_NoOp: the dispatcher
// returns nil and no POST happens when the supplied hooks slice is
// empty.  The SMTP layer still treats the recipient as accepted; this
// just verifies the dispatcher does not panic / log-error in the
// no-subscriber case.
func TestDispatchSynthetic_NoMatchingSubscription_NoOp(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})

	// Different domain has no subscriptions.
	hooks := h.dispatcher.MatchingSyntheticHooks(h.ctx, "no-hooks.example")
	if len(hooks) != 0 {
		t.Fatalf("expected no hooks, got %d", len(hooks))
	}
	if err := h.dispatcher.DispatchSynthetic(h.ctx, protowebhook.SyntheticDispatch{
		Domain:    "no-hooks.example",
		Recipient: "x@no-hooks.example",
		BlobHash:  "deadbeef",
	}, hooks); err != nil {
		t.Fatalf("DispatchSynthetic on empty hooks should be no-op: %v", err)
	}
	// Drive the clock briefly; assert no delivery received.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.clk.Advance(20 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.receivedCount(); got != 0 {
		t.Fatalf("delivered=%d, want 0 (no subscribers)", got)
	}
}

// TestDispatchSynthetic_TextRequiredDropsNoText: text_required=true
// + an attachment-only message drops the synthetic delivery without a
// POST and writes an audit row, mirroring the principal-bound
// REQ-HOOK-EXTRACTED-03 path.
func TestDispatchSynthetic_TextRequiredDropsNoText(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	h.insertExtractedHookForDomain(t, "tickets.example", true, 0)

	raw := "From: alice@sender.test\r\n" +
		"To: reply+42@tickets.example\r\n" +
		"Subject: pdf-only\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n"
	ref, err := h.store.Blobs().Put(h.ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	parsed, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	hooks := h.dispatcher.MatchingSyntheticHooks(h.ctx, "tickets.example")
	if err := h.dispatcher.DispatchSynthetic(h.ctx, protowebhook.SyntheticDispatch{
		Domain:    "tickets.example",
		Recipient: "reply+42@tickets.example",
		MailFrom:  "alice@sender.test",
		RouteTag:  "ticket:42",
		BlobHash:  ref.Hash,
		Size:      ref.Size,
		Parsed:    parsed,
	}, hooks); err != nil {
		t.Fatalf("DispatchSynthetic: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.clk.Advance(20 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if h.receivedCount() > 0 {
			t.Fatalf("expected drop; receiver got POST(s)")
		}
	}

	entries, err := h.store.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	var saw bool
	for _, e := range entries {
		if e.Action == "hook.dispatch.dropped_no_text" && e.Metadata["reason"] == "dropped_no_text" {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatalf("expected dropped_no_text audit row in %+v", entries)
	}
}
