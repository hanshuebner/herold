package admin

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadOrGenerateWebhookSigningKey_GeneratesOnFirstBoot verifies that
// a fresh key is written to disk when the key file does not exist.
func TestLoadOrGenerateWebhookSigningKey_GeneratesOnFirstBoot(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secrets", "webhook", "sign.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	key1, err := loadOrGenerateWebhookSigningKey(keyPath, logger)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("key length: got %d, want 32", len(key1))
	}

	// Key file must exist on disk after generation.
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if !bytes.Equal(raw, key1) {
		t.Errorf("persisted key differs from returned key")
	}

	// File permissions must be 0600.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode: got %04o, want 0600", perm)
	}
}

// TestLoadOrGenerateWebhookSigningKey_LoadsExistingKey verifies idempotence:
// booting twice returns the same key bytes.
func TestLoadOrGenerateWebhookSigningKey_LoadsExistingKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secrets", "webhook", "sign.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	key1, err := loadOrGenerateWebhookSigningKey(keyPath, logger)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}

	key2, err := loadOrGenerateWebhookSigningKey(keyPath, logger)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}

	if !bytes.Equal(key1, key2) {
		t.Errorf("key changed between loads; want idempotent")
	}
}

// TestLoadOrGenerateWebhookSigningKey_RejectsCorruptFile verifies that a
// key file with the wrong number of bytes returns an error rather than
// silently using a truncated key.
func TestLoadOrGenerateWebhookSigningKey_RejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "sign.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Write a corrupt (too-short) key file.
	if err := os.WriteFile(keyPath, []byte("tooshort"), 0o600); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}

	_, err := loadOrGenerateWebhookSigningKey(keyPath, logger)
	if err == nil {
		t.Fatal("expected error for corrupt key file, got nil")
	}
}

// TestFetchHandler_MountedOnPublicListener verifies that the
// /webhook-fetch/ path is reachable on the public listener and returns
// 400 (bad request — delivery ID required) rather than 404.
func TestFetchHandler_MountedOnPublicListener(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	// GET /webhook-fetch/ with no delivery ID. The fetch server returns 400.
	resp, err := http.Get("http://" + publicAddr + "/webhook-fetch/")
	if err != nil {
		t.Fatalf("GET /webhook-fetch/ on public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("/webhook-fetch/ on public returned 404; handler not mounted\nbody: %s", body)
	}
}
