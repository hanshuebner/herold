package aclcodec_test

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/aclcodec"
	"github.com/hanshuebner/herold/internal/store"
)

func TestEncode_CanonicalOrder(t *testing.T) {
	got := aclcodec.Encode(store.ACLRightsAll)
	if got != "lrswipkxtea" {
		t.Fatalf("Encode(All) = %q; want %q", got, "lrswipkxtea")
	}
	if got := aclcodec.Encode(0); got != "" {
		t.Fatalf("Encode(0) = %q; want empty", got)
	}
}

func TestEncode_Subset(t *testing.T) {
	// Setting bits out of order in the source mask must still produce
	// the canonical "lrswipkxtea" ordering on the wire.
	mask := store.ACLRightAdmin | store.ACLRightLookup | store.ACLRightRead
	got := aclcodec.Encode(mask)
	if got != "lra" {
		t.Fatalf("Encode = %q; want %q", got, "lra")
	}
}

func TestDecode_Roundtrip(t *testing.T) {
	for _, want := range []string{"", "l", "lr", "lra", "lrswipkxtea"} {
		got, err := aclcodec.Decode(want)
		if err != nil {
			t.Fatalf("Decode(%q): %v", want, err)
		}
		if back := aclcodec.Encode(got); back != want {
			t.Fatalf("roundtrip(%q) = %q", want, back)
		}
	}
}

func TestDecode_UnknownLetter(t *testing.T) {
	for _, in := range []string{"c", "d", "z", "lrZ"} {
		if _, err := aclcodec.Decode(in); err == nil {
			t.Fatalf("Decode(%q) accepted; want error", in)
		} else if !strings.Contains(err.Error(), "unknown ACL right") {
			t.Fatalf("Decode(%q) error %q does not mention unknown letter", in, err)
		}
	}
}

func TestDecode_DuplicateLetterIdempotent(t *testing.T) {
	got, err := aclcodec.Decode("lll")
	if err != nil {
		t.Fatalf("Decode(\"lll\"): %v", err)
	}
	if got != store.ACLRightLookup {
		t.Fatalf("Decode(\"lll\") = %v; want %v", got, store.ACLRightLookup)
	}
}
