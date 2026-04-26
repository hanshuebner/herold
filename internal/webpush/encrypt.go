package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// RFC 8291 fixed parameters.
const (
	// aes128GCMKeyLen is the CEK length (RFC 8291 §3.4).
	aes128GCMKeyLen = 16
	// aes128GCMNonceLen is the AES-GCM nonce length (RFC 8291 §3.4).
	aes128GCMNonceLen = 12
	// aes128GCMSaltLen is the salt prepended to the envelope.
	aes128GCMSaltLen = 16
	// aes128GCMRecordSize is the record-size field default (4096 is
	// the canonical "single record" value the spec recommends; large
	// enough to hold any push-gateway-acceptable payload).
	aes128GCMRecordSize uint32 = 4096
	// p256UncompressedLen is the uncompressed SEC1 P-256 public key
	// length (0x04 || X || Y).
	p256UncompressedLen = 65
	// authSecretLen is the recipient auth secret length (RFC 8291 §3.2).
	authSecretLen = 16
)

// HKDF info strings per RFC 8291 §3.3 / §3.4. The auth-info string
// terminates with a single 0x01 byte per the HKDF-Expand step the
// RFC bakes into the derivation of the IKM.
var (
	// hkdfAuthInfo is the info string used to derive the IKM from the
	// shared secret + recipient auth secret per RFC 8291 §3.3:
	//   info = "WebPush: info" || 0x00 || ua_public || as_public
	// where ua_public is the recipient (User Agent) P-256 public key
	// and as_public is the application server's ephemeral public key.
	hkdfAuthInfoPrefix = []byte("WebPush: info\x00")

	// hkdfKeyInfo / hkdfNonceInfo are the info strings used to
	// derive the CEK and nonce from the IKM + salt per RFC 8291 §3.4
	// composed with the aes128gcm record padding rules from
	// RFC 8188 §2.2.
	hkdfKeyInfo   = []byte("Content-Encoding: aes128gcm\x00")
	hkdfNonceInfo = []byte("Content-Encoding: nonce\x00")
)

// EncryptOptions tunes Encrypt for tests; production callers leave
// every field zero. The two seams the dispatcher uses are EphemeralKey
// (for known-answer tests) and Salt (likewise).
type EncryptOptions struct {
	// Rand replaces the default crypto/rand.Reader for ephemeral key
	// generation and salt minting. Tests pass deterministic readers.
	Rand io.Reader
	// EphemeralKey, when non-nil, replaces the freshly-generated
	// ephemeral key pair. Used by RFC 8291 §5 KAT verification.
	EphemeralKey *ecdh.PrivateKey
	// Salt, when non-nil, replaces the freshly-generated 16-byte salt.
	// Length must equal aes128GCMSaltLen; the validator returns an
	// error otherwise.
	Salt []byte
}

// Encrypt produces the RFC 8291 aes128gcm envelope for payload sent
// to the subscription identified by recipientPub (the 65-byte
// uncompressed SEC1 P-256 public key the client registered) and
// recipientAuth (the 16-byte auth secret).
//
// The envelope shape is:
//
//	salt(16) || rs(4 BE) || idlen(1=65) || keyid(65) || ciphertext
//
// where ciphertext is AES-128-GCM(payload || 0x02) under the CEK and
// nonce derived per RFC 8291 §3.4. The terminating 0x02 is the
// "delimiter byte for the last (and here, only) record" — RFC 8291
// §2 / RFC 8188 §2.1.
//
// opts is optional; a nil/zero EncryptOptions means "production
// settings" (crypto/rand for keys + salt, freshly generated ephemeral
// key, RecordSize=4096).
func Encrypt(payload, recipientPub, recipientAuth []byte, opts *EncryptOptions) ([]byte, error) {
	if len(recipientPub) != p256UncompressedLen || recipientPub[0] != 0x04 {
		return nil, fmt.Errorf("webpush: recipient p256dh must be the 65-byte uncompressed SEC1 form")
	}
	if len(recipientAuth) != authSecretLen {
		return nil, fmt.Errorf("webpush: recipient auth secret must be %d bytes (got %d)",
			authSecretLen, len(recipientAuth))
	}
	if opts == nil {
		opts = &EncryptOptions{}
	}
	rng := opts.Rand
	if rng == nil {
		rng = rand.Reader
	}

	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: parse recipient public key: %w", err)
	}

	asPriv := opts.EphemeralKey
	if asPriv == nil {
		asPriv, err = curve.GenerateKey(rng)
		if err != nil {
			return nil, fmt.Errorf("webpush: generate ephemeral key: %w", err)
		}
	}
	asPub := asPriv.PublicKey().Bytes()
	if len(asPub) != p256UncompressedLen {
		return nil, fmt.Errorf("webpush: ephemeral public key length %d (want %d)",
			len(asPub), p256UncompressedLen)
	}

	salt := opts.Salt
	if salt == nil {
		salt = make([]byte, aes128GCMSaltLen)
		if _, err := io.ReadFull(rng, salt); err != nil {
			return nil, fmt.Errorf("webpush: salt: %w", err)
		}
	} else if len(salt) != aes128GCMSaltLen {
		return nil, fmt.Errorf("webpush: salt must be %d bytes (got %d)",
			aes128GCMSaltLen, len(salt))
	}

	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: ecdh: %w", err)
	}

	cek, nonce, err := deriveKeyMaterial(shared, recipientAuth, salt, recipientPub, asPub)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher.NewGCM: %w", err)
	}

	// RFC 8188 §2.1 / RFC 8291 §2: the plaintext for the (only)
	// record is `payload || 0x02` — 0x02 is the last-record padding
	// delimiter. Single-record envelopes never need the trailing 0x01
	// "more records follow" form.
	plaintext := make([]byte, 0, len(payload)+1)
	plaintext = append(plaintext, payload...)
	plaintext = append(plaintext, 0x02)

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	envelope := make([]byte, 0, aes128GCMSaltLen+4+1+p256UncompressedLen+len(ciphertext))
	envelope = append(envelope, salt...)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, aes128GCMRecordSize)
	envelope = append(envelope, rs...)
	envelope = append(envelope, byte(p256UncompressedLen))
	envelope = append(envelope, asPub...)
	envelope = append(envelope, ciphertext...)
	return envelope, nil
}

