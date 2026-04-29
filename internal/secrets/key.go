package secrets

import (
	"encoding/hex"
	"fmt"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// minKeyBytes is the minimum acceptable decoded key length. ChaCha20-Poly1305
// requires exactly 32 bytes; we enforce the same floor so a future algorithm
// swap to a 256-bit requirement works without changing this constant.
const minKeyBytes = 32

// SecretsConfig mirrors the [server.secrets] sysconfig block. It is declared
// in sysconfig; LoadDataKey accepts it as a value so callers do not need to
// reach into sysconfig internals.
//
// LoadDataKey is the only consumer. The config type is defined in sysconfig
// so it participates in TOML parsing and Validate; this package only reads it.

// LoadDataKey resolves the data_key_ref secret reference from cfg, hex-decodes
// it, and validates that the result is at least 32 bytes.
//
// The ref must be a $VAR or file:/path reference (sysconfig.ResolveSecretStrict;
// inline values are refused per STANDARDS §9). The resolved value must be
// 64 or more hex characters decoding to >= 32 bytes.
//
// Generate a suitable key with: openssl rand -hex 32
func LoadDataKey(cfg sysconfig.SecretsConfig) ([]byte, error) {
	ref := cfg.DataKeyRef
	if ref == "" {
		return nil, fmt.Errorf("secrets: [server.secrets].data_key_ref is not configured")
	}
	hexStr, err := sysconfig.ResolveSecretStrict(ref)
	if err != nil {
		return nil, fmt.Errorf("secrets: resolve data_key_ref: %w", err)
	}
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("secrets: data key is not valid hex: %w", err)
	}
	if len(key) < minKeyBytes {
		return nil, fmt.Errorf("secrets: data key is %d bytes (decoded from hex), minimum is %d bytes; generate a longer key", len(key), minKeyBytes)
	}
	return key, nil
}
