package emailsubmission

import (
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestBuildEnvelope_UnsafeAddressesNeverReachEnvelope: neither an explicit
// envelope nor the header-derived fallback may hand the queue an address
// that could carry SMTP parameters (re #562).
func TestBuildEnvelope_UnsafeAddressesNeverReachEnvelope(t *testing.T) {
	bad := "bob@dest.test> NOTIFY=NEVER <x@dest.test"
	t.Run("explicit-mailfrom", func(t *testing.T) {
		_, _, serr := buildEnvelope(&jmapEnvelope{
			MailFrom: jmapAddress{Email: "a@b.test> RET=FULL <x"},
			RcptTo:   []jmapAddress{{Email: "bob@dest.test"}},
		}, "id@example.test", store.Message{})
		if serr == nil {
			t.Fatalf("unsafe mailFrom accepted")
		}
	})
	t.Run("explicit-rcpt-space", func(t *testing.T) {
		_, _, serr := buildEnvelope(&jmapEnvelope{
			RcptTo: []jmapAddress{{Email: "\"a b\"@dest.test"}},
		}, "id@example.test", store.Message{})
		if serr == nil {
			t.Fatalf("recipient with whitespace accepted")
		}
	})
	t.Run("header-derived", func(t *testing.T) {
		for _, hdr := range []string{
			"x <" + bad + ">",
			"bob@dest.test NOTIFY=NEVER",
			"Bob <bob@dest.test foo>",
		} {
			m := store.Message{}
			m.Envelope.To = hdr
			_, rcpts, _ := buildEnvelope(nil, "id@example.test", m)
			for _, r := range rcpts {
				if r != "bob@dest.test" {
					t.Errorf("header %q produced envelope recipient %q", hdr, r)
				}
			}
		}
	})
}
