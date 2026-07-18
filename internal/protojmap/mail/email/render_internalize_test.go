package email_test

// render_internalize_test.go -- regression test for the InternalizePending
// placeholder-rewrite loop's interaction with the RFC 8621 §4.1.4 symmetric-
// fill fix (re #258).
//
// resolveBodyLists (render.go) can promote a text/plain leaf with no html
// alternative at its level into htmlBody/htmlParts (the #258 fix). The
// InternalizePending placeholder-rewrite loop in render.go/get.go iterates
// htmlParts and runs extimg.RewriteForPlaceholder -- an html.Parse/Render
// round-trip -- on each part's bodyValue. Without a type guard, a promoted
// text/plain part gets HTML-round-tripped too, corrupting its bodyValue
// (which textBody shares by partId) into an <html><body>...</body></html>
// wrapper.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// insertInternalizePendingMessage stores a raw RFC 5322 message with
// InternalizePending set to true, bypassing extimg.Internalize entirely --
// this test only needs the Email/get-side placeholder rewrite to run, not a
// real fetch/failure history.
func insertInternalizePendingMessage(t *testing.T, f *fixture, raw string) store.MessageID {
	t.Helper()
	ref := f.putBlob(t, raw)
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate:       now,
		ReceivedAt:         now,
		Size:               ref.Size,
		Blob:               ref,
		InternalizePending: true,
		Envelope: store.Envelope{
			Subject: "Fwd: original",
			From:    "forwarder@example.test",
			To:      "rcpt@example.test",
			Date:    now,
		},
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg,
		[]store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	return mostRecentMessageID(t, f)
}

// TestEmailGet_InternalizePending_DoesNotRewritePromotedPlainPart
// reproduces the regression flagged by independent verification of the
// #258 fix: a multipart/mixed message with a leading text/plain note (no
// html sibling of its own -- promoted into htmlBody by the symmetric-fill
// rule) alongside a genuine text/html part carrying an external image
// reference, with InternalizePending == true.
//
// The note's bodyValue must be left byte-for-byte unchanged (no
// html.Parse/Render round-trip, no <html>/<body> wrapper), while the
// genuine text/html part's external image reference IS replaced with
// extimg.PlaceholderDataURI.
func TestEmailGet_InternalizePending_DoesNotRewritePromotedPlainPart(t *testing.T) {
	f := setupFixture(t)

	const notePlain = "Hallo zusammen, ich leite euch mal die Mail weiter."
	const externalImgURL = "http://evil.example.test/track.png"

	raw := strings.Join([]string{
		"From: forwarder@example.test",
		"To: rcpt@example.test",
		"Subject: Fwd: original",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"mix\"",
		"",
		"--mix",
		"Content-Type: text/plain; charset=utf-8",
		"",
		notePlain,
		"--mix",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p>Sehr geehrte Damen und Herren.</p><img src=\"" + externalImgURL + "\"></body></html>",
		"--mix--",
	}, "\r\n")

	id := insertInternalizePendingMessage(t, f, raw)

	_, raw2 := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{fmt.Sprintf("%d", id)},
		"fetchTextBodyValues": true,
		"fetchHTMLBodyValues": true,
		"maxBodyValueBytes":   64 * 1024,
	})
	var resp struct {
		List []struct {
			TextBody []struct {
				PartID string `json:"partId"`
				Type   string `json:"type"`
			} `json:"textBody"`
			HTMLBody []struct {
				PartID string `json:"partId"`
				Type   string `json:"type"`
			} `json:"htmlBody"`
			BodyValues map[string]struct {
				Value string `json:"value"`
			} `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw2, &resp); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, raw2)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d entries, want 1: %s", len(resp.List), raw2)
	}
	got := resp.List[0]

	// The #258 fix must still promote the note into htmlBody (both lists
	// carry both parts, in order) -- this pins the RFC 8621 §4.1.4 logic
	// itself, unaffected by this regression fix.
	if len(got.HTMLBody) != 2 {
		t.Fatalf("htmlBody = %+v, want 2 entries (note promoted + genuine html)", got.HTMLBody)
	}
	notePartID := got.HTMLBody[0].PartID
	htmlPartID := got.HTMLBody[1].PartID
	if got.HTMLBody[0].Type != "text/plain" {
		t.Fatalf("htmlBody[0].type = %q, want text/plain (the promoted note)", got.HTMLBody[0].Type)
	}
	if got.HTMLBody[1].Type != "text/html" {
		t.Fatalf("htmlBody[1].type = %q, want text/html", got.HTMLBody[1].Type)
	}

	noteValue, ok := got.BodyValues[notePartID]
	if !ok {
		t.Fatalf("no bodyValue for promoted note partId %q: %+v", notePartID, got.BodyValues)
	}
	// The regression: without the type guard, the note's bodyValue gets
	// html.Parse/Render round-tripped and gains an <html><body> wrapper.
	if noteValue.Value != notePlain {
		t.Errorf("promoted plain note bodyValue = %q, want unchanged %q (OBSERVED BUG: html round-trip corrupted a text/plain part)",
			noteValue.Value, notePlain)
	}
	if strings.Contains(noteValue.Value, "<html") || strings.Contains(noteValue.Value, "<body") {
		t.Errorf("promoted plain note bodyValue was HTML-wrapped: %q", noteValue.Value)
	}

	htmlValue, ok := got.BodyValues[htmlPartID]
	if !ok {
		t.Fatalf("no bodyValue for genuine html partId %q: %+v", htmlPartID, got.BodyValues)
	}
	if !strings.Contains(htmlValue.Value, extimg.PlaceholderDataURI) {
		t.Errorf("genuine html part was not placeholder-rewritten: %q", htmlValue.Value)
	}
	if strings.Contains(htmlValue.Value, externalImgURL) {
		t.Errorf("genuine html part still leaks the external image URL: %q", htmlValue.Value)
	}
}
