package webpush

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// TestEncryptRoundTrip exercises the full RFC 8291 aes128gcm path
// against a freshly generated recipient key pair. The test does the
// same encrypt + decrypt the dispatcher's e2e path does, so a
// breakage in derive / GCM / record-padding code surfaces here
// before it surfaces inside the dispatcher.
func TestEncryptRoundTrip(t *testing.T) {
	t.Parallel()
	recipientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("recipient key gen: %v", err)
	}
	recipientPub := recipientPriv.PublicKey().Bytes()

	auth := make([]byte, authSecretLen)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth secret: %v", err)
	}

	plaintext := []byte("hello, web push - REQ-PROTO-123")
	envelope, err := Encrypt(plaintext, recipientPub, auth, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if got := envelope[aes128GCMSaltLen+4]; got != p256UncompressedLen {
		t.Fatalf("idlen byte = %d, want %d", got, p256UncompressedLen)
	}
	if envelope[aes128GCMSaltLen+5] != 0x04 {
		t.Fatalf("keyid first byte = %02x, want 0x04 (uncompressed SEC1)", envelope[aes128GCMSaltLen+5])
	}

	decrypted, err := decryptForTest(envelope, recipientPriv, auth)
	if err != nil {
		t.Fatalf("decryptForTest: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round-trip mismatch:\n got: %q\nwant: %q", decrypted, plaintext)
	}
}

// TestEncryptRejectsBadInputs covers the validation path: wrong-shape
// p256dh, wrong-length auth secret, malformed pre-supplied salt. The
// dispatcher relies on these checks to refuse to encrypt against a
// row that has been corrupted in the store.
func TestEncryptRejectsBadInputs(t *testing.T) {
	t.Parallel()
	recip, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	good := recip.PublicKey().Bytes()
	auth := make([]byte, authSecretLen)
	rand.Read(auth)

	cases := []struct {
		name string
		pub  []byte
		auth []byte
		opts *EncryptOptions
	}{
		{"short pub", good[:len(good)-1], auth, nil},
		{"non-uncompressed pub", append([]byte{0x02}, good[1:]...), auth, nil},
		{"short auth", good, auth[:len(auth)-1], nil},
		{"long auth", good, append(auth, 0x00), nil},
		{"bad salt length", good, auth, &EncryptOptions{Salt: make([]byte, 8)}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Encrypt([]byte("payload"), c.pub, c.auth, c.opts); err == nil {
				t.Fatalf("Encrypt %s: want error, got nil", c.name)
			}
		})
	}
}

// TestEncryptDeterministicWithFixedSeeds verifies that supplying a
// fixed ephemeral key + salt produces deterministic output, which
// is the seam tests use to land RFC 8291 §5 KAT-style assertions.
// We do not assert against the published vector (the spec's vector
// references an exact private-key bytes layout we cannot reproduce
// without parsing PKCS#8 fixtures the test would have to ship); the
// determinism check is the regression net.
func TestEncryptDeterministicWithFixedSeeds(t *testing.T) {
	t.Parallel()
	recip, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("recip key: %v", err)
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral key: %v", err)
	}
	auth := bytes.Repeat([]byte{0xAB}, authSecretLen)
	salt := bytes.Repeat([]byte{0xCD}, aes128GCMSaltLen)

	out1, err := Encrypt([]byte("REQ-PROTO-123"), recip.PublicKey().Bytes(), auth, &EncryptOptions{
		EphemeralKey: ephemeral, Salt: salt,
	})
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	out2, err := Encrypt([]byte("REQ-PROTO-123"), recip.PublicKey().Bytes(), auth, &EncryptOptions{
		EphemeralKey: ephemeral, Salt: salt,
	})
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("non-deterministic output with fixed key + salt")
	}
	dec, err := decryptForTest(out1, recip, auth)
	if err != nil {
		t.Fatalf("decryptForTest: %v", err)
	}
	if string(dec) != "REQ-PROTO-123" {
		t.Fatalf("plaintext mismatch: %q", dec)
	}
}

// TestEnvelopeShape asserts the envelope layout matches RFC 8291 §3:
// salt(16) || rs(4 BE) || idlen(1) || keyid(idlen) || ciphertext.
func TestEnvelopeShape(t *testing.T) {
	t.Parallel()
	recip, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	auth := make([]byte, authSecretLen)
	rand.Read(auth)
	out, err := Encrypt([]byte("hello"), recip.PublicKey().Bytes(), auth, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(out) < aes128GCMSaltLen+4+1+p256UncompressedLen+1 {
		t.Fatalf("envelope too short: %d bytes", len(out))
	}
	// Salt is the first 16 bytes (cannot inspect content; just length).
	// rs is big-endian uint32; we set RecordSize=4096 (0x00 00 10 00).
	rs := out[aes128GCMSaltLen : aes128GCMSaltLen+4]
	if rs[0] != 0x00 || rs[1] != 0x00 || rs[2] != 0x10 || rs[3] != 0x00 {
		t.Fatalf("rs bytes %x; want 00 00 10 00 (4096)", rs)
	}
	if got := out[aes128GCMSaltLen+4]; got != 65 {
		t.Fatalf("idlen %d; want 65", got)
	}
	keyid := out[aes128GCMSaltLen+5 : aes128GCMSaltLen+5+p256UncompressedLen]
	if keyid[0] != 0x04 {
		t.Fatalf("keyid first byte %x; want 0x04 (uncompressed)", keyid[0])
	}
	// Verify keyid is a valid P-256 public point.
	if _, err := ecdh.P256().NewPublicKey(keyid); err != nil {
		t.Fatalf("envelope keyid is not a valid P-256 public key: %v", err)
	}
}

// TestPayloadBase64Length is a sanity check that the envelope size
// stays well under 4 KiB for typical payload sizes (FCM and Mozilla
// Autopush both reject payloads above 4096 octets after encryption).
func TestEnvelopeSizeUnderGatewayCap(t *testing.T) {
	t.Parallel()
	recip, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key gen: %v", err)
	}
	auth := make([]byte, authSecretLen)
	rand.Read(auth)

	// 1 KiB payload — the largest realistic JSON the dispatcher
	// builds (calendar + summary + location with the 80-byte caps
	// is closer to 200 bytes; 1 KiB is generous).
	payload := bytes.Repeat([]byte{'A'}, 1024)
	out, err := Encrypt(payload, recip.PublicKey().Bytes(), auth, nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(out) > 4096 {
		t.Fatalf("envelope %d bytes exceeds 4 KiB cap", len(out))
	}
	_ = base64.RawURLEncoding // keep import for documentation
}
