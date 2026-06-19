package protowebhook_test

// TestDispatch_InlineBody_CapOverflow_* tests verify REQ-STORE-17 Phase-1
// behaviour in the inline delivery path: when readBlob detects that the
// stored blob is larger than the configured inlineMaxSize (the cap parameter
// passed to readBlob), the dispatcher MUST fall through to fetch_url and
// deliver the complete message via the signed URL rather than truncating the
// inline content.
//
// We exercise the LimitReader detection path by inserting a message with a
// manually under-reported Size field (so the existing tooBig check does not
// trigger) while the actual blob content exceeds the cap.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/store"
)

// deliverMessageWithFakeSize inserts a message whose stored Size reports
// reportedSize regardless of how many bytes the blob actually contains.
// This lets tests place a blob that is larger than inlineMaxSize into the
// store while making the dispatcher's msg.Size check believe it fits.
func (h *dispatcherHarness) deliverMessageWithFakeSize(
	t *testing.T,
	mb store.Mailbox,
	subject, raw string,
	reportedSize int64,
) store.Message {
	t.Helper()
	ref, err := h.store.Blobs().Put(h.ctx, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	// Override the reported size so tooBig stays false even though the
	// blob itself exceeds inlineMaxSize.
	msg := store.Message{
		PrincipalID: mb.PrincipalID,
		Size:        reportedSize,
		Blob:        ref,
		Envelope: store.Envelope{
			Subject: subject,
			From:    "alice@example.net",
			To:      "user@example.com",
		},
	}
	if _, _, err := h.store.Meta().InsertMessage(h.ctx, msg, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	return msg
}

// TestDispatch_InlineBody_CapOverflow_FallsBackToFetchURL: when a blob
// exceeds the configured inlineMaxSize despite msg.Size passing the tooBig
// check (store-metadata discrepancy), the dispatcher must NOT deliver a
// truncated body inline.  It must fall through to fetch_url mode so the
// receiver obtains the full, untruncated message via the signed URL.
//
// Configuration: inlineMax=4 bytes.  readBlob cap = 4 bytes.
// Blob is 34 bytes (> 4).  reportedSize = 3 (< 4) so tooBig=false.
// The LimitReader inside readBlob returns oversized=true; the dispatcher
// emits a warning log and switches to fetch_url.
func TestDispatch_InlineBody_CapOverflow_FallsBackToFetchURL(t *testing.T) {
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		inlineMax: 4, // readBlob cap = 4 bytes
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))

	// Blob is 34 bytes (> cap=4).  reportedSize=3 so tooBig=false.
	raw := "Subject: cap-overflow\r\n\r\nabcdef\r\n"
	h.deliverMessageWithFakeSize(t, mb, "cap-overflow", raw, 3)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// Must NOT deliver inline (that would be a truncated body).
	if pl.Body.Mode == "inline" {
		t.Fatalf("got inline delivery on oversized blob — silent truncation detected; body=%+v", pl.Body)
	}
	if pl.Body.Mode != "fetch_url" || pl.Body.FetchURL == nil {
		t.Fatalf("expected fetch_url fallback, got mode=%q payload=%+v", pl.Body.Mode, pl.Body)
	}

	// Fetch through the signed URL and verify the body is complete and
	// untruncated.
	resp, err := http.Get(pl.Body.FetchURL.URL)
	if err != nil {
		t.Fatalf("GET fetch_url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != raw {
		t.Fatalf("fetched body mismatch:\ngot  %q\nwant %q", got, raw)
	}
}

// TestDispatch_InlineBody_ExactlyAtCap_IsInline: a blob whose size equals
// exactly inlineMaxSize is delivered inline (not oversized).  This confirms
// the cap boundary is inclusive — the off-by-one in the LimitReader
// detection (reading capBytes+1) does not accidentally push on-cap blobs
// to fetch_url.
func TestDispatch_InlineBody_ExactlyAtCap_IsInline(t *testing.T) {
	// inlineMax=10. readBlob cap=10. We use a 10-byte blob with
	// reportedSize=9 (< 10) so tooBig=false, and the blob fits exactly.
	h := newDispatcherHarness(t, dispatcherHarnessOptions{
		inlineMax: 10,
	})
	_, mb := h.seedPrincipalDomain(t, "user@example.com")
	h.insertWebhookForDomain(t, "example.com", store.DeliveryModeInline, []byte("k"))

	raw := "0123456789" // 10 bytes exactly == cap
	h.deliverMessageWithFakeSize(t, mb, "at-cap", raw, 9)

	req := h.waitForDelivery(t, 2*time.Second)
	var pl protowebhook.Payload
	if err := json.Unmarshal(req.body, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pl.Body.Mode != "inline" {
		t.Fatalf("expected inline body at exact cap, got mode=%q", pl.Body.Mode)
	}
	if pl.Body.Inline == nil {
		t.Fatalf("inline body struct is nil")
	}
}
