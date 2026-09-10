package protoadmin_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// newSpamStatusTestServer builds a bootstrapped admin server whose
// GET /api/v1/spam/status answer is driven by provider. A nil provider
// exercises the "not wired" fallback.
func newSpamStatusTestServer(t *testing.T, provider protoadmin.SpamStatusProvider) (*httptest.Server, string) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
		SpamStatus:              provider,
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "POST", "/api/v1/bootstrap", "",
		map[string]any{"email": "admin@test.local"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", res.StatusCode, body)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(body, &boot); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	return hs, boot.InitialAPIKey
}

func spamStatusHTTP(t *testing.T, c *http.Client, base, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, buf
}

// TestSpamStatus_NotConfigured covers the "no spam plugin configured"
// state: enabled=false, plugin="", and a reason naming what's missing.
func TestSpamStatus_NotConfigured(t *testing.T) {
	provider := protoadmin.SpamStatusProvider(func() protoadmin.SpamStatus {
		return protoadmin.SpamStatus{
			Enabled: false,
			Plugin:  "",
			Reason:  "no spam plugin configured in system.toml",
		}
	})
	hs, key := newSpamStatusTestServer(t, provider)

	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "GET", "/api/v1/spam/status", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET spam/status: %d: %s", res.StatusCode, body)
	}
	var out protoadmin.SpamStatus
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if out.Enabled {
		t.Fatalf("expected enabled=false, got true")
	}
	if out.Plugin != "" {
		t.Fatalf("expected plugin=\"\", got %q", out.Plugin)
	}
	if out.Reason != "no spam plugin configured in system.toml" {
		t.Fatalf("unexpected reason: %q", out.Reason)
	}
}

// TestSpamStatus_Enabled covers the healthy-plugin state: enabled=true
// with the plugin name and no reason.
func TestSpamStatus_Enabled(t *testing.T) {
	provider := protoadmin.SpamStatusProvider(func() protoadmin.SpamStatus {
		return protoadmin.SpamStatus{
			Enabled: true,
			Plugin:  "herold-spam-llm",
		}
	})
	hs, key := newSpamStatusTestServer(t, provider)

	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "GET", "/api/v1/spam/status", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET spam/status: %d: %s", res.StatusCode, body)
	}
	var out protoadmin.SpamStatus
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if !out.Enabled {
		t.Fatalf("expected enabled=true, got false")
	}
	if out.Plugin != "herold-spam-llm" {
		t.Fatalf("unexpected plugin: %q", out.Plugin)
	}
	if out.Reason != "" {
		t.Fatalf("expected no reason on the enabled path, got %q", out.Reason)
	}
}

// TestSpamStatus_LoadFailed covers the "configured but not loaded" state:
// enabled=false with the plugin name and the supervisor's reason.
func TestSpamStatus_LoadFailed(t *testing.T) {
	provider := protoadmin.SpamStatusProvider(func() protoadmin.SpamStatus {
		return protoadmin.SpamStatus{
			Enabled: false,
			Plugin:  "herold-spam-llm",
			Reason:  "plugin: invalid manifest: spam plugin must declare temperature=0 in its manifest",
		}
	})
	hs, key := newSpamStatusTestServer(t, provider)

	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "GET", "/api/v1/spam/status", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET spam/status: %d: %s", res.StatusCode, body)
	}
	var out protoadmin.SpamStatus
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if out.Enabled {
		t.Fatalf("expected enabled=false, got true")
	}
	if out.Plugin != "herold-spam-llm" {
		t.Fatalf("unexpected plugin: %q", out.Plugin)
	}
	if out.Reason == "" {
		t.Fatalf("expected a non-empty reason")
	}
}

// TestSpamStatus_NoProviderWired covers the fallback when the server was
// not given a SpamStatusProvider at all.
func TestSpamStatus_NoProviderWired(t *testing.T) {
	hs, key := newSpamStatusTestServer(t, nil)

	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "GET", "/api/v1/spam/status", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET spam/status: %d: %s", res.StatusCode, body)
	}
	var out protoadmin.SpamStatus
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if out.Enabled {
		t.Fatalf("expected enabled=false, got true")
	}
	if out.Reason == "" {
		t.Fatalf("expected a non-empty reason")
	}
}

// TestSpamStatus_RequiresAdmin covers the auth gate: an unauthenticated
// caller must not see spam status.
func TestSpamStatus_RequiresAdmin(t *testing.T) {
	hs, _ := newSpamStatusTestServer(t, nil)
	res, body := spamStatusHTTP(t, hs.Client(), hs.URL, "GET", "/api/v1/spam/status", "", nil)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("expected non-200 without auth, got 200: %s", body)
	}
}
