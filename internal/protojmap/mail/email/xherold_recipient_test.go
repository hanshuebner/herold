package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// TestEmailGet_InjectsXHeroldRecipient verifies REQ-FLOW-34 on the
// JMAP read path: Email/get with header:X-Herold-Recipient surfaces
// the per-(message, mailbox) received_to as a synthetic header. The
// header is added at render time only; the persisted blob remains
// dedup-shareable across fan-out recipients (REQ-FLOW-30).
func TestEmailGet_InjectsXHeroldRecipient(t *testing.T) {
	f := setupFixture(t)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: hi\r\n\r\nbody"
	ref := f.putBlob(t, body)
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject: "hi",
			From:    "bob@sender.test",
			To:      "alice@example.test",
			Date:    now,
		},
	}
	want := "alice+work@example.test"
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg, []store.MessageMailbox{{
		MailboxID:  f.inbox.ID,
		ReceivedTo: want,
	}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	mid := mostRecentMessageID(t, f)

	// Use the dynamic header accessor: header:X-Herold-Recipient:asText.
	// Email/get parses the blob and surfaces matching headers from the
	// top of the message.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", mid)},
		"properties": []string{
			"id", "header:X-Herold-Recipient:asText", "bodyStructure",
		},
	})
	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	hdr := resp.List[0]["header:X-Herold-Recipient:asText"]
	if len(hdr) == 0 {
		t.Fatalf("header:X-Herold-Recipient:asText missing from response: %s", raw)
	}
	var got string
	if err := json.Unmarshal(hdr, &got); err != nil {
		t.Fatalf("decode header value: %v (%s)", err, hdr)
	}
	if got != want {
		t.Fatalf("header:X-Herold-Recipient:asText = %q, want %q", got, want)
	}
}

// TestEmailGet_LegacyEmptyReceivedTo_NoInjection asserts the legacy
// case (REQ-FLOW-34): when received_to is empty (pre-feature
// memberships, Email/set drafts, JMAP imports), the renderer emits
// no synthetic header — the accessor returns the JSON null sentinel.
func TestEmailGet_LegacyEmptyReceivedTo_NoInjection(t *testing.T) {
	f := setupFixture(t)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: hi\r\n\r\nbody"
	m := f.insertMessage(t, body, "hi", "bob@sender.test", "alice@example.test", nil, "")

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{
			"id", "header:X-Herold-Recipient:asText",
		},
	})
	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	hdr := string(resp.List[0]["header:X-Herold-Recipient:asText"])
	if hdr != "null" {
		t.Fatalf("header:X-Herold-Recipient:asText = %s, want null (no injection for empty received_to)", hdr)
	}
}

// TestDownload_RFC822_InjectsXHeroldRecipient verifies REQ-FLOW-34 on
// the /jmap/download/<accountId>/<blobHash>/message%2Frfc822/msg.eml
// path: a full-message download for the owning principal carries the
// X-Herold-Recipient header at the top of the streamed body.
func TestDownload_RFC822_InjectsXHeroldRecipient(t *testing.T) {
	f := setupFixture(t)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: dl\r\n\r\nbody-bytes"
	ref := f.putBlob(t, body)
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject: "dl",
			From:    "bob@sender.test",
			To:      "alice@example.test",
			Date:    now,
		},
	}
	want := "alice+downloads@example.test"
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg, []store.MessageMailbox{{
		MailboxID:  f.inbox.ID,
		ReceivedTo: want,
	}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	dlURL := fmt.Sprintf("%s/jmap/download/%s/%s/message%%2Frfc822/msg.eml",
		f.baseURL, protojmap.AccountIDForPrincipal(f.pid), ref.Hash)
	req, err := http.NewRequest(http.MethodGet, dlURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, gotBody)
	}
	header := "X-Herold-Recipient: " + want
	if !strings.HasPrefix(string(gotBody), header+"\r\n") {
		t.Fatalf("download body does not start with %q.\nGot first 256 bytes:\n%s",
			header, string(gotBody)[:min(256, len(gotBody))])
	}
	// Content-Length must reflect the injected size.
	if cl := resp.Header.Get("Content-Length"); cl != fmt.Sprintf("%d", len(gotBody)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(gotBody))
	}
}
