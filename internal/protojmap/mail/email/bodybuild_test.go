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
