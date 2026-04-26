package protowebhook_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/store"
)

// insertExtractedHookForDomain registers an active synthetic+extracted
// subscription matching the supplied domain.
func (h *dispatcherHarness) insertExtractedHookForDomain(
	t *testing.T,
	domain string,
	textRequired bool,
	maxBytes int64,
) store.Webhook {
	t.Helper()
	w, err := h.store.Meta().InsertWebhook(h.ctx, store.Webhook{
		// Stored with owner_kind=domain (best legacy fallback) so
		// list-by-owner CRUD continues to surface the row; the
		// dispatcher matches via TargetKind.
		OwnerKind:             store.WebhookOwnerDomain,
		OwnerID:               domain,
		TargetKind:            store.WebhookTargetSynthetic,
		TargetURL:             h.receiver.URL,
		HMACSecret:            []byte("ext-hook-secret"),
		DeliveryMode:          store.DeliveryModeInline, // legacy mirror
		BodyMode:              store.WebhookBodyModeExtracted,
		ExtractedTextMaxBytes: maxBytes,
		TextRequired:          textRequired,
		Active:                true,
	})
	if err != nil {
		t.Fatalf("insert extracted webhook: %v", err)
	}
	return w
}

// TestDispatch_Extracted_Native: a multipart/alternative arrives and
// the webhook payload carries body.text from the text/plain part with
// origin=native and the attachments behind fetch URLs.
func TestDispatch_Extracted_Native(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertExtractedHookForDomain(t, "example.com", false, 0)

	raw := "From: a@example.net\r\n" +
		"To: user@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=O\r\n" +
		"\r\n" +
		"--O\r\n" +
		"Content-Type: multipart/alternative; boundary=I\r\n" +
		"\r\n" +
		"--I\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"hello team\r\n" +
		"--I\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>hello team</p>\r\n" +
		"--I--\r\n" +
		"--O\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=trace.bin\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAEC\r\n" +
		"--O--\r\n"
	h.deliverMessage(t, mb, "hi", raw)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.Mode != "extracted" {
		t.Fatalf("body.mode = %q, want extracted", pl.Body.Mode)
	}
	if pl.Body.TextOrigin != "native" {
		t.Fatalf("body.text_origin = %q, want native", pl.Body.TextOrigin)
	}
	if !strings.Contains(pl.Body.Text, "hello team") {
		t.Fatalf("body.text = %q", pl.Body.Text)
	}
	if pl.Body.TextTruncated {
		t.Fatalf("text_truncated = true unexpectedly")
	}
	if pl.RawRFC822URL == "" {
		t.Fatalf("raw_rfc822_url missing")
	}
	if len(pl.Attachments) != 1 {
		t.Fatalf("attachments count = %d, want 1: %+v", len(pl.Attachments), pl.Attachments)
	}
	if pl.Attachments[0].FetchURL == "" {
		t.Fatalf("attachment fetch_url empty")
	}
	if pl.Attachments[0].Filename != "trace.bin" {
		t.Fatalf("attachment filename = %q", pl.Attachments[0].Filename)
	}
}

// TestDispatch_Extracted_DerivedFromHTML: a html-only message yields
// origin=derived_from_html.
func TestDispatch_Extracted_DerivedFromHTML(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertExtractedHookForDomain(t, "example.com", false, 0)

	raw := "From: a@example.net\r\n" +
		"To: user@example.com\r\n" +
		"Subject: hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>see <a href=\"https://wiki/x\">the wiki</a> for details</p>\r\n"
	h.deliverMessage(t, mb, "hi", raw)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.TextOrigin != "derived_from_html" {
		t.Fatalf("text_origin = %q, want derived_from_html", pl.Body.TextOrigin)
	}
	if !strings.Contains(pl.Body.Text, "the wiki (https://wiki/x)") {
		t.Fatalf("text = %q (link not preserved)", pl.Body.Text)
	}
}

// TestDispatch_Extracted_Truncation: a body longer than the
// per-subscription cap is truncated with text_truncated=true.
func TestDispatch_Extracted_Truncation(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertExtractedHookForDomain(t, "example.com", false, 64) // tiny cap

	body := strings.Repeat("abcdef ", 200) // ~1400 bytes after extraction
	raw := "From: a@example.net\r\nTo: user@example.com\r\nSubject: long\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\n" + body
	h.deliverMessage(t, mb, "long", raw)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !pl.Body.TextTruncated {
		t.Fatalf("expected text_truncated=true, got false")
	}
	if int64(len(pl.Body.Text)) != 64 {
		t.Fatalf("text length = %d, want 64", len(pl.Body.Text))
	}
	if pl.RawRFC822URL == "" {
		t.Fatalf("raw_rfc822_url missing")
	}
}

// TestDispatch_Extracted_TextRequired_DropsNoText: text_required=true
// + origin=none drops the delivery without POST.
func TestDispatch_Extracted_TextRequired_DropsNoText(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertExtractedHookForDomain(t, "example.com", true, 0)

	// Message with only application/pdf — no text body.
	raw := "From: a@example.net\r\n" +
		"To: user@example.com\r\n" +
		"Subject: pdf-only\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQK\r\n"
	h.deliverMessage(t, mb, "pdf-only", raw)

	// Drive the dispatcher: tick the clock past the poll interval and
	// give the worker time to process.  We deliberately do NOT call
	// waitForDelivery — the message should be dropped.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.clk.Advance(20 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if h.receivedCount() > 0 {
			t.Fatalf("expected drop; receiver got POST(s)")
		}
	}

	// Verify the audit log carries the dropped_no_text entry.
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
		t.Fatalf("expected audit entry hook.dispatch.dropped_no_text in %+v", entries)
	}
}

// TestDispatch_Extracted_TextRequired_NativeStillDelivered: with
// text_required=true and a real text body, the delivery proceeds.
func TestDispatch_Extracted_TextRequired_NativeStillDelivered(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertExtractedHookForDomain(t, "example.com", true, 0)

	raw := "From: a@example.net\r\nTo: user@example.com\r\nSubject: hi\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\n" +
		"native body present\r\n"
	h.deliverMessage(t, mb, "hi", raw)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.TextOrigin != "native" {
		t.Fatalf("text_origin = %q, want native", pl.Body.TextOrigin)
	}
	if !strings.Contains(pl.Body.Text, "native body present") {
		t.Fatalf("text = %q", pl.Body.Text)
	}
}

// TestDispatch_SyntheticTargetKind_Matches: a target_kind=synthetic
// subscription on the recipient domain matches alongside any
// owner_kind=domain hooks.
func TestDispatch_SyntheticTargetKind_Matches(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")

	// Hook 1: legacy owner_kind=domain.
	if _, err := h.store.Meta().InsertWebhook(h.ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "example.com",
		TargetURL: h.receiver.URL, HMACSecret: []byte("k"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	// Hook 2: target_kind=synthetic on the same domain (extracted-mode
	// is independent — exercising the matching predicate alone).
	h.insertExtractedHookForDomain(t, "example.com", false, 0)

	h.deliverMessage(t, mb, "hi", "Subject: hi\r\n\r\nhello\r\n")

	// Both hooks should fire.  Wait for two POSTs.
	h.waitForDelivery(t, 2*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.receivedCount() < 2 {
		h.clk.Advance(20 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.receivedCount(); got < 2 {
		t.Fatalf("delivered=%d, want >=2 (legacy + synthetic-target hooks)", got)
	}
}
