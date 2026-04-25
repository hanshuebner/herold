package contacts

// JSContact Card JSON-decoder fuzz target (STANDARDS section 8.2).
// Track C of Wave 2.9.5: untrusted JSON arrives via JMAP
// Contact/set; the Card.UnmarshalJSON path projects typed fields
// from a free-form RFC 9553 object. A panic here would translate
// into a JMAP-method crash visible to the client.
//
// Invariants:
//
//   1. UnmarshalJSON never panics on any input.
//   2. On a successful parse, c.Version, c.Kind, c.UID are valid
//      UTF-8 strings.
//   3. PrimaryEmail / DisplayName / SearchBlob are total functions
//      and never panic on the parsed Card (they are routinely called
//      by the store-side denormalisation path).
//   4. A Marshal/Unmarshal cycle preserves the typed projections.

import (
	"testing"
	"unicode/utf8"
)

// FuzzCardUnmarshalJSON drives Card.UnmarshalJSON over arbitrary
// bytes.
func FuzzCardUnmarshalJSON(f *testing.F) {
	// Seed 1: minimal individual.
	f.Add([]byte(`{"version":"1.0","kind":"individual","uid":"u1","name":{"full":"Alice"}}`))
	// Seed 2: complex org with x.example/foo extension.
	f.Add([]byte(`{"version":"1.0","kind":"org","uid":"u2","name":{"full":"Acme Inc","components":[{"kind":"given","value":"Acme"},{"kind":"surname","value":"Inc"}]},"emails":{"work":{"address":"info@acme.example","contexts":{"work":true},"pref":1}},"phones":{"main":{"number":"+1-555-0100"}},"organizations":{"o1":{"name":"Acme","units":["R&D","Sales"]}},"titles":{"t1":{"name":"CEO"}},"x.example/foo":{"customField":42,"nested":{"a":[1,2,3]}}}`))
	// Seed 3: invalid kind + null emails.
	f.Add([]byte(`{"version":"1.0","kind":"banana","uid":"u3","emails":null,"name":null,"phones":{"p":null}}`))
	// Adversarial.
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`[1,2]`))
	f.Add([]byte(`{"version":"2.0","uid":""}`))
	f.Add([]byte(`{"name":{"components":[{"kind":"given"},{"value":"NoKind"}]}}`))

	f.Fuzz(func(t *testing.T, in []byte) {
		var c Card
		if err := c.UnmarshalJSON(in); err != nil {
			return
		}
		if c.Version != "" && !utf8.ValidString(c.Version) {
			t.Fatalf("Version not utf8: %q", c.Version)
		}
		if c.Kind != "" && !utf8.ValidString(c.Kind) {
			t.Fatalf("Kind not utf8: %q", c.Kind)
		}
		if c.UID != "" && !utf8.ValidString(c.UID) {
			t.Fatalf("UID not utf8: %q", c.UID)
		}
		// These are total functions; they must never panic.
		_ = c.PrimaryEmail()
		_ = c.DisplayName()
		_ = c.GivenName()
		_ = c.Surname()
		_ = c.OrgName()
		_ = c.SearchBlob()
		// Marshal/Unmarshal cycle. encoding/json replaces non-UTF-8
		// bytes with U+FFFD on emission, so the byte-equal UID
		// invariant only holds when the inbound UID was already
		// valid UTF-8.
		body, err := c.MarshalJSON()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var round Card
		if err := round.UnmarshalJSON(body); err != nil {
			t.Fatalf("re-unmarshal failed on canonical body: %v", err)
		}
		if utf8.ValidString(c.UID) && round.UID != c.UID {
			t.Fatalf("UID lost across cycle: %q -> %q", c.UID, round.UID)
		}
	})
}
