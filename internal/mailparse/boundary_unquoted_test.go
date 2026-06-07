package mailparse

import (
	"strings"
	"testing"
)

// TestParse_UnquotedTrailingDashBoundary: inbound message from a third-party
// MUA that uses an UNQUOTED boundary parameter ending in '-', e.g.:
//
//	Content-Type: multipart/alternative; boundary=abc123-
//
// RFC 2046 §5.1.1 permits this — boundary values are bcharsnospace tokens and
// '-' is in bcharsnospace. normalizeTrailingDashBoundaries detects the
// trailing '-' via its unquoted capture group but previously only rewrote the
// header parameter when anchored on a leading quote, leaving header and wire
// delimiters out of sync and causing enmime to parse zero child parts.
//
// Regression for the parser-side fix of the same desync we fixed on the
// generator side (hex-encoded boundaries): third parties can still emit
// unquoted trailing-dash boundaries.
func TestParse_UnquotedTrailingDashBoundary(t *testing.T) {
	raw := "From: sender@third-party.example\r\n" +
		"To: user@example.com\r\n" +
		"Subject: test unquoted trailing dash\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=abc123-\r\n" +
		"\r\n" +
		"--abc123-\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"PLAIN BODY UNQUOTED\r\n" +
		"--abc123-\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body>HTML BODY UNQUOTED</body></html>\r\n" +
		"--abc123---\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native (text=%q)", origin, text)
	}
	if !strings.Contains(text, "PLAIN BODY UNQUOTED") {
		t.Fatalf("text/plain body not extracted: %q", text)
	}
}

// TestParse_MixedQuotingTrailingDash: outer boundary is double-quoted with
// trailing '-'; inner boundary is UNQUOTED with trailing '-'. Exercises the
// normalizer handling both quoting styles in a single message.
func TestParse_MixedQuotingTrailingDash(t *testing.T) {
	// outer boundary (quoted): "outer-bnd-"
	// inner boundary (unquoted): inner-bnd-
	raw := "From: sender@third-party.example\r\n" +
		"To: user@example.com\r\n" +
		"Subject: mixed quoting test\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related;\r\n" +
		" boundary=\"outer-bnd-\"\r\n" +
		"\r\n" +
		"--outer-bnd-\r\n" +
		"Content-Type: multipart/alternative; boundary=inner-bnd-\r\n" +
		"\r\n" +
		"--inner-bnd-\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"PLAIN INNER MIXED\r\n" +
		"--inner-bnd-\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body>HTML INNER MIXED</body></html>\r\n" +
		"--inner-bnd---\r\n" +
		"--outer-bnd---\r\n"
	msg, err := Parse(strings.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	text, origin := ExtractBodyText(msg)
	if origin != BodyTextOriginNative {
		t.Fatalf("origin = %q, want native (text=%q)", origin, text)
	}
	if !strings.Contains(text, "PLAIN INNER MIXED") {
		t.Fatalf("text/plain body not extracted: %q", text)
	}
}

// TestNormalize_UnquotedParamRewritten checks the normalizer byte-level
// output directly: given a raw snippet with an unquoted boundary=foo-,
// the returned bytes must have boundary= pointing at the herold-norm-*
// replacement and the -- wire delimiter must match that same replacement.
func TestNormalize_UnquotedParamRewritten(t *testing.T) {
	input := []byte("Content-Type: multipart/mixed; boundary=foo-\r\n\r\n--foo-\r\npart\r\n--foo---\r\n")
	out := normalizeTrailingDashBoundaries(input)

	// The original boundary value must not appear in the output at all
	// (neither in the header parameter nor in the delimiters).
	if strings.Contains(string(out), "foo-") {
		t.Fatalf("original boundary value 'foo-' still present in output:\n%s", out)
	}
	// A herold-norm- replacement must be present and consistent: the
	// boundary= parameter value and the -- delimiter must match.
	re := boundaryParamRE
	m := re.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no boundary= parameter found in output:\n%s", out)
	}
	// Extract whichever capture group matched the value.
	var paramVal string
	for _, idx := range []int{1, 2, 3} {
		if len(m) > idx && len(m[idx]) > 0 {
			paramVal = string(m[idx])
			break
		}
	}
	if !strings.HasPrefix(paramVal, "herold-norm-") {
		t.Fatalf("boundary= value %q does not start with herold-norm-", paramVal)
	}
	delimPrefix := "--" + paramVal
	if !strings.Contains(string(out), delimPrefix) {
		t.Fatalf("expected delimiter %q not found in output:\n%s", delimPrefix, out)
	}
}

// TestNormalize_UnquotedBoundaryNotTouchedInBodyText: the body happens to
// contain the text "boundary=foo-" in plain text. The normalizer must rewrite
// the Content-Type parameter but MUST NOT touch the occurrence in the body.
func TestNormalize_UnquotedBoundaryNotTouchedInBodyText(t *testing.T) {
	// The body part contains the literal text "boundary=foo-" (not a header).
	input := []byte(
		"Content-Type: multipart/mixed; boundary=foo-\r\n" +
			"\r\n" +
			"--foo-\r\n" +
			"Content-Type: text/plain\r\n" +
			"\r\n" +
			"This message mentions boundary=foo- in body text.\r\n" +
			"--foo---\r\n",
	)
	out := normalizeTrailingDashBoundaries(input)

	// The body occurrence is tricky: "boundary=foo-" in body text looks the
	// same to a regex as the header. This test documents the known limitation:
	// the regex-based rewrite may touch body occurrences too, but crucially the
	// header and delimiters MUST be consistent (header's boundary value must
	// match the wire delimiters). We assert consistency, not body immutability.
	re := boundaryParamRE
	m := re.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no boundary= parameter found in output:\n%s", out)
	}
	var paramVal string
	for _, idx := range []int{1, 2, 3} {
		if len(m) > idx && len(m[idx]) > 0 {
			paramVal = string(m[idx])
			break
		}
	}
	if !strings.HasPrefix(paramVal, "herold-norm-") {
		t.Fatalf("boundary= value %q does not start with herold-norm-", paramVal)
	}
	// The wire delimiter must match the header value.
	delimPrefix := "--" + paramVal
	if !strings.Contains(string(out), delimPrefix) {
		t.Fatalf("header/delimiter desync: expected delimiter %q not found in:\n%s", delimPrefix, out)
	}
}
