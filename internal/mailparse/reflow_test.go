package mailparse

import (
	"regexp"
	"strings"
	"testing"
)

// isQuotedLineRe mirrors web/apps/suite/src/lib/mail/quoted.ts's
// isQuotedLine regex (`/^>+(\s|$)/` applied to line.trim()) so this Go
// test can assert, in the same terms the Suite uses, that a reflowed
// quoted line is still recognized as quoted (re #261 follow-up: a
// depth>0 line whose markers were glued directly to the text, with no
// separating space, failed this exact check).
var isQuotedLineRe = regexp.MustCompile(`^>+(\s|$)`)

func isQuotedLine(line string) bool {
	return isQuotedLineRe.MatchString(strings.TrimSpace(line))
}

func TestReflowFormatFlowed(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		delSp bool
		want  string
	}{
		{
			name: "soft-wrapped paragraph joins into one line",
			body: "This is a long line that was \n" +
				"wrapped by the sending client \n" +
				"into several short lines.\n",
			want: "This is a long line that was wrapped by the sending client into several short lines.\n",
		},
		{
			name: "hard break stays a paragraph boundary",
			body: "First paragraph.\n" +
				"\n" +
				"Second paragraph.\n",
			want: "First paragraph.\n\nSecond paragraph.\n",
		},
		{
			name: "space-stuffed line loses exactly one leading space",
			body: " Not >really< a quote, just stuffed.\n",
			want: "Not >really< a quote, just stuffed.\n",
		},
		{
			name: "space-stuffed From line",
			body: " From the report attached, ...\n",
			want: "From the report attached, ...\n",
		},
		{
			name:  "delsp=yes soft join removes the marker space",
			body:  "foo \nbar\n",
			delSp: true,
			want:  "foobar\n",
		},
		{
			name:  "delsp=no soft join keeps the separating space",
			body:  "foo \nbar\n",
			delSp: false,
			want:  "foo bar\n",
		},
		{
			// The single space right after the quote marker is the
			// generator's readability convention, not stuffed content --
			// RFC 3676 unstuffing cannot tell the two apart and always
			// removes exactly one leading space, so it disappears from
			// `content` here. The emitter then normalizes the display
			// prefix back to exactly one space so the output still reads
			// as "> text" and matches the Suite's quote-collapse detector
			// (re #261 follow-up).
			name: "quoted region reflows within its depth and keeps its prefix",
			body: "> quoted line one \n" +
				"> quoted line two\n" +
				"unquoted reply\n",
			want: "> quoted line one quoted line two\n" +
				"unquoted reply\n",
		},
		{
			name: "nested depth reflows and keeps a single readable space",
			body: ">> nested quote line one \n" +
				">> nested quote line two\n",
			want: ">> nested quote line one nested quote line two\n",
		},
		{
			name: "double-stuffed quoted line preserves one readability space",
			body: ">  quoted line\n",
			want: "> quoted line\n",
		},
		{
			name: "quote depth change is a hard boundary",
			body: ">> deep quote \n" +
				"> shallow quote\n",
			want: ">> deep quote \n" +
				"> shallow quote\n",
		},
		{
			name: "signature separator is preserved as a hard break",
			body: "Regards,\n" +
				"-- \n" +
				"Alice\n",
			want: "Regards,\n-- \nAlice\n",
		},
		{
			name: "CRLF line endings are handled",
			body: "one \r\ntwo\r\n",
			want: "one two\n",
		},
		{
			name: "no trailing newline",
			body: "one \ntwo",
			want: "one two",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "empty quoted line preserves its marker",
			body: "> \n> \n",
			want: ">\n>\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReflowFormatFlowed(tt.body, tt.delSp)
			if got != tt.want {
				t.Errorf("ReflowFormatFlowed(%q, delSp=%v) = %q, want %q", tt.body, tt.delSp, got, tt.want)
			}
		})
	}
}

// TestReflowFormatFlowedQuotedLinesMatchSuiteDetector asserts that every
// quoted (depth>0) line ReflowFormatFlowed emits is still recognized by
// the Suite's isQuotedLine regex, for both a single-space-convention
// sender (the common case, and the one that used to glue the markers
// directly to the text after unstuffing) and a properly double-stuffed
// sender, at depth 1 and depth 2 (re #261 follow-up).
func TestReflowFormatFlowedQuotedLinesMatchSuiteDetector(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "single-space convention, depth 1",
			body: "> long quoted line that was \n> soft-wrapped here.\n",
		},
		{
			name: "single-space convention, depth 2",
			body: ">> long quoted line that was \n>> soft-wrapped here.\n",
		},
		{
			name: "double-stuffed convention, depth 1",
			body: ">  quoted line\n",
		},
		{
			name: "empty quoted line",
			body: "> \n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ReflowFormatFlowed(tt.body, false)
			for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if !strings.HasPrefix(line, ">") {
					continue
				}
				if !isQuotedLine(line) {
					t.Errorf("reflowed quoted line %q does not match the Suite's isQuotedLine regex %s", line, isQuotedLineRe)
				}
			}
		})
	}
}

// TestReflowFormatFlowedSingleSpaceQuoteJoinsReadably is the exact
// worked example from the #261 follow-up: a depth-1, single-space-
// convention soft-wrapped quote reflows to one line, marker + one
// space + joined text, not the markers glued directly to the text.
func TestReflowFormatFlowedSingleSpaceQuoteJoinsReadably(t *testing.T) {
	body := "> long quoted line that was \n> soft-wrapped here.\n"
	want := "> long quoted line that was soft-wrapped here.\n"
	got := ReflowFormatFlowed(body, false)
	if got != want {
		t.Errorf("ReflowFormatFlowed(%q, false) = %q, want %q", body, got, want)
	}
	if !isQuotedLine(strings.TrimRight(got, "\n")) {
		t.Errorf("reflowed line %q does not match the Suite's isQuotedLine regex", got)
	}
}

// TestReflowFormatFlowedNonFlowedPassthrough documents that ordinary
// non-flowed text (no soft-break trailing spaces) is unchanged by
// ReflowFormatFlowed, matching the wire path's "only reflow format=flowed
// parts" rule -- this test exercises the function directly on
// already-hard-wrapped text; the render-path test in
// internal/protojmap/mail/email covers the "don't even call it" half.
func TestReflowFormatFlowedNonFlowedPassthrough(t *testing.T) {
	body := "Line one.\nLine two.\nLine three.\n"
	got := ReflowFormatFlowed(body, false)
	if got != body {
		t.Errorf("ReflowFormatFlowed on hard-wrapped text = %q, want unchanged %q", got, body)
	}
}
