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
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123", nil)

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
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123", nil)

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
	_, _, _, _, attParts := walkParts(msg.Body, 0, "hash123", nil)

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

// TestWalkParts_ImageDimsApplied verifies that intrinsic pixel dimensions from
// the dims map are attached to image leaf parts, while text and multipart parts
// receive nil Width/Height even when a matching dims entry exists.
func TestWalkParts_ImageDimsApplied(t *testing.T) {
	// multipart/related (idx 1):
	//   text/html (idx 2)
	//   image/png with CID (idx 3)
	//   image/jpeg with CID (idx 4)
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: dims applied",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><img src=\"cid:abc@mail\"/></body></html>",
		"--b",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <abc@mail>",
		"",
		"FAKEIMAGEDATA",
		"--b",
		"Content-Type: image/jpeg",
		"Content-Disposition: inline",
		"Content-ID: <def@mail>",
		"",
		"MOREFAKEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)

	dims := map[int]struct{ W, H int }{
		3: {W: 120, H: 80},
		4: {W: 200, H: 150},
	}
	bs, _, _, htmlParts, attParts := walkParts(msg.Body, 0, "hash123", dims)

	// Root multipart must carry no dimensions.
	if bs.Width != nil || bs.Height != nil {
		t.Errorf("root multipart: Width=%v Height=%v, want both nil", bs.Width, bs.Height)
	}
	// text/html part carries no dimensions.
	if len(htmlParts) != 1 {
		t.Fatalf("want 1 htmlPart, got %d", len(htmlParts))
	}
	if htmlParts[0].Width != nil || htmlParts[0].Height != nil {
		t.Errorf("html part: Width=%v Height=%v, want both nil", htmlParts[0].Width, htmlParts[0].Height)
	}
	// Image parts must carry dimensions from the map.
	if len(attParts) != 2 {
		t.Fatalf("want 2 attParts (image parts), got %d", len(attParts))
	}
	pngPart := attParts[0]
	if pngPart.Width == nil || *pngPart.Width != 120 {
		t.Errorf("png Width = %v, want 120", pngPart.Width)
	}
	if pngPart.Height == nil || *pngPart.Height != 80 {
		t.Errorf("png Height = %v, want 80", pngPart.Height)
	}
	jpegPart := attParts[1]
	if jpegPart.Width == nil || *jpegPart.Width != 200 {
		t.Errorf("jpeg Width = %v, want 200", jpegPart.Width)
	}
	if jpegPart.Height == nil || *jpegPart.Height != 150 {
		t.Errorf("jpeg Height = %v, want 150", jpegPart.Height)
	}
}

// TestWalkParts_NilDimsProducesNoWidthHeight verifies that passing a nil
// dims map leaves Width/Height as nil on all parts, including image parts.
func TestWalkParts_NilDimsProducesNoWidthHeight(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: nil dims",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"b\"",
		"",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body></body></html>",
		"--b",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <img@mail>",
		"",
		"FAKEIMAGEDATA",
		"--b--",
	)
	msg := parseMsg(t, raw)
	bs, _, _, htmlParts, attParts := walkParts(msg.Body, 0, "hash123", nil)

	if bs.Width != nil || bs.Height != nil {
		t.Errorf("root: Width=%v Height=%v, want nil", bs.Width, bs.Height)
	}
	if len(htmlParts) != 1 {
		t.Fatalf("want 1 htmlPart, got %d", len(htmlParts))
	}
	if htmlParts[0].Width != nil || htmlParts[0].Height != nil {
		t.Errorf("html: Width=%v Height=%v, want nil", htmlParts[0].Width, htmlParts[0].Height)
	}
	if len(attParts) != 1 {
		t.Fatalf("want 1 attPart, got %d", len(attParts))
	}
	if attParts[0].Width != nil || attParts[0].Height != nil {
		t.Errorf("image (nil dims): Width=%v Height=%v, want nil", attParts[0].Width, attParts[0].Height)
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
	_, _, _, _, attParts := walkParts(msg.Body, 0, "", nil)
	if len(attParts) == 0 {
		t.Error("attParts is empty; hasAttachment would be false, want true")
	}
}
