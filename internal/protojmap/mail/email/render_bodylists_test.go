package email

// render_bodylists_test.go -- white-box unit tests for walkParts's
// textBody/htmlBody construction per RFC 8621 §4.1.4 (re #258).
//
// These tests live in the `email` package (not `email_test`) so they can
// call the unexported walkParts function directly, same as render_test.go.

import (
	"testing"
)

// partIDs extracts the ordered PartID values from a bodyPart slice for
// compact assertions.
func partIDs(parts []bodyPart) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		if p.PartID != nil {
			out[i] = *p.PartID
		}
	}
	return out
}

// assertPartIDs fails the test unless got's ordered partIds exactly equal
// want.
func assertPartIDs(t *testing.T, label string, got []bodyPart, want []string) {
	t.Helper()
	gotIDs := partIDs(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("%s: partIds = %v, want %v", label, gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("%s: partIds = %v, want %v", label, gotIDs, want)
		}
	}
}

// TestWalkParts_Issue258_LeadingPlainNoteWithRelatedAlternative reproduces
// the #258 MIME structure: a multipart/mixed forwarded message whose first
// child is the forwarder's own text/plain note (no html sibling of its
// own), followed by a multipart/related carrying the forwarded original as
// a multipart/alternative (text+html) plus an inline cid image, and a PDF
// attachment.
//
//	multipart/mixed                    idx 1
//	  text/plain (forwarder's note)    idx 2  <- Part1, no html sibling
//	  multipart/related                idx 3
//	    multipart/alternative          idx 4
//	      text/plain (forwarded text)  idx 5
//	      text/html  (forwarded html)  idx 6
//	    image/png (inline, cid)        idx 7
//	  application/pdf (attachment)     idx 8
//
// Before the fix, htmlBody omitted Part1 (idx 2) entirely -- only the
// forwarded html (idx 6) appeared, so the forwarder's note never reached
// the client. Per RFC 8621 §4.1.4, htmlBody and textBody are each the
// ordered concatenation of every mixed child's own contribution, and Part1
// (a leaf with no html alternative at its level) contributes to BOTH
// lists.
func TestWalkParts_Issue258_LeadingPlainNoteWithRelatedAlternative(t *testing.T) {
	raw := rawMsg(
		"From: forwarder@example.test",
		"To: rcpt@example.test",
		"Subject: Fwd: original",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"mix\"",
		"",
		"--mix",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hallo zusammen, ich leite euch mal die Mail weiter.",
		"--mix",
		"Content-Type: multipart/related; boundary=\"rel\"",
		"",
		"--rel",
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Sehr geehrte Damen und Herren (text).",
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>Sehr geehrte Damen und Herren (html).</p>",
		"--alt--",
		"--rel",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <logo@mail>",
		"",
		"FAKEPNGDATA",
		"--rel--",
		"--mix",
		"Content-Type: application/pdf",
		"Content-Disposition: attachment; filename=\"invoice.pdf\"",
		"",
		"FAKEPDFDATA",
		"--mix--",
	)
	msg := parseMsg(t, raw)
	_, values, textParts, htmlParts, attParts := walkParts(msg.Body, 0, "hash258", nil)

	// idx: 1=mixed, 2=Part1(plain note), 3=related, 4=alternative,
	// 5=forwarded text, 6=forwarded html, 7=inline png, 8=pdf attachment.
	assertPartIDs(t, "textBody", textParts, []string{"2", "5"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"2", "6"})

	// Part1 (idx 2) must be the SAME part in both lists (not a copy that
	// lost its type/value), and it must retain its own text/plain type --
	// RFC 8621 does not require a synthetic type change when a part is
	// reused across both lists.
	if textParts[0].Type != "text/plain" || htmlParts[0].Type != "text/plain" {
		t.Errorf("Part1 type = text:%q html:%q, want text/plain in both", textParts[0].Type, htmlParts[0].Type)
	}
	if v := values["2"].Value; v != "Hallo zusammen, ich leite euch mal die Mail weiter." {
		t.Errorf("Part1 bodyValue = %q, want the forwarder's note", v)
	}
	if v := values["5"].Value; v != "Sehr geehrte Damen und Herren (text)." {
		t.Errorf("forwarded text bodyValue = %q", v)
	}
	if v := values["6"].Value; v != "<p>Sehr geehrte Damen und Herren (html).</p>" {
		t.Errorf("forwarded html bodyValue = %q", v)
	}

	// attachments: the inline cid image (idx 7) and the pdf (idx 8), not
	// the note or the forwarded alternative parts.
	assertPartIDs(t, "attachments", attParts, []string{"7", "8"})
}

