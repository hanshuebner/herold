package mailparse

import "testing"

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
			// but RFC 3676 unstuffing cannot tell the two apart and always
			// removes exactly one leading space, so it disappears here.
			// A generator that wants the space preserved must stuff it
			// twice (see the next case).
			name: "quoted region reflows within its depth and keeps its prefix",
			body: "> quoted line one \n" +
				"> quoted line two\n" +
				"unquoted reply\n",
			want: ">quoted line one quoted line two\n" +
				"unquoted reply\n",
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
			want: ">>deep quote \n" +
				">shallow quote\n",
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
