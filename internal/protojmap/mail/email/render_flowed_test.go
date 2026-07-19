package email

// render_flowed_test.go — render-path coverage for RFC 3676 format=flowed
// reflow (re #261): walkParts must serve the reflowed text for a text/plain
// part carrying format=flowed, and must leave an ordinary text/plain part
// untouched.

import "testing"

// TestWalkParts_FormatFlowedReflow verifies that a text/plain part whose
// Content-Type carries format=flowed is served reflowed -- the soft-wrapped
// wire lines are joined into flowing paragraphs -- rather than verbatim.
func TestWalkParts_FormatFlowedReflow(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: flowed",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; format=flowed; charset=utf-8",
		"",
		"This is a long line that was ",
		"wrapped by the sending client ",
		"into several short lines.",
	)
	msg := parseMsg(t, raw)
	_, values, textParts, _, _ := walkParts(msg.Body, 0, "hash123", nil)

	if len(textParts) != 1 {
		t.Fatalf("want 1 textPart, got %d", len(textParts))
	}
	partID := *textParts[0].PartID
	got := values[partID].Value
	want := "This is a long line that was wrapped by the sending client into several short lines."
	if got != want {
		t.Errorf("reflowed bodyValue = %q, want %q", got, want)
	}
}

// TestWalkParts_FormatFlowedReflowDelSp verifies delsp=yes joins soft-broken
// lines without inserting a doubled space.
func TestWalkParts_FormatFlowedReflowDelSp(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: flowed delsp",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; format=flowed; delsp=yes; charset=utf-8",
		"",
		"supercali ",
		"fragilistic",
	)
	msg := parseMsg(t, raw)
	_, values, textParts, _, _ := walkParts(msg.Body, 0, "hash123", nil)

	partID := *textParts[0].PartID
	got := values[partID].Value
	want := "supercalifragilistic"
	if got != want {
		t.Errorf("delsp bodyValue = %q, want %q (soft-break space deleted before join)", got, want)
	}
}

// TestWalkParts_NonFlowedPlainUnchanged verifies that an ordinary text/plain
// part (no format=flowed parameter) is served verbatim, wire-wrapping and
// all -- reflow must never run on it.
func TestWalkParts_NonFlowedPlainUnchanged(t *testing.T) {
	raw := rawMsg(
		"From: sender@example.test",
		"To: rcpt@example.test",
		"Subject: not flowed",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"line one ",
		"line two",
	)
	msg := parseMsg(t, raw)
	_, values, textParts, _, _ := walkParts(msg.Body, 0, "hash123", nil)

	partID := *textParts[0].PartID
	got := values[partID].Value
	// The wire-wrapped CRLF line ending is preserved verbatim: reflow must
	// never run on a part without format=flowed.
	want := "line one \r\nline two"
	if got != want {
		t.Errorf("non-flowed bodyValue = %q, want unchanged %q", got, want)
	}
}
