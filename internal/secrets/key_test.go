package secrets

import (
	"encoding/hex"
	"testing"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

func TestLoadDataKey_HappyPath(t *testing.T) {
	// 32 bytes hex-encoded = 64 hex chars.
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	hexKey := hex.EncodeToString(raw)

	t.Setenv("HEROLD_TEST_DATA_KEY", hexKey)
	cfg := sysconfig.SecretsConfig{DataKeyRef: "$HEROLD_TEST_DATA_KEY"}
	key, err := LoadDataKey(cfg)
	if err != nil {
		t.Fatalf("LoadDataKey: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("LoadDataKey: got %d bytes, want 32", len(key))
	}
	for i, b := range key {
		if b != byte(i) {
			t.Fatalf("key[%d] = %d, want %d", i, b, byte(i))
		}
	}
}

func TestLoadDataKey_MissingRef(t *testing.T) {
	cfg := sysconfig.SecretsConfig{DataKeyRef: ""}
	_, err := LoadDataKey(cfg)
	if err == nil {
		t.Fatal("LoadDataKey: expected error for empty ref, got nil")
	}
}

func TestLoadDataKey_EnvNotSet(t *testing.T) {
	cfg := sysconfig.SecretsConfig{DataKeyRef: "$HEROLD_TEST_MISSING_DATA_KEY_XYZZY"}
	_, err := LoadDataKey(cfg)
	if err == nil {
		t.Fatal("LoadDataKey: expected error for unset env var, got nil")
	}
}

func TestLoadDataKey_ShortKey(t *testing.T) {
	// 15 bytes = 30 hex chars, below the 32-byte minimum.
	raw := make([]byte, 15)
	hexKey := hex.EncodeToString(raw)
	t.Setenv("HEROLD_TEST_SHORT_KEY", hexKey)
	cfg := sysconfig.SecretsConfig{DataKeyRef: "$HEROLD_TEST_SHORT_KEY"}
	_, err := LoadDataKey(cfg)
	if err == nil {
		t.Fatal("LoadDataKey: expected error for short key, got nil")
	}
}

func TestLoadDataKey_NonHexContent(t *testing.T) {
	t.Setenv("HEROLD_TEST_NONHEX_KEY", "this-is-not-hex-content!!!")
	cfg := sysconfig.SecretsConfig{DataKeyRef: "$HEROLD_TEST_NONHEX_KEY"}
	_, err := LoadDataKey(cfg)
	if err == nil {
		t.Fatal("LoadDataKey: expected error for non-hex content, got nil")
	}
}

func TestLoadDataKey_InlineRefused(t *testing.T) {
	raw := make([]byte, 32)
	hexKey := hex.EncodeToString(raw)
	// Pass the hex string directly (not as $VAR or file:/) — should be refused.
	cfg := sysconfig.SecretsConfig{DataKeyRef: hexKey}
	_, err := LoadDataKey(cfg)
	if err == nil {
		t.Fatal("LoadDataKey: expected error for inline secret, got nil")
	}
}