// deriveKeyMaterial implements RFC 8291 §3.3 (IKM) and §3.4 (CEK +
// nonce) of the aes128gcm encoding. Splitting it out keeps the
// ECDH-side mechanics testable in isolation against the published
// vector.
func deriveKeyMaterial(sharedSecret, authSecret, salt, uaPub, asPub []byte) (cek, nonce []byte, err error) {
	// IKM = HKDF-Extract+Expand(salt=auth_secret, IKM=ECDH_shared,
	//                          info="WebPush: info\x00" || ua_public || as_public,
	//                          L=32) per RFC 8291 §3.3.
	info := make([]byte, 0, len(hkdfAuthInfoPrefix)+len(uaPub)+len(asPub))
	info = append(info, hkdfAuthInfoPrefix...)
	info = append(info, uaPub...)
	info = append(info, asPub...)

	ikm, err := hkdf.Key(sha256.New, sharedSecret, authSecret, string(info), 32)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: hkdf ikm: %w", err)
	}

	// CEK = HKDF-Extract+Expand(salt=salt, IKM=ikm,
	//                           info="Content-Encoding: aes128gcm\x00", L=16).
	cek, err = hkdf.Key(sha256.New, ikm, salt, string(hkdfKeyInfo), aes128GCMKeyLen)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: hkdf cek: %w", err)
	}

	// Nonce = HKDF-Extract+Expand(salt=salt, IKM=ikm,
	//                             info="Content-Encoding: nonce\x00", L=12).
	nonce, err = hkdf.Key(sha256.New, ikm, salt, string(hkdfNonceInfo), aes128GCMNonceLen)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: hkdf nonce: %w", err)
	}
	return cek, nonce, nil
}

// decryptForTest is the inverse of Encrypt used only by unit tests
// (encrypt_test.go and the dispatcher test's fake gateway). It
// recovers the original payload from envelope using the recipient's
// P-256 private key and auth secret. NOT used in production: herold
// only encrypts; gateways and clients decrypt.
func decryptForTest(envelope []byte, recipientPriv *ecdh.PrivateKey, recipientAuth []byte) ([]byte, error) {
	if len(envelope) < aes128GCMSaltLen+4+1+p256UncompressedLen {
		return nil, errors.New("webpush: envelope too short")
	}
	salt := envelope[:aes128GCMSaltLen]
	idlen := envelope[aes128GCMSaltLen+4]
	if idlen != p256UncompressedLen {
		return nil, fmt.Errorf("webpush: idlen %d (want %d)", idlen, p256UncompressedLen)
	}
	asPub := envelope[aes128GCMSaltLen+5 : aes128GCMSaltLen+5+p256UncompressedLen]
	ciphertext := envelope[aes128GCMSaltLen+5+p256UncompressedLen:]

	curve := ecdh.P256()
	asPubKey, err := curve.NewPublicKey(asPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: parse as_public: %w", err)
	}
	shared, err := recipientPriv.ECDH(asPubKey)
	if err != nil {
		return nil, fmt.Errorf("webpush: ecdh: %w", err)
	}
	uaPub := recipientPriv.PublicKey().Bytes()
	cek, nonce, err := deriveKeyMaterial(shared, recipientAuth, salt, uaPub, asPub)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("webpush: gcm.Open: %w", err)
	}
	if len(plain) == 0 {
		return nil, errors.New("webpush: empty plaintext")
	}
	// Strip the RFC 8188 single-record terminator 0x02.
	if plain[len(plain)-1] != 0x02 {
		return nil, fmt.Errorf("webpush: trailing byte %02x (want 0x02)", plain[len(plain)-1])
	}
	return plain[:len(plain)-1], nil
}
