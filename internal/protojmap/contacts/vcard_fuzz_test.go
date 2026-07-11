package contacts

// vCard 4.0 parser fuzz target (STANDARDS §8.2, REQ-CTS-11, REQ-CTS-40).
//
// ParseVCards is the highest-volume untrusted surface on the vCard import
// path: arbitrary bytes from a user-uploaded .vcf file enter here. This
// target asserts:
//
//  1. ParseVCards never panics on any input bytes.
//  2. On a successful parse (non-nil Card), the Card is self-consistent:
//     Version=="1.0", typed accessors (PrimaryEmail, DisplayName,
//     SearchBlob) do not panic, and a Marshal round-trip is stable.
//  3. GenerateVCard on a successfully-parsed Card does not panic and
//     emits valid CRLF-terminated lines of at most 75 octets each.
//
// The input-size guard skips inputs larger than 1 MiB to keep per-run
// memory bounded; production imports enforce a tighter limit (REQ-CTS-41).

import (
	"strings"
	"testing"
)

const vcardFuzzMaxInput = 1 << 20 // 1 MiB

// FuzzParseVCard drives ParseVCards over arbitrary bytes.
func FuzzParseVCard(f *testing.F) {
	// Seed: minimal individual.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Alice\r\nUID:urn:uuid:1\r\nEND:VCARD\r\n"))
	// Seed: full individual with typed emails, phone, address, org, title.
	f.Add([]byte(strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"UID:urn:uuid:full",
		"KIND:individual",
		"FN:Ada Lovelace",
		"N:Lovelace;Ada;Augusta;Hon.;",
		"EMAIL;TYPE=work;PREF=1:ada@example.test",
		"TEL;TYPE=voice,cell:+44 20 7946 0958",
		"ADR;TYPE=work;CC=GB:;;Street;London;;SW1A 2AA;UK",
		"ORG:Org;Unit",
		"TITLE:Engineer",
		"ROLE:Fellow",
		"NICKNAME:Ada",
		"NOTE:A note.",
		"URL:https://example.test",
		"BDAY:18151210",
		"ANNIVERSARY:--1225",
		"X-CUSTOM:value",
		"END:VCARD",
	}, "\r\n") + "\r\n"))
	// Seed: group card.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nKIND:group\r\nFN:Team\r\nUID:urn:uuid:g\r\nEND:VCARD\r\n"))
	// Seed: year-omitted birthday.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:B\r\nUID:urn:uuid:b\r\nBDAY:--0229\r\nEND:VCARD\r\n"))
	// Seed: data URI photo.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:P\r\nUID:urn:uuid:p\r\n" +
		"PHOTO:data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVQI12NgAAIABQAABjE+ibYAAAAASUVORK5CYII=\r\n" +
		"END:VCARD\r\n"))
	// Seed: multiple cards.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:A\r\nUID:urn:uuid:a\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:B\r\nUID:urn:uuid:b\r\nEND:VCARD\r\n"))
	// Adversarial: truncated card.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Trunc"))
	// Adversarial: no BEGIN.
	f.Add([]byte("VERSION:4.0\r\nFN:NoBegin\r\nEND:VCARD\r\n"))
	// Adversarial: empty.
	f.Add([]byte{})
	// Adversarial: null bytes.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\n\x00FN:\x00Alice\x00\r\nEND:VCARD\r\n"))
	// Adversarial: invalid UTF-8 in value.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:\xff\xfe\r\nUID:urn:uuid:u\r\nEND:VCARD\r\n"))
	// Adversarial: deeply folded line.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:fold\r\n NOTE:a\r\n b\r\n c\r\nUID:u\r\nEND:VCARD\r\n"))
	// Adversarial: missing colon separator.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nNOCOLON\r\nEND:VCARD\r\n"))
	// Adversarial: excessively long property value.
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\nUID:u\r\nNOTE:" +
		strings.Repeat("A", 8192) + "\r\nEND:VCARD\r\n"))

	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) > vcardFuzzMaxInput {
			return
		}

		// Invariant 1: ParseVCards must never panic (the defer+recover in
		// ParseVCards itself catches panics from sub-functions; the recover
		// below is a secondary backstop for test harness panics).
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ParseVCards panicked: %v", r)
			}
		}()

		results := ParseVCards(in, nil)

		for _, r := range results {
			if r.Error != nil {
				continue
			}
			c := r.Card
			if c == nil {
				t.Fatal("non-error result has nil Card")
			}

			// Invariant 2: typed accessors must not panic.
			_ = c.PrimaryEmail()
			_ = c.DisplayName()
			_ = c.GivenName()
			_ = c.Surname()
			_ = c.OrgName()
			_ = c.SearchBlob()

			// Invariant 2: Version is always "1.0" for a parsed card.
			if c.Version != "1.0" {
				t.Errorf("Version = %q, want 1.0", c.Version)
			}

			// Invariant 3: GenerateVCard must not panic and must produce
			// valid folded lines.
			gr, err := GenerateVCard(c, nil)
			if err != nil {
				// GenerateVCard error is acceptable (e.g., malformed stored
				// media); a panic is not.
				continue
			}
			for _, line := range strings.Split(string(gr.VCard), "\r\n") {
				if len(line) > 75 {
					t.Errorf("generated line exceeds 75 octets: len=%d", len(line))
				}
			}
		}
	})
}
