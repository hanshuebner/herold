package srs_test

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/srs"
)

var testSecret = []byte("test-secret-do-not-use-in-prod!")
var otherSecret = []byte("a-different-secret-material-here")

func TestEncodeDecodeRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, now, "alice", "sender.example")
	if !srs.IsSRSAddress(encoded) {
		t.Fatalf("IsSRSAddress(%q) = false; want true", encoded)
	}
	local, domain, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if local != "alice" || domain != "sender.example" {
		t.Errorf("Decode = (%q, %q); want (alice, sender.example)", local, domain)
	}
}

func TestEncodeDecodeRoundTrip_LocalPartWithEquals(t *testing.T) {
	// A local-part containing "=" (rare but legal) must survive the
	// SplitN(4) parse unmangled, since the delimiter is reused.
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, now, "weird=local=part", "sender.example")
	local, domain, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if local != "weird=local=part" || domain != "sender.example" {
		t.Errorf("Decode = (%q, %q); want (weird=local=part, sender.example)", local, domain)
	}
}

func TestDecode_TamperedHashRejected(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, now, "alice", "sender.example")
	// Flip a character in the hash segment (right after "SRS0=").
	tampered := "SRS0=XXXX" + encoded[len("SRS0=HHHH"):]
	_, _, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, tampered)
	if err != srs.ErrTamper {
		t.Fatalf("Decode(tampered) = %v; want ErrTamper", err)
	}
}

func TestDecode_WrongSecretRejected(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, now, "alice", "sender.example")
	_, _, err := srs.Decode([][]byte{otherSecret}, now, srs.MaxAgeDefault, encoded)
	if err != srs.ErrTamper {
		t.Fatalf("Decode(wrong secret) = %v; want ErrTamper", err)
	}
}

func TestDecode_RotatedSecretStillVerifies(t *testing.T) {
	// Rotation: newest secret signs, but decode must try every known
	// secret so an address signed before rotation keeps validating.
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, now, "alice", "sender.example")
	local, domain, err := srs.Decode([][]byte{testSecret, otherSecret}, now, srs.MaxAgeDefault, encoded)
	if err != nil {
		t.Fatalf("Decode with rotated secret set: %v", err)
	}
	if local != "alice" || domain != "sender.example" {
		t.Errorf("Decode = (%q, %q); want (alice, sender.example)", local, domain)
	}
}

func TestDecode_StaleTimestampRejected(t *testing.T) {
	signedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, signedAt, "alice", "sender.example")
	verifyAt := signedAt.Add(srs.MaxAgeDefault + 48*time.Hour)
	_, _, err := srs.Decode([][]byte{testSecret}, verifyAt, srs.MaxAgeDefault, encoded)
	if err != srs.ErrStale {
		t.Fatalf("Decode(stale) = %v; want ErrStale", err)
	}
}

func TestDecode_WithinMaxAgeAccepted(t *testing.T) {
	signedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	encoded := srs.EncodeSRS0(testSecret, signedAt, "alice", "sender.example")
	verifyAt := signedAt.Add(10 * 24 * time.Hour)
	_, _, err := srs.Decode([][]byte{testSecret}, verifyAt, srs.MaxAgeDefault, encoded)
	if err != nil {
		t.Fatalf("Decode within maxAge: %v", err)
	}
}

func TestWrap_PlainAddress_ProducesSRS0(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	wrapped := srs.Wrap(testSecret, now, "alice", "sender.example")
	if wrapped[:5] != "SRS0=" {
		t.Fatalf("Wrap(plain) = %q; want SRS0= prefix", wrapped)
	}
}

// TestWrap_AlreadySRS_UsesSRS1WithoutNesting covers the "double forward"
// requirement (issue #204): forwarding a message whose MAIL FROM is
// already an SRS address must produce SRS1, not a second SRS0 layer
// wrapped around the first.
func TestWrap_AlreadySRS_UsesSRS1WithoutNesting(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	// Hop 1: alice@sender.example is forwarded through this server's
	// "relay-one.example" domain. The resulting MAIL FROM is
	// firstHop@relay-one.example.
	firstHop := srs.EncodeSRS0(testSecret, now, "alice", "sender.example")

	// Hop 2: the message (MAIL FROM firstHop@relay-one.example) is
	// forwarded again, through "relay-two.example". Wrap sees the
	// CURRENT MAIL FROM is already SRS and produces SRS1, embedding the
	// current (local, domain) pair as an opaque unit rather than nesting
	// a second SRS0 layer.
	secondHop := srs.Wrap(testSecret, now, firstHop, "relay-one.example")
	if secondHop[:5] != "SRS1=" {
		t.Fatalf("Wrap(already-SRS) = %q; want SRS1= prefix", secondHop)
	}
	local, domain, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, secondHop)
	if err != nil {
		t.Fatalf("Decode(SRS1): %v", err)
	}
	if local != firstHop || domain != "relay-one.example" {
		t.Errorf("Decode(SRS1) = (%q, %q); want (%q, relay-one.example)", local, domain, firstHop)
	}

	// Hop 3: forwarded again, through "relay-three.example". Must still
	// be a single SRS1 layer, not growing with each hop.
	thirdHop := srs.Wrap(testSecret, now, secondHop, "relay-two.example")
	if thirdHop[:5] != "SRS1=" {
		t.Fatalf("Wrap(third hop) = %q; want SRS1= prefix", thirdHop)
	}
	local2, domain2, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, thirdHop)
	if err != nil {
		t.Fatalf("Decode(third hop): %v", err)
	}
	if local2 != secondHop || domain2 != "relay-two.example" {
		t.Errorf("Decode(third hop) = (%q, %q); want (%q, relay-two.example)", local2, domain2, secondHop)
	}

	// Recursively decoding the recovered chain, one layer per hop,
	// eventually reaches the original sender.
	local3, domain3, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, local2)
	if err != nil {
		t.Fatalf("Decode(recovered hop 2): %v", err)
	}
	if local3 != firstHop || domain3 != "relay-one.example" {
		t.Errorf("Decode(recovered hop 2) = (%q, %q); want (%q, relay-one.example)", local3, domain3, firstHop)
	}
	final, finalDomain, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, local3)
	if err != nil {
		t.Fatalf("Decode(recovered hop 1): %v", err)
	}
	if final != "alice" || finalDomain != "sender.example" {
		t.Errorf("fully-unwound chain = (%q, %q); want (alice, sender.example)", final, finalDomain)
	}
}

func TestDecode_NotAnSRSAddress(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	_, _, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, "alice")
	if err != srs.ErrNotSRS {
		t.Fatalf("Decode(plain) = %v; want ErrNotSRS", err)
	}
}

func TestDecode_MalformedSRS0(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for _, bad := range []string{
		"SRS0=",
		"SRS0=hash",
		"SRS0=hash=ts",
		"SRS0=hash=ts=domain",
		"SRS0=hash=ts==",
	} {
		if _, _, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, bad); err == nil {
			t.Errorf("Decode(%q) = nil error; want an error", bad)
		}
	}
}

func TestDecode_MalformedSRS1(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for _, bad := range []string{
		"SRS1=",
		"SRS1=hash",
		"SRS1=hash=domainwithoutseparator",
	} {
		if _, _, err := srs.Decode([][]byte{testSecret}, now, srs.MaxAgeDefault, bad); err == nil {
			t.Errorf("Decode(%q) = nil error; want an error", bad)
		}
	}
}