// TestWalkParts_BodyLists_BarePlainOnly verifies that a message with a
// single text/plain leaf as its entire body (no multipart wrapper at all)
// contributes that leaf to BOTH textBody and htmlBody, per RFC 8621
// §4.1.4's symmetric fallback (there is no html alternative anywhere in
// the message, so the plain part stands in for it).
func TestWalkParts_BodyLists_BarePlainOnly(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: plain only",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"just plain text",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, _ := walkParts(msg.Body, 0, "hashplain", nil)

	assertPartIDs(t, "textBody", textParts, []string{"1"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"1"})
}

// TestWalkParts_BodyLists_BareHTMLOnly is the symmetric counterpart of
// TestWalkParts_BodyLists_BarePlainOnly: a message with a single text/html
// leaf contributes to both lists.
func TestWalkParts_BodyLists_BareHTMLOnly(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: html only",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>just html</p>",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, _ := walkParts(msg.Body, 0, "hashhtml", nil)

	assertPartIDs(t, "textBody", textParts, []string{"1"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"1"})
}

// TestWalkParts_BodyLists_SimpleAlternative verifies the ordinary case: a
// bare multipart/alternative with one text/plain and one text/html leaf
// produces exactly one entry in each list, with no cross-duplication (this
// must NOT regress to the #258 both-track-fill behaviour, which applies
// only when a type has no alternative counterpart).
func TestWalkParts_BodyLists_SimpleAlternative(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: simple alternative",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain alt",
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html alt</p>",
		"--alt--",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, _ := walkParts(msg.Body, 0, "hashalt", nil)

	assertPartIDs(t, "textBody", textParts, []string{"2"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"3"})
}

// TestWalkParts_BodyLists_MixedTwoPlainNotes verifies multipart/mixed
// concatenation for two standalone text/plain notes with no html anywhere
// in the message: each note independently has no html alternative at its
// level, so RFC 8621 §4.1.4's symmetric fallback applies to each
// independently, and both end up in both lists, in order.
func TestWalkParts_BodyLists_MixedTwoPlainNotes(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: two plain notes",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"mix\"",
		"",
		"--mix",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"note one",
		"--mix",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"note two",
		"--mix--",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, _ := walkParts(msg.Body, 0, "hashtwoplain", nil)

	assertPartIDs(t, "textBody", textParts, []string{"2", "3"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"2", "3"})
}

// TestWalkParts_BodyLists_NestedRelatedNoAlternative verifies a
// multipart/related whose root part is text/html with an inline cid image
// and NO multipart/alternative/plain counterpart at all (RFC 8621 EXAMPLE
// 5's "related" shape without the outer alternative): the html part
// contributes to both lists (no plain alternative exists anywhere), and the
// inline image stays out of both lists (it surfaces via attachments only).
func TestWalkParts_BodyLists_NestedRelatedNoAlternative(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: related no alternative",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"rel\"",
		"",
		"--rel",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p><img src=\"cid:img@mail\"/></p>",
		"--rel",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <img@mail>",
		"",
		"FAKEPNGDATA",
		"--rel--",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, attParts := walkParts(msg.Body, 0, "hashrelated", nil)

	assertPartIDs(t, "textBody", textParts, []string{"2"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"2"})
	assertPartIDs(t, "attachments", attParts, []string{"3"})
}

// TestWalkParts_BodyLists_RelatedWithAlternative pins RFC 8621 EXAMPLE 5's
// exact shape: multipart/related wraps a multipart/alternative (text+html)
// plus an inline cid image. textBody and htmlBody must each get exactly one
// entry (the respective alternative leaf), and the image stays in
// attachments only.
func TestWalkParts_BodyLists_RelatedWithAlternative(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: related with alternative",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"rel\"",
		"",
		"--rel",
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain",
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p><img src=\"cid:img@mail\"/></p>",
		"--alt--",
		"--rel",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-ID: <img@mail>",
		"",
		"FAKEPNGDATA",
		"--rel--",
	)
	msg := parseMsg(t, raw)
	_, _, textParts, htmlParts, attParts := walkParts(msg.Body, 0, "hashrelalt", nil)

	assertPartIDs(t, "textBody", textParts, []string{"3"})
	assertPartIDs(t, "htmlBody", htmlParts, []string{"4"})
	assertPartIDs(t, "attachments", attParts, []string{"5"})
}
