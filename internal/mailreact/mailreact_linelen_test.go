package mailreact

import (
	"strings"
	"testing"
	"time"
)

// TestBuildReactionBody_LineLengths: a long reactor display name and a
// long References chain keep every line within 998 octets.
func TestBuildReactionBody_LineLengths(t *testing.T) {
	var refs []string
	for i := 0; i < 60; i++ {
		refs = append(refs, "<ref-0123456789abcdef0123456789abcdef@mail.example.test>")
	}
	orig := OriginalEmailInfo{
		MessageID:  "orig@mail.example.test",
		References: strings.Join(refs, " "),
		Subject:    strings.Repeat("Gr\u00fc\u00dfe aus M\u00fcnchen ", 40),
	}
	name := strings.Repeat("Longname ", 300)
	raw, err := buildReactionBody(name, "me@example.test", "heart", orig, time.Unix(1794121361, 0), "mail.example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\r\n") {
		if len(l) > 998 {
			t.Fatalf("line of %d octets", len(l))
		}
	}
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for _, l := range strings.Split(head, "\r\n") {
		if len(l) > 78 {
			t.Fatalf("header line of %d octets", len(l))
		}
	}
}
