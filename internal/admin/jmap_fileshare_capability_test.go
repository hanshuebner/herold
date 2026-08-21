package admin

// jmap_fileshare_capability_test.go is a boot-smoke regression test for the
// attachment-shares (FileShare) capability wiring added in
// internal/admin/server.go.
//
// composeAdminAndUI advertises CapabilityFileShares
// ("https://netzhansa.com/jmap/file-shares") only when
// [server.attachment_shares].enabled is true AND public_base_url is set.
// When both conditions are met the JMAP session descriptor must carry the
// capability key; when either is absent it must not appear.
//
// A companion test verifies that the public /share/{id} download routes are
// mounted (200 or 410, not 404) when the feature is active.
//
// REQ-SHARE-30, REQ-SHARE-40.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// bootServerWithShares starts the full server with attachment_shares active
// (enabled=true, public_base_url set) and returns the listener addresses, a
// teardown channel, and a cancel function.
func bootServerWithShares(t *testing.T) (addrs map[string]string, doneCh <-chan struct{}, cancel func()) {
	t.Helper()
	_, cfg := minimalConfigFixture(t)

	tr := true
	cfg.Server.AttachmentShares.Enabled = &tr
	cfg.Server.AttachmentShares.PublicBaseURL = "https://test.local"

	ctx, cancelFn := context.WithCancel(context.Background())
	addrMap := map[string]string{}
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	return addrMap, done, cancelFn
}

