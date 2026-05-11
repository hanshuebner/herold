package admin

// jmap_tagged_addresses_capability_test.go is a boot-smoke regression test
// for the tagged-addresses capability emission gate added in
// internal/admin/server.go.
//
// composeAdminAndUI advertises CapabilityTaggedAddresses
// ("https://netzhansa.com/jmap/tagged-addresses") when
// [server.tagged_addresses].enabled is true (the default). This test boots
// a full server against the minimalConfigFixture (which omits the section
// entirely, exercising the absent-section default-true path) and verifies
// that the JMAP session descriptor surfaces the capability key. A future
// regression that drops the registration call from composeAdminAndUI would
// produce a silent "TaggedAddressFilter unavailable" failure in the SPA;
// this test catches it at the Go-test layer instead.
//
// The companion negative case lives below: when the feature is explicitly
// disabled in the sysconfig, the capability MUST NOT appear in the session
// descriptor.
//
// REQ-TAG-70.

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

// TestTaggedAddressesCapabilityInSessionDescriptor verifies that the JMAP
// session descriptor returned by GET /.well-known/jmap contains
// "https://netzhansa.com/jmap/tagged-addresses" in the top-level
// capabilities map when [server.tagged_addresses].enabled is true (the
// default when the section is omitted, per
// docs/design/server/requirements/24-tagged-addresses.md).
func TestTaggedAddressesCapabilityInSessionDescriptor(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
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
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap a principal so we can authenticate against the session endpoint.
	b, _ := json.Marshal(map[string]any{
		"email":        "tagged-cap-test@example.com",
		"display_name": "Tagged Cap Test",
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
	const capTagged = "https://netzhansa.com/jmap/tagged-addresses"
	if _, ok := caps[capTagged]; !ok {
		keys := make([]string, 0, len(caps))
		for k := range caps {
			keys = append(keys, k)
		}
		t.Errorf("session capabilities missing %q; capabilities keys: %v", capTagged, keys)
	}
}

// TestTaggedAddressesCapabilityAbsentWhenDisabled verifies that flipping
// [server.tagged_addresses].enabled to false suppresses the capability
// advertisement at boot. The server is brought up with a hand-crafted cfg
// rather than via startTestServer because the helper bakes the
// always-enabled minimal fixture and there is no per-test override hook.
func TestTaggedAddressesCapabilityAbsentWhenDisabled(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	// Flip the gate. applyDefaults has already populated Enabled=&true and
	// the documented caps, so we just need to overwrite Enabled.
	f := false
	cfg.Server.TaggedAddresses.Enabled = &f

	ctx, cancelFn := context.WithCancel(context.Background())
	addrs := map[string]string{}
	addrsMu := &sync.Mutex{}
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
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

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap a principal so we can authenticate against the session endpoint.
	b, _ := json.Marshal(map[string]any{
		"email":        "tagged-cap-disabled@example.com",
		"display_name": "Tagged Cap Disabled",
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
	const capTagged = "https://netzhansa.com/jmap/tagged-addresses"
	if _, ok := caps[capTagged]; ok {
		t.Errorf("session capabilities should NOT contain %q when [server.tagged_addresses].enabled=false", capTagged)
	}
}

// fetchSessionCapabilities issues GET /.well-known/jmap against publicAddr
// with the supplied bearer token and returns the top-level "capabilities"
// map keys parsed from the response body. The helper is shared between the
// enabled/disabled variants above.
func fetchSessionCapabilities(t *testing.T, publicAddr, apiKey string) map[string]json.RawMessage {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	sessResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer sessResp.Body.Close()
	sessRaw, _ := io.ReadAll(sessResp.Body)
	if sessResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.well-known/jmap: status=%d body=%s", sessResp.StatusCode, sessRaw)
	}
	var sess struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(sessRaw, &sess); err != nil {
		t.Fatalf("session descriptor unmarshal: %v body=%s", err, sessRaw)
	}
	return sess.Capabilities
}
