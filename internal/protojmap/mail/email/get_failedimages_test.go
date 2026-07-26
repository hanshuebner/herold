package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// insertMessageWithFailedImages stores a message carrying the four
// failed-image store fields directly (issue #271), mirroring
// fixture.insertMessage but exposing the fields that helper omits.
func (f *fixture) insertMessageWithFailedImages(t *testing.T, subject string, count, retryable int, state, reason string) store.Message {
	t.Helper()
	ref := f.putBlob(t, "body for "+subject)
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate:              now,
		ReceivedAt:                now,
		Size:                      ref.Size,
		Blob:                      ref,
		Envelope:                  store.Envelope{Subject: subject, From: "sender@example.test", To: "rcpt@example.test", Date: now},
		FailedImageCount:          count,
		FailedImageState:          state,
		RetryableFailedImageCount: retryable,
		FailedImageReason:         reason,
	}
	_, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg, []store.MessageMailbox{{MailboxID: f.inbox.ID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg.ID = mostRecentMessageID(t, f)
	return msg
}

// TestEmail_Get_FailedImageSignal_PermanentOnly covers issue #271: a
// message whose failed images are all permanent reports
// retryableFailedImageCount = 0 and a non-null failedImageReason.
func TestEmail_Get_FailedImageSignal_PermanentOnly(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessageWithFailedImages(t, "permanent-only", 1, 0,
		`{"urls":["http://example.test/a.png"]}`, string(extimg.FailedImageReasonBlocked))

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	got := resp.List[0]
	if v, ok := got["retryableFailedImageCount"]; ok && v != nil && v != float64(0) {
		t.Errorf("retryableFailedImageCount = %v, want 0 (absent or 0)", v)
	}
	reason, ok := got["failedImageReason"]
	if !ok {
		t.Fatalf("failedImageReason key missing from response: %s", raw)
	}
	if reason != string(extimg.FailedImageReasonBlocked) {
		t.Errorf("failedImageReason = %v, want %q", reason, extimg.FailedImageReasonBlocked)
	}
	if v, ok := got["failedImageCount"]; !ok || v != float64(1) {
		t.Errorf("failedImageCount = %v, want 1", v)
	}
}

// TestEmail_Get_FailedImageSignal_Transient covers the counterpart: a
// message with at least one transient failure reports
// retryableFailedImageCount > 0 and a null failedImageReason (no
// permanent category applies).
func TestEmail_Get_FailedImageSignal_Transient(t *testing.T) {
	f := setupFixture(t)
	m := f.insertMessageWithFailedImages(t, "transient", 1, 1,
		`{"urls":["http://example.test/b.png"]}`, "")

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	got := resp.List[0]
	retryable, ok := got["retryableFailedImageCount"]
	if !ok || retryable != float64(1) {
		t.Errorf("retryableFailedImageCount = %v, want 1", retryable)
	}
	reason, ok := got["failedImageReason"]
	if !ok {
		t.Fatalf("failedImageReason key missing from response: %s", raw)
	}
	if reason != nil {
		t.Errorf("failedImageReason = %v, want null", reason)
	}
}
