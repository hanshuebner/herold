package email

// bodybuild_test.go — white-box unit tests for formatAddresses.
//
// These tests live in the `email` package (not `email_test`) so they can
// call the unexported formatAddresses function directly.

import (
	"fmt"
	"net/mail"
	"strings"
	"testing"

	"github.com/jhillyerd/enmime"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// TestGenerateMessageID_HostnameLeak: when the caller supplies an
// empty hostname, the generator must NOT fall back to "localhost" --
// that string then leaks into outbound Message-ID headers and trips
// spam heuristics. Production callers always pass the configured
// [server] hostname; the empty string is a programming error and we
// keep the "localhost" sentinel only to avoid emitting a malformed
// "<...@>" header. Test guards both shapes.
func TestGenerateMessageID_HostnameApplied(t *testing.T) {
	id := generateMessageID("mx.example.com")
	if !strings.HasSuffix(id, "@mx.example.com>") {
		t.Errorf("generateMessageID dropped the hostname: %q does not end with @mx.example.com>", id)
	}
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
		t.Errorf("generateMessageID must wrap in angle brackets: %q", id)
	}
}

// TestGenerateMessageID_NoLeakedLocalhost guards against the previous
// behaviour where production paths were calling generateMessageID("")
// (because the hostname was not plumbed through Email/set's handler)
// and the resulting Message-IDs ended in "@localhost". A real
// hostname round-trip MUST never produce "localhost".
func TestGenerateMessageID_NoLeakedLocalhost(t *testing.T) {
	id := generateMessageID("mx.netzhansa.com")
	if strings.Contains(id, "localhost") {
		t.Errorf("Message-ID contains 'localhost' even with a real hostname: %q", id)
	}
}

