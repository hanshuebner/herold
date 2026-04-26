package webpush

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestBuildPayload_Email_IncludesPreview drives a real RFC 5322 message
// through BuildPayload and asserts the resulting payload carries a
// preview field bounded by the 80-byte cap.
func TestBuildPayload_Email_IncludesPreview(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")

	body := strings.Repeat("Hello there friend ", 20) // far longer than 80 bytes
	rfc822 := "From: bob@example.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Hi\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		body + "\r\n"
	ref, err := st.Blobs().Put(ctx, strings.NewReader(rfc822))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	mid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		MailboxID: mbid,
		Blob:      ref,
		Size:      ref.Size,
		Envelope:  store.Envelope{From: "bob@example.test", Subject: "Hi"},
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	res, err := BuildPayload(ctx, st, store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	})
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.JSON, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	preview, _ := got["preview"].(string)
	if preview == "" {
		t.Fatalf("preview missing from payload: %s", res.JSON)
	}
	if len(preview) > PayloadCapBytes {
		t.Fatalf("preview %d bytes exceeds cap %d", len(preview), PayloadCapBytes)
	}
	if !strings.Contains(preview, "Hello") {
		t.Fatalf("preview did not capture body text: %q", preview)
	}
}

// TestBuildPayload_Email_BlobMissingOmitsPreview asserts that when the
// blob fetch fails (or the message has no blob), BuildPayload omits the
// preview rather than failing.
func TestBuildPayload_Email_BlobMissingOmitsPreview(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		MailboxID: mbid,
		// Blob ref points at nothing.
		Blob:     store.BlobRef{Hash: "", Size: 0},
		Envelope: store.Envelope{Subject: "no body"},
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	res, err := BuildPayload(ctx, st, store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	})
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.JSON, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, hasPreview := got["preview"]; hasPreview {
		t.Fatalf("preview must be omitted when blob unavailable; got %s", res.JSON)
	}
}
