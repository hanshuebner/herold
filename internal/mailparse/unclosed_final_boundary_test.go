package mailparse

import (
	"bytes"
	"errors"
	"testing"
)

// TestUnclosedFinalBoundary_StrictRejectsLenientRecovers pins re #285: a
// multipart message whose final delimiter lacks the closing "--" (a common
// PHP/web-form mailer output) must fail under the default NewParseOptions
// (StrictBoundary: true) but recover its body and attachment under
// NewLenientParseOptions, the options the render/index paths use. The
// corpus mirrors the reported message shape exactly: a quoted boundary
// containing a space ("simple boundary"), a text/html body part, and a
// text/csv attachment part.
func TestUnclosedFinalBoundary_StrictRejectsLenientRecovers(t *testing.T) {
	data := loadCorpus(t, "27-unclosed-final-boundary-quoted-with-space.eml")

	// Strict (production default): Parse must fail with ReasonTruncated,
	// confirming the reported symptom -- render-time strictness on an
	// already-accepted message loses the body entirely.
	_, err := Parse(bytes.NewReader(data), NewParseOptions())
	if err == nil {
		t.Fatal("expected NewParseOptions (StrictBoundary=true) to reject the unclosed final boundary, got nil error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}
	if pe.Reason != ReasonTruncated {
		t.Fatalf("expected ReasonTruncated, got %v (%v)", pe.Reason, err)
	}

	// Lenient (render/index policy, re #285): Parse must succeed and
	// recover both the HTML body and the CSV attachment.
	msg, err := Parse(bytes.NewReader(data), NewLenientParseOptions())
	if err != nil {
		t.Fatalf("NewLenientParseOptions: unexpected error: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginDerivedFromHTML {
		t.Errorf("ExtractBodyText origin = %v, want %v", origin, BodyTextOriginDerivedFromHTML)
	}
	if text == "" {
		t.Error("ExtractBodyText: expected recovered HTML-derived body text, got empty string")
	}
	if !HasAttachment(msg) {
		t.Error("HasAttachment: expected the text/csv part to be detected as an attachment")
	}
}

// TestQuotedBoundaryWithSpace_ParsesUnderEitherStrictness confirms the
// quoted boundary parameter containing a space ("simple boundary") is NOT
// itself the cause of the parse failure: mime.ParseMediaType handles it
// correctly regardless of StrictBoundary, so the message with a properly
// closed final delimiter parses cleanly under strict defaults too.
func TestQuotedBoundaryWithSpace_ParsesUnderEitherStrictness(t *testing.T) {
	closed := []byte(`From: Web Form <info@classic-computing.de>
To: vorsitz@classic-computing.de
Subject: Mitgliedsantrag
Date: Tue, 18 Aug 2026 17:32:01 +0000
Message-ID: <closed-variant@classic-computing.de>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="simple boundary"

--simple boundary
Content-Type: text/html; charset=utf-8

<html><body><p>Neuer Mitgliedsantrag ueber das Formular.</p></body></html>

--simple boundary
Content-Type: text/csv
Content-Disposition: attachment; filename="antrag.csv"

name,email
Max Mustermann,max@example.org
--simple boundary--
`)
	if _, err := Parse(bytes.NewReader(closed), NewParseOptions()); err != nil {
		t.Fatalf("quoted boundary with a space should parse under strict defaults when properly closed: %v", err)
	}
	if _, err := Parse(bytes.NewReader(closed), NewLenientParseOptions()); err != nil {
		t.Fatalf("quoted boundary with a space should parse under lenient options when properly closed: %v", err)
	}
}
