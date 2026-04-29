// Package secrets provides authenticated encryption primitives and data-key
// management for herold's at-rest credential protection.
//
// # Threat model
//
// herold stores externally-supplied SMTP submission credentials (passwords,
// OAuth tokens) in the metadata store. The secrets package encrypts those
// values before storage so that a database dump (e.g. a leaked SQLite file or
// a Postgres backup) does not expose plaintext credentials.
//
// The threat model is: an attacker who reads the raw database file but does
// NOT have access to the server's runtime environment (environment variables
// or key files) cannot recover plaintext credentials. An attacker with full
// operating-system access to the server already has access to the running
// process memory and the key material; encryption does not help in that case.
//
// This package does NOT implement volume-level encryption, forward secrecy,
// or key rotation in the general case. Operators who need stronger guarantees
// should use filesystem-level encryption (LUKS, FileVault, etc.) in addition.
//
// # AEAD construction
//
// Ciphertext produced by Seal uses ChaCha20-Poly1305 (golang.org/x/crypto/
// chacha20poly1305). The wire format is a version tag followed by a random
// 12-byte nonce and the AEAD output:
//
//	"v1:" | nonce(12 bytes) | ciphertext+MAC
//
// The version tag makes future algorithm changes detectable by Open without
// requiring a separate version column in the database.
//
// # Key encoding
//
// The 32-byte data key is stored hex-encoded (64 ASCII hex characters) in
// the environment variable or file referenced by [server.secrets].data_key_ref.
// Hex encoding avoids shell-escaping hazards. Generate a suitable key with:
//
//	openssl rand -hex 32
//
// Or using Go:
//
//	head -c32 /dev/urandom | xxd -p | tr -d '\n'
//
// The key reference uses sysconfig.ResolveSecretStrict, so inline values in
// system.toml are refused (STANDARDS §9). Use "$HEROLD_DATA_KEY" (env) or
// "file:/run/secrets/herold-data-key" (file).
package secrets
