package email_test

// internalize_pending_test.go covers REQ-EXTIMG-90/91/92 for the JMAP
// Email/import create path: an uploaded blob lands verbatim, exactly
// like the IMAP-mirror and Gmail Takeout importers, so it shares
// their on-demand flagging decision, factored into internal/extimg
// (re #446).

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// importAndGetMessage performs an Email/import create with body and
// returns the resulting store.Message row.
func importAndGetMessage(t *testing.T, f *fixture, body string) store.Message {
	t.Helper()
	ref := f.putBlob(t, body)
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	if mid == "" {
		t.Fatalf("created id missing: %v", resp.Created)
	}
	midU, err := strconv.ParseUint(mid, 10, 64)
	if err != nil {
		t.Fatalf("parse email id %q: %v", mid, err)
	}
	stored, err := f.srv.Store.Meta().GetMessage(context.Background(), store.MessageID(midU))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	return stored
}

const htmlBodyWithRemoteImage = "From: a@example.test\r\n" +
	"To: b@example.test\r\n" +
	"Subject: import-flag\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>hi</p><img src=\"https://example.test/tracker.png\"></body></html>\r\n"

const htmlBodyNoRemoteImage = "From: a@example.test\r\n" +
	"To: b@example.test\r\n" +
	"Subject: import-noflag\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>hi, no images here</p></body></html>\r\n"

// TestEmail_Import_FlagsExternalHTMLImage verifies that Email/import
// flags an uploaded message whose HTML body carries an external
// http(s) image reference (REQ-EXTIMG-91).
func TestEmail_Import_FlagsExternalHTMLImage(t *testing.T) {
	f := setupFixture(t)
	stored := importAndGetMessage(t, f, htmlBodyWithRemoteImage)
	if !stored.InternalizePending {
		t.Errorf("InternalizePending = false; want true for an imported message with an external HTML image reference")
	}
}

// TestEmail_Import_NoExternalImageNotFlagged verifies that plain HTML
// with no external image reference is not flagged.
func TestEmail_Import_NoExternalImageNotFlagged(t *testing.T) {
	f := setupFixture(t)
	stored := importAndGetMessage(t, f, htmlBodyNoRemoteImage)
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false for an imported message with no external image reference")
	}
}

// TestEmail_Import_PassthroughModeSuppressesFlag verifies that
// [external_images] mode = "passthrough" suppresses the flag even when
// the body carries an external HTML image reference (REQ-EXTIMG-92).
func TestEmail_Import_PassthroughModeSuppressesFlag(t *testing.T) {
	f := setupRetryFixture(t, extimg.Config{Mode: extimg.ModePassthrough})
	stored := importAndGetMessage(t, f, htmlBodyWithRemoteImage)
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false when [external_images] mode is \"passthrough\"")
	}
}
