package email

// render_test.go — white-box unit tests for walkParts (re #1).
//
// These tests live in the `email` package (not `email_test`) so they can
// call the unexported walkParts function directly.

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// parseMsg is a minimal helper that builds a mailparse.Message from a raw
// RFC 5322 string.  It uses lax options (no strict-boundary check) so test
// messages need not be perfectly formed.
func parseMsg(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	opts := mailparse.NewParseOptions()
	opts.StrictBoundary = false
	opts.StrictBase64 = false
	opts.StrictQP = false
	msg, err := mailparse.Parse(strings.NewReader(raw), opts)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}
	return msg
}

// rawMsg joins header+body lines with CRLF.
func rawMsg(lines ...string) string {
	return strings.Join(lines, "\r\n")
}

// TestWalkParts_InlineImageNoCID verifies defect (2) from issue #1:
// an image part with Content-Disposition: inline but NO Content-ID header
// must be classified as a regular attachment (no disposition set in the JMAP
// output) so the suite's AttachmentList shows it under the main Attachments
// section rather than the "Inline images" sub-section.
func TestWalkParts_InlineImageNoCID(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image no cid",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline; filename=\"photo.jpg\"",
		"",
		"FAKEIMAGEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123")

	if len(attParts) != 1 {
		t.Fatalf("want 1 attPart, got %d", len(attParts))
	}
	att := attParts[0]
	if att.Type != "image/jpeg" {
		t.Errorf("part type = %q, want image/jpeg", att.Type)
	}
	// disposition must be nil (not "inline") because there is no CID.
	if att.Disposition != nil {
		t.Errorf("disposition = %q, want nil (no CID means not truly inline)", *att.Disposition)
	}
	// name must be preserved.
	if att.Name == nil || *att.Name != "photo.jpg" {
		n := "<nil>"
		if att.Name != nil {
			n = *att.Name
		}
		t.Errorf("name = %q, want photo.jpg", n)
	}
}

// TestWalkParts_InlineImageWithCID verifies that an image with
// Content-Disposition: inline AND a Content-ID retains disposition=="inline"
// so the suite can show it in the "Inline images" sub-section and wire the
// HTML-body overlay.
func TestWalkParts_InlineImageWithCID(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image with cid",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><img src=\"cid:abc@mail\"/></body></html>",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline; filename=\"photo.jpg\"",
		"Content-ID: <abc@mail>",
		"",
		"FAKEIMAGEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123")

	if len(attParts) != 1 {
		t.Fatalf("want 1 attPart, got %d", len(attParts))
	}
	att := attParts[0]
	if att.Disposition == nil || *att.Disposition != "inline" {
		d := "<nil>"
		if att.Disposition != nil {
			d = *att.Disposition
		}
		t.Errorf("disposition = %q, want inline (has CID so it is truly inline)", d)
	}
	if att.Cid == nil || *att.Cid != "abc@mail" {
		c := "<nil>"
		if att.Cid != nil {
			c = *att.Cid
		}
		t.Errorf("cid = %q, want abc@mail", c)
	}
}

// TestWalkParts_AttachmentDispositionUnchanged verifies that explicit
// Content-Disposition: attachment parts continue to land in attParts
// with disposition=="attachment" (regression guard for the existing test).
func TestWalkParts_AttachmentDispositionUnchanged(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: explicit attachment",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: attachment; filename=\"photo.jpg\"",
		"",
		"FAKEIMAGEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123")

	if len(attParts) != 1 {
		t.Fatalf("want 1 attPart, got %d", len(attParts))
	}
	att := attParts[0]
	if att.Disposition == nil || *att.Disposition != "attachment" {
		d := "<nil>"
		if att.Disposition != nil {
			d = *att.Disposition
		}
		t.Errorf("disposition = %q, want attachment", d)
	}
}

// TestWalkParts_HasAttachment_InlineNoCID verifies that hasAttachment is true
// for a message with an inline image that has no CID (defect 2 indicator
// test: the message list must show the attachment paperclip).
func TestWalkParts_HasAttachment_InlineNoCID(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: inline image no cid hasattachment",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline",
		"",
		"FAKEIMAGEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)
	_, _, _, _, attParts := walkParts(msg.Body, 0, "")
	if len(attParts) == 0 {
		t.Error("attParts is empty; hasAttachment would be false, want true")
	}
}
