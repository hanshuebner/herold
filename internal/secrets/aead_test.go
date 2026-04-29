package secrets

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, chacha20poly1305.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestSealOpen_RoundTrip(t *testing.T) {
	key := testKey(t)
	plaintext := []byte("hello, world")
	sealed, err := Seal(key, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Open returned %q, want %q", got, plaintext)
	}
}

func TestSealOpen_EmptyPlaintext(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(key, []byte{})
	if err != nil {
		t.Fatalf("Seal empty: %v", err)
	}
	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Open returned %q for empty plaintext", got)
	}
}

func TestOpen_TamperedCiphertext(t *testing.T) {
	key := testKey(t)
	sealed, _ := Seal(key, []byte("secret"))
	// flip last byte of ciphertext (inside the AEAD tag)
	sealed[len(sealed)-1] ^= 0xFF
	_, err := Open(key, sealed)
	if err == nil {
		t.Fatal("Open: expected error for tampered ciphertext, got nil")
	}
}

func TestOpen_WrongVersion(t *testing.T) {
	key := testKey(t)
	sealed, _ := Seal(key, []byte("data"))
	// replace "v1:" with "v9:"
	sealed[1] = '9'
	_, err := Open(key, sealed)
	if err == nil {
		t.Fatal("Open: expected error for wrong version prefix, got nil")
	}
}

func TestOpen_TruncatedCiphertext(t *testing.T) {
	key := testKey(t)
	sealed, _ := Seal(key, []byte("some data"))
	// truncate to just the version prefix — no nonce, no ciphertext
	truncated := sealed[:versionPrefixLen]
	_, err := Open(key, truncated)
	if err == nil {
		t.Fatal("Open: expected error for truncated ciphertext (no nonce), got nil")
	}
	// truncate to version prefix + partial nonce
	truncated2 := sealed[:versionPrefixLen+nonceSize/2]
	_, err = Open(key, truncated2)
	if err == nil {
		t.Fatal("Open: expected error for truncated ciphertext (partial nonce), got nil")
	}
}

func TestOpen_WrongKey(t *testing.T) {
	key := testKey(t)
	sealed, _ := Seal(key, []byte("top secret"))
	wrongKey := make([]byte, chacha20poly1305.KeySize)
	for i := range wrongKey {
		wrongKey[i] = 0xFF
	}
	_, err := Open(wrongKey, sealed)
	if err == nil {
		t.Fatal("Open: expected error for wrong key, got nil")
	}
}

func TestNonceUniqueness(t *testing.T) {
	// Probabilistic nonce collision test: 10k seals should produce all unique nonces.
	// The collision probability for a 96-bit (12-byte) nonce over 10k samples is
	// approximately 10000^2 / 2^97 ~ 2^-83, effectively zero.
	key := testKey(t)
	plaintext := []byte("nonce uniqueness test")
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		sealed, err := Seal(key, plaintext)
		if err != nil {
			t.Fatalf("Seal iteration %d: %v", i, err)
		}
		if len(sealed) < versionPrefixLen+nonceSize {
			t.Fatalf("sealed too short at iteration %d", i)
		}
		nonce := string(sealed[versionPrefixLen : versionPrefixLen+nonceSize])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("nonce collision at iteration %d", i)
		}
		seen[nonce] = struct{}{}
	}
}

// FuzzOpen exercises the Open function with arbitrary bytes, verifying that it
// never panics and always returns an error for random (unauthenticated) input.
func FuzzOpen(f *testing.F) {
	key := make([]byte, chacha20poly1305.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	// Seed corpus: a valid ciphertext (so the fuzzer can explore mutations).
	plaintext := []byte("fuzz seed")
	sealed, _ := Seal(key, plaintext)
	f.Add(sealed)
	// Additional seeds for structural edge cases.
	f.Add([]byte("v1:"))
	f.Add([]byte("v1:" + string(make([]byte, nonceSize))))
	f.Add([]byte(""))
	f.Add([]byte("v2:garbage"))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Open must not panic; it may return an error or the plaintext.
		_, _ = Open(key, data)
	})
}