// TestFileShareCapabilityInSessionDescriptor verifies that the JMAP session
// descriptor contains "https://netzhansa.com/jmap/file-shares" when
// attachment_shares is active (enabled=true + public_base_url set).
func TestFileShareCapabilityInSessionDescriptor(t *testing.T) {
	addrs, done, cancel := bootServerWithShares(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap a principal for the API key.
	b, _ := json.Marshal(map[string]any{
		"email":        "fileshare-cap-test@example.com",
		"display_name": "FileShare Cap Test",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	if boot.InitialAPIKey == "" {
		t.Fatalf("bootstrap returned empty initial_api_key; body=%s", raw)
	}

	caps := fetchSessionCapabilities(t, publicAddr, boot.InitialAPIKey)
	const capFileShares = "https://netzhansa.com/jmap/file-shares"
	if _, ok := caps[capFileShares]; !ok {
		keys := make([]string, 0, len(caps))
		for k := range caps {
			keys = append(keys, k)
		}
		t.Errorf("session capabilities missing %q; capability keys: %v", capFileShares, keys)
	}
}

// TestFileShareCapabilityAbsentWhenDisabled verifies that the capability does
// NOT appear when attachment_shares.enabled is false, even if public_base_url
// is set.
func TestFileShareCapabilityAbsentWhenDisabled(t *testing.T) {
	_, cfg := minimalConfigFixture(t)

	f := false
	cfg.Server.AttachmentShares.Enabled = &f
	cfg.Server.AttachmentShares.PublicBaseURL = "https://test.local"

	ctx, cancelFn := context.WithCancel(context.Background())
	addrMap := map[string]string{}
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	t.Cleanup(func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrMap["public"]
	adminAddr := addrMap["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrMap)
	}

	b, _ := json.Marshal(map[string]any{
		"email":        "fileshare-disabled@example.com",
		"display_name": "FileShare Disabled",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}

	caps := fetchSessionCapabilities(t, publicAddr, boot.InitialAPIKey)
	const capFileShares = "https://netzhansa.com/jmap/file-shares"
	if _, ok := caps[capFileShares]; ok {
		t.Errorf("session capabilities should NOT contain %q when attachment_shares.enabled=false", capFileShares)
	}
}

// TestFileShareCapabilityAbsentWithoutPublicBaseURL verifies that the
// capability does NOT appear when enabled=true but public_base_url is empty,
// because AttachmentSharesActive() requires both conditions.
func TestFileShareCapabilityAbsentWithoutPublicBaseURL(t *testing.T) {
	_, cfg := minimalConfigFixture(t)

	// enabled defaults to true; ensure public_base_url is not set (default).
	cfg.Server.AttachmentShares.PublicBaseURL = ""

	ctx, cancelFn := context.WithCancel(context.Background())
	addrMap := map[string]string{}
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrMap,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancelFn()
		t.Fatalf("server did not become ready within timeout")
	}
	t.Cleanup(func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrMap["public"]
	adminAddr := addrMap["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrMap)
	}

	b, _ := json.Marshal(map[string]any{
		"email":        "fileshare-nourl@example.com",
		"display_name": "FileShare NoURL",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}

	caps := fetchSessionCapabilities(t, publicAddr, boot.InitialAPIKey)
	const capFileShares = "https://netzhansa.com/jmap/file-shares"
	if _, ok := caps[capFileShares]; ok {
		t.Errorf("session capabilities should NOT contain %q when public_base_url is empty", capFileShares)
	}
}

// TestFileShareCapabilityAdvertisesConfiguredTTLs verifies, through the
// fully composed server (StartServer -> the real public listener -> the
// session endpoint), that the FileShares capability descriptor reports the
// deployment's configured default_ttl_seconds and max_ttl_seconds rather
// than an empty object. minimalConfigFixture leaves [server.attachment_shares]
// at its documented defaults (default_ttl=48h, max_ttl=90d); a handler-level
// unit test cannot catch a wiring gap between sysconfig and the registered
// capability descriptor, so this asserts against the wired server (re #290).
func TestFileShareCapabilityAdvertisesConfiguredTTLs(t *testing.T) {
	addrs, done, cancel := bootServerWithShares(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	b, _ := json.Marshal(map[string]any{
		"email":        "fileshare-ttl-cap-test@example.com",
		"display_name": "FileShare TTL Cap Test",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}

	caps := fetchSessionCapabilities(t, publicAddr, boot.InitialAPIKey)
	const capFileShares = "https://netzhansa.com/jmap/file-shares"
	descRaw, ok := caps[capFileShares]
	if !ok {
		t.Fatalf("session capabilities missing %q", capFileShares)
	}
	var desc struct {
		DefaultTTLSeconds int64 `json:"default_ttl_seconds"`
		MaxTTLSeconds     int64 `json:"max_ttl_seconds"`
	}
	if err := json.Unmarshal(descRaw, &desc); err != nil {
		t.Fatalf("descriptor unmarshal: %v raw=%s", err, descRaw)
	}
	const wantDefaultTTL = int64(48 * 3600)  // 48h
	const wantMaxTTL = int64(90 * 24 * 3600) // 90d
	if desc.DefaultTTLSeconds != wantDefaultTTL {
		t.Errorf("default_ttl_seconds = %d, want %d (raw=%s)", desc.DefaultTTLSeconds, wantDefaultTTL, descRaw)
	}
	if desc.MaxTTLSeconds != wantMaxTTL {
		t.Errorf("max_ttl_seconds = %d, want %d (raw=%s)", desc.MaxTTLSeconds, wantMaxTTL, descRaw)
	}
}

// TestFileShareRoutesMount verifies that the /share/{id} routes are mounted
// on the public listener when attachment_shares is active. An unknown share
// ID must return 410 Gone (not 404 Not Found), confirming the route was
// registered and the handler is reachable.
func TestFileShareRoutesMount(t *testing.T) {
	addrs, done, cancel := bootServerWithShares(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	// GET /share/<nonexistent-id> — the protoshare handler returns 410 for any
	// id that does not exist in the store. 404 would mean the route is not
	// mounted at all.
	resp, err := http.Get("http://" + publicAddr + "/share/NOSUCHSHARE00000000")
	if err != nil {
		t.Fatalf("GET /share/NOSUCHSHARE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("GET /share/<unknown>: got status %d, want 410 Gone (route must be mounted)", resp.StatusCode)
	}
}