// TestFormatAddresses_EmptyName formats a bare addr-spec when the display
// name is the empty string.
func TestFormatAddresses_EmptyName(t *testing.T) {
	got := formatAddresses([]emailAddress{{Name: "", Email: "alice@example.com"}})
	// net/mail.Address.String() yields "<alice@example.com>" for an empty name.
	want := "<alice@example.com>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFormatAddresses_ASCIIName formats a normal ASCII display name.
func TestFormatAddresses_ASCIIName(t *testing.T) {
	got := formatAddresses([]emailAddress{{Name: "Alice Smith", Email: "alice@example.com"}})
	parsed, err := mail.ParseAddressList(got)
	if err != nil {
		t.Fatalf("ParseAddressList(%q): %v", got, err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 address, got %d: %v", len(parsed), parsed)
	}
	if parsed[0].Name != "Alice Smith" {
		t.Fatalf("Name: got %q, want %q", parsed[0].Name, "Alice Smith")
	}
	if parsed[0].Address != "alice@example.com" {
		t.Fatalf("Address: got %q, want %q", parsed[0].Address, "alice@example.com")
	}
}

// TestFormatAddresses_UTF8Name formats a display name containing non-ASCII
// characters (RFC 2047 Q-encoding should be applied).
func TestFormatAddresses_UTF8Name(t *testing.T) {
	got := formatAddresses([]emailAddress{{Name: "Renée Düpré", Email: "renee@example.com"}})
	parsed, err := mail.ParseAddressList(got)
	if err != nil {
		t.Fatalf("ParseAddressList(%q): %v", got, err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 address, got %d", len(parsed))
	}
	if parsed[0].Name != "Renée Düpré" {
		t.Fatalf("Name round-trip: got %q", parsed[0].Name)
	}
	if parsed[0].Address != "renee@example.com" {
		t.Fatalf("Address: got %q", parsed[0].Address)
	}
}

// TestFormatAddresses_NameEqualsEmail is the regression test for the bug:
// when the display name contains "@" (typical when an autocomplete entry's
// name is set to the email address), the old code emitted
//
//	admin@example.com <admin@example.com>
//
// which is not a legal name-addr because the unquoted phrase
// "admin@example.com" contains specials. net/mail and enmime both split it
// into two addresses. The fixed code must produce exactly one address after
// a round-trip through each parser.
func TestFormatAddresses_NameEqualsEmail(t *testing.T) {
	addrs := []emailAddress{{Name: "admin@example.com", Email: "admin@example.com"}}
	got := formatAddresses(addrs)

	// Round-trip through net/mail.
	parsed, err := mail.ParseAddressList(got)
	if err != nil {
		t.Fatalf("ParseAddressList(%q): %v", got, err)
	}
	if len(parsed) != 1 {
		t.Fatalf("net/mail round-trip: expected 1 address, got %d: %v", len(parsed), parsed)
	}
	if parsed[0].Name != "admin@example.com" {
		t.Fatalf("net/mail Name: got %q", parsed[0].Name)
	}
	if parsed[0].Address != "admin@example.com" {
		t.Fatalf("net/mail Address: got %q", parsed[0].Address)
	}

	// Round-trip through enmime to nail the original regression.
	raw := fmt.Sprintf("From: sender@example.com\r\nTo: %s\r\nSubject: test\r\n\r\nbody\r\n", got)
	env, err := enmime.ReadEnvelope(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("enmime.ReadEnvelope: %v", err)
	}
	enmimeAddrs, err := env.AddressList("To")
	if err != nil {
		t.Fatalf("enmime AddressList(To): %v", err)
	}
	if len(enmimeAddrs) != 1 {
		t.Fatalf("enmime round-trip: expected 1 address, got %d: %v", len(enmimeAddrs), enmimeAddrs)
	}
	if enmimeAddrs[0].Address != "admin@example.com" {
		t.Fatalf("enmime Address: got %q", enmimeAddrs[0].Address)
	}
}

// TestFormatAddresses_NameWithSpecials verifies that display names
// containing other RFC 5322 specials (comma, angle bracket, colon) are
// also handled without producing malformed output.
func TestFormatAddresses_NameWithSpecials(t *testing.T) {
	cases := []struct {
		name  string
		email string
	}{
		{"Smith, Alice", "alice@example.com"},
		{"<Alias>", "alias@example.com"},
		{"Dept: Eng", "eng@example.com"},
	}
	for _, c := range cases {
		got := formatAddresses([]emailAddress{{Name: c.name, Email: c.email}})
		parsed, err := mail.ParseAddressList(got)
		if err != nil {
			t.Errorf("name=%q: ParseAddressList(%q): %v", c.name, got, err)
			continue
		}
		if len(parsed) != 1 {
			t.Errorf("name=%q: expected 1 address, got %d from %q", c.name, len(parsed), got)
			continue
		}
		if parsed[0].Name != c.name {
			t.Errorf("name=%q: round-trip Name: got %q", c.name, parsed[0].Name)
		}
		if parsed[0].Address != c.email {
			t.Errorf("name=%q: round-trip Address: got %q", c.name, parsed[0].Address)
		}
	}
}

// TestFormatAddresses_Multiple checks that a slice with multiple entries
// is rendered as a comma-separated list and all addresses survive a
// round-trip.
func TestFormatAddresses_Multiple(t *testing.T) {
	addrs := []emailAddress{
		{Name: "Alice", Email: "alice@example.com"},
		{Name: "bob@example.com", Email: "bob@example.com"}, // name == email regression
		{Name: "", Email: "carol@example.com"},
	}
	got := formatAddresses(addrs)
	parsed, err := mail.ParseAddressList(got)
	if err != nil {
		t.Fatalf("ParseAddressList(%q): %v", got, err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 addresses, got %d: %v", len(parsed), parsed)
	}
}

// TestFormatAddresses_SkipsEmptyEmail ensures that entries with an empty
// Email field are silently dropped.
func TestFormatAddresses_SkipsEmptyEmail(t *testing.T) {
	addrs := []emailAddress{
		{Name: "Nobody", Email: ""},
		{Name: "Alice", Email: "alice@example.com"},
	}
	got := formatAddresses(addrs)
	parsed, err := mail.ParseAddressList(got)
	if err != nil {
		t.Fatalf("ParseAddressList(%q): %v", got, err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 address (empty Email skipped), got %d: %v", len(parsed), parsed)
	}
}

// TestMediaParam_PlainTokenUnquoted covers issue #512: a safe RFC 2045
// token value (no spaces, no tspecials) must be written unquoted, exactly
// as before the fix.
func TestMediaParam_PlainTokenUnquoted(t *testing.T) {
	got := mediaParam("filename", "report.pdf")
	want := `filename=report.pdf`
	if got != want {
		t.Errorf("mediaParam(filename, report.pdf) = %q, want %q", got, want)
	}
}

// TestMediaParam_SpacesQuotesSemicolons is the exact-header-bytes
// regression test for issue #512: a filename containing spaces,
// double-quotes, or a semicolon is a tspecial-bearing value that RFC 2045
// section 5.1 requires to be a quoted-string, not written bare the way
// the pre-fix mime.QEncoding.Encode concatenation did (which silently
// produced an invalid, truncatable header for "Offer_ Draft with
// spaces.pdf").
func TestMediaParam_SpacesQuotesSemicolons(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "spaces and underscore (the reported name)",
			value: "Offer_ Draft with spaces.pdf",
			want:  `filename="Offer_ Draft with spaces.pdf"`,
		},
		{
			name:  "embedded double quotes",
			value: `has "quotes".pdf`,
			want:  `filename="has \"quotes\".pdf"`,
		},
		{
			name:  "semicolon",
			value: "semi;colon.pdf",
			want:  `filename="semi;colon.pdf"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mediaParam("filename", c.value)
			if got != c.want {
				t.Errorf("mediaParam(filename, %q) = %q, want %q", c.value, got, c.want)
			}
		})
	}
}

// TestMediaParam_NonASCII_UmlautsAndCJK is the exact-header-bytes
// regression test for the RFC 2231 branch: a non-ASCII name is written as
// both an ASCII-safe "name=" fallback (for parsers that only understand
// RFC 2045) and the exact UTF-8 name under the RFC 2231 extended "name*="
// form, matching the ticket's acceptance criterion for umlauts and CJK
// characters.
func TestMediaParam_NonASCII_UmlautsAndCJK(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "umlauts",
			value: "Übung Prüfung.pdf", // "Übung Prüfung.pdf"
			want:  `name="_bung Pr_fung.pdf"; name*=utf-8''%C3%9Cbung%20Pr%C3%BCfung.pdf`,
		},
		{
			name:  "CJK",
			value: "日本語ファイル.pdf", // "日本語ファイル.pdf"
			want:  `name=_______.pdf; name*=utf-8''%E6%97%A5%E6%9C%AC%E8%AA%9E%E3%83%95%E3%82%A1%E3%82%A4%E3%83%AB.pdf`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mediaParam("name", c.value)
			if got != c.want {
				t.Errorf("mediaParam(name, %q) = %q, want %q", c.value, got, c.want)
			}
		})
	}
}

// TestMediaParam_RoundTripsThroughMailparse builds a minimal multipart/mixed
// message whose attachment part's Content-Type/Content-Disposition
// headers are produced by mediaParam, feeds it through
// internal/mailparse.Parse (the production parser used by Email/get), and
// checks the recovered Part.Filename matches the original name exactly --
// for the plain, quoted, and RFC 2231 extended forms. This is the
// writer/parser round trip issue #512 asks for: mediaParam's output must
// be exactly what mailparse already reads correctly.
func TestMediaParam_RoundTripsThroughMailparse(t *testing.T) {
	names := []string{
		"report.pdf",
		"Offer_ Draft with spaces.pdf",
		`has "quotes".pdf`,
		"semi;colon.pdf",
		"Übung Prüfung.pdf",
		"日本語ファイル.pdf",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctParam := mediaParam("name", name)
			dispParam := mediaParam("filename", name)
			raw := strings.Join([]string{
				"From: sender@example.test",
				"To: rcpt@example.test",
				"Subject: attachment name round trip",
				"MIME-Version: 1.0",
				`Content-Type: multipart/mixed; boundary="b"`,
				"",
				"--b",
				"Content-Type: text/plain; charset=utf-8",
				"",
				"see attached",
				"--b",
				"Content-Type: application/pdf; " + ctParam,
				"Content-Disposition: attachment; " + dispParam,
				"Content-Transfer-Encoding: base64",
				"",
				"AAAA",
				"--b--",
			}, "\r\n")

			msg, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
			if err != nil {
				t.Fatalf("mailparse.Parse: %v", err)
			}
			if len(msg.Body.Children) != 2 {
				t.Fatalf("expected 2 parts, got %d", len(msg.Body.Children))
			}
			att := msg.Body.Children[1]
			if att.Filename != name {
				t.Errorf("Filename = %q, want %q (raw=\n%s)", att.Filename, name, raw)
			}
			if att.ContentType != "application/pdf" {
				t.Errorf("ContentType = %q, want application/pdf", att.ContentType)
			}
			if att.Disposition != mailparse.DispositionAttachment {
				t.Errorf("Disposition = %v, want DispositionAttachment", att.Disposition)
			}
		})
	}
}
