package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestResolveSessionSigningKey_PersistsOnFirstStart verifies the issue #14
// fix: on a fresh data_dir, the function generates a fresh 32-byte key,
// writes it to <data_dir>/secrets/ui-session-key with mode 0600, and
// returns the same bytes on a subsequent call (the persistence "round
// trip"). Without this, every restart minted a new ephemeral key and
// invalidated every browser session.
func TestResolveSessionSigningKey_PersistsOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	// Suppress env-var paths in case the test runner has them set.
	t.Setenv(defaultSessionKeyEnv, "")
	cfg := &sysconfig.Config{}
	cfg.Server.DataDir = dir

	first := resolveSessionSigningKey(cfg, nil)
	if len(first) != 32 {
		t.Fatalf("first key wrong length: got %d, want 32", len(first))
	}

	path := filepath.Join(dir, "secrets", sessionKeyFilename)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected persisted key at %s: %v", path, err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("persisted key mode: got %o, want 0600", mode)
	}
	on_disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted key: %v", err)
	}
	if !bytes.Equal(on_disk, first) {
		t.Fatalf("persisted bytes don't match returned key")
	}

	// A "restart": a second call with the same data_dir reads the same
	// key back. This is the property the issue #14 fix is about.
	second := resolveSessionSigningKey(cfg, nil)
	if !bytes.Equal(first, second) {
		t.Fatalf("second call returned a different key; persistence broken (this is the issue #14 regression)")
	}
}

// TestResolveSessionSigningKey_EnvOverridesPersisted verifies the env-var
// path still wins when an operator wants to externalize the key
// (k8s secret injection, external vault, ...). The persisted file is
// left alone -- we don't touch the data dir at all when env wins.
func TestResolveSessionSigningKey_EnvOverridesPersisted(t *testing.T) {
	dir := t.TempDir()
	envKey := "operator-supplied-key-of-exactly-32B!" // 36 bytes, >= 32
	t.Setenv(defaultSessionKeyEnv, envKey)
	cfg := &sysconfig.Config{}
	cfg.Server.DataDir = dir

	got := resolveSessionSigningKey(cfg, nil)
	if string(got) != envKey {
		t.Fatalf("env-var path lost: got %q, want %q", string(got), envKey)
	}
	// We must not have created the persisted file when env wins.
	if _, err := os.Stat(filepath.Join(dir, "secrets", sessionKeyFilename)); !os.IsNotExist(err) {
		t.Fatalf("env-supplied key triggered a persistence write; want no file in data_dir")
	}
}

// TestResolveSessionSigningKey_RegeneratesOnShortFile verifies the
// recovery path: a persisted file that's been truncated below 32 bytes
// (operator accident, partial fs error) is rewritten with a fresh key.
func TestResolveSessionSigningKey_RegeneratesOnShortFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(defaultSessionKeyEnv, "")
	cfg := &sysconfig.Config{}
	cfg.Server.DataDir = dir

	// Stage a too-short pre-existing file.
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	short := []byte("too short")
	if err := os.WriteFile(filepath.Join(secretsDir, sessionKeyFilename), short, 0o600); err != nil {
		t.Fatalf("write short file: %v", err)
	}

	got := resolveSessionSigningKey(cfg, nil)
	if len(got) != 32 {
		t.Fatalf("regenerated key length: got %d, want 32", len(got))
	}
	if bytes.Equal(got, short) {
		t.Fatalf("regeneration kept the short bytes")
	}
	onDisk, err := os.ReadFile(filepath.Join(secretsDir, sessionKeyFilename))
	if err != nil {
		t.Fatalf("read regenerated file: %v", err)
	}
	if !bytes.Equal(onDisk, got) {
		t.Fatalf("regenerated key not written back to disk")
	}
}

// TestResolveSessionSigningKey_EphemeralWhenDataDirUnusable verifies the
// last-resort fallback: when data_dir is empty (misconfiguration) the
// function still returns a usable key rather than panicking, so the
// server can still come up. The operator sees a WARN log; sessions are
// per-process again, as before the fix.
func TestResolveSessionSigningKey_EphemeralWhenDataDirUnusable(t *testing.T) {
	t.Setenv(defaultSessionKeyEnv, "")
	cfg := &sysconfig.Config{}
	cfg.Server.DataDir = "" // empty -> persistence path errors out

	got := resolveSessionSigningKey(cfg, nil)
	if len(got) != 32 {
		t.Fatalf("ephemeral key length: got %d, want 32", len(got))
	}
}
