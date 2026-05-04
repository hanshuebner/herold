package protoadmin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// dkimTestHarness is a self-contained test harness for DKIM REST handler
// tests. It owns an httptest.Server backed by an in-memory SQLite store so
// the DKIMKeyManager can be injected without fighting the testharness
// AttachAdmin "already attached" guard.
type dkimTestHarness struct {
	t        *testing.T
	fs       store.Store
	hs       *httptest.Server
	client   *http.Client
	adminKey string
	stub     *stubDKIMManager
}

// stubDKIMManager is a minimal DKIMKeyManager for handler tests.
// GenerateKey upserts a key with selector "testsel-<domain>" directly into
// the store so the handler's subsequent GetActiveDKIMKey reads work.
type stubDKIMManager struct {
	meta store.Metadata
}

func (m *stubDKIMManager) GenerateKey(ctx context.Context, domain string, alg store.DKIMAlgorithm) (string, error) {
	selector := "testsel-" + domain
	return selector, m.meta.UpsertDKIMKey(ctx, store.DKIMKey{
		Domain:        domain,
		Selector:      selector,
		Algorithm:     alg,
		PrivateKeyPEM: "FAKEPEM",
		PublicKeyB64:  "FAKEPUB",
		Status:        store.DKIMKeyStatusActive,
	})
}

func (m *stubDKIMManager) PublishedRecord(_ context.Context, key store.DKIMKey) (string, error) {
	return "v=DKIM1; k=ed25519; p=" + key.PublicKeyB64, nil
}

func newDKIMTestHarness(t *testing.T, withManager bool) *dkimTestHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
	}
	var stub *stubDKIMManager
	if withManager {
		stub = &stubDKIMManager{meta: fs.Meta()}
		opts.DKIMKeyManager = stub
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Bootstrap the first admin principal.
	res, body := dkimHTTP(t, hs.Client(), hs.URL, "POST", "/api/v1/bootstrap", "",
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
	return &dkimTestHarness{
		t: t, fs: fs, hs: hs,
		client:   hs.Client(),
		adminKey: boot.InitialAPIKey,
		stub:     stub,
	}
}

// dkimHTTP issues an HTTP request and returns the response + body.
func dkimHTTP(t *testing.T, c *http.Client, base, method, path, key string, body any) (*http.Response, []byte) {
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

func (h *dkimTestHarness) do(method, path string, body any) (*http.Response, []byte) {
	return dkimHTTP(h.t, h.client, h.hs.URL, method, path, h.adminKey, body)
}

func (h *dkimTestHarness) doAs(key, method, path string, body any) (*http.Response, []byte) {
	return dkimHTTP(h.t, h.client, h.hs.URL, method, path, key, body)
}

func (h *dkimTestHarness) createDomain(name string) {
	h.t.Helper()
	res, buf := h.do("POST", "/api/v1/domains", map[string]any{"name": name})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("create domain %q: %d: %s", name, res.StatusCode, buf)
	}
}

func TestDKIM_GenerateAndList(t *testing.T) {
	h := newDKIMTestHarness(t, true)
	h.createDomain("dkim.example.com")

	// POST: generate first key.
	res, buf := h.do("POST", "/api/v1/domains/dkim.example.com/dkim", nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("generate dkim: %d: %s", res.StatusCode, buf)
	}
	var genOut struct {
		Selector  string `json:"selector"`
		Algorithm string `json:"algorithm"`
		IsActive  bool   `json:"is_active"`
		TXTRecord string `json:"txt_record"`
	}
	if err := json.Unmarshal(buf, &genOut); err != nil {
		t.Fatalf("decode generate: %v: %s", err, buf)
	}
	if genOut.Selector == "" {
		t.Fatalf("selector empty")
	}
	if !genOut.IsActive {
		t.Fatalf("expected is_active=true")
	}
	if !strings.HasPrefix(genOut.TXTRecord, "v=DKIM1") {
		t.Fatalf("expected TXT to start with v=DKIM1: %s", genOut.TXTRecord)
	}

	// GET: list — verify the generated selector appears.
	res, buf = h.do("GET", "/api/v1/domains/dkim.example.com/dkim", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list dkim: %d: %s", res.StatusCode, buf)
	}
	var listOut struct {
		Items []struct {
			Selector  string `json:"selector"`
			IsActive  bool   `json:"is_active"`
			TXTRecord string `json:"txt_record"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &listOut); err != nil {
		t.Fatalf("decode list: %v: %s", err, buf)
	}
	if len(listOut.Items) == 0 {
		t.Fatalf("expected at least one item in list")
	}
	found := false
	for _, item := range listOut.Items {
		if item.Selector == genOut.Selector {
			found = true
			if !item.IsActive {
				t.Fatalf("listed key not active")
			}
			if !strings.HasPrefix(item.TXTRecord, "v=DKIM1") {
				t.Fatalf("listed key missing TXT: %s", item.TXTRecord)
			}
		}
	}
	if !found {
		t.Fatalf("generated selector %q not found in list", genOut.Selector)
	}
}

// TestDKIM_Generate_SetsActiveKey seeds a retiring key and verifies that
// after POST /dkim exactly one active key is present in the list.
func TestDKIM_Generate_SetsActiveKey(t *testing.T) {
	h := newDKIMTestHarness(t, true)
	h.createDomain("rot.example.com")

	// Seed a retiring key directly via the stub's meta to simulate a pre-
	// existing rotation state.
	ctx := context.Background()
	_ = h.stub.meta.UpsertDKIMKey(ctx, store.DKIMKey{
		Domain:        "rot.example.com",
		Selector:      "old-sel",
		Algorithm:     store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "OLDPEM",
		PublicKeyB64:  "OLDPUB",
		Status:        store.DKIMKeyStatusRetiring,
	})

	res, buf := h.do("POST", "/api/v1/domains/rot.example.com/dkim", nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("generate: %d: %s", res.StatusCode, buf)
	}

	res, buf = h.do("GET", "/api/v1/domains/rot.example.com/dkim", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d: %s", res.StatusCode, buf)
	}
	var listOut struct {
		Items []struct {
			IsActive bool `json:"is_active"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &listOut); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	activeCount := 0
	for _, it := range listOut.Items {
		if it.IsActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("want exactly 1 active key, got %d (total=%d)", activeCount, len(listOut.Items))
	}
}

func TestDKIM_AdminGating(t *testing.T) {
	h := newDKIMTestHarness(t, true)
	h.createDomain("gate.example.com")

	// Create a non-admin principal and mint an API key for it.
	res, buf := h.do("POST", "/api/v1/principals",
		map[string]any{"email": "plain@test.local", "password": "correct-horse-battery-staple"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create principal: %d: %s", res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		t.Fatalf("decode principal: %v", err)
	}
	res, buf = h.do("POST",
		fmt.Sprintf("/api/v1/principals/%d/api-keys", p.ID),
		map[string]any{"label": "plain-key"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create non-admin key: %d: %s", res.StatusCode, buf)
	}
	var keyOut struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &keyOut); err != nil {
		t.Fatalf("decode key: %v: %s", err, buf)
	}
	plainKey := keyOut.Key

	res, _ = h.doAs(plainKey, "POST", "/api/v1/domains/gate.example.com/dkim", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("POST dkim with non-admin key: got %d, want 403", res.StatusCode)
	}
	res, _ = h.doAs(plainKey, "GET", "/api/v1/domains/gate.example.com/dkim", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("GET dkim with non-admin key: got %d, want 403", res.StatusCode)
	}
}

func TestDKIM_UnknownDomain_Returns404(t *testing.T) {
	h := newDKIMTestHarness(t, true)

	res, _ := h.do("POST", "/api/v1/domains/nosuchdomain.example.com/dkim", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("generate on unknown domain: got %d, want 404", res.StatusCode)
	}
	res, _ = h.do("GET", "/api/v1/domains/nosuchdomain.example.com/dkim", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("list on unknown domain: got %d, want 404", res.StatusCode)
	}
}

func TestDKIM_NoManager_Returns501(t *testing.T) {
	h := newDKIMTestHarness(t, false)
	h.createDomain("nil.example.com")

	res, _ := h.do("POST", "/api/v1/domains/nil.example.com/dkim", nil)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST without manager: got %d, want 501", res.StatusCode)
	}
	res, _ = h.do("GET", "/api/v1/domains/nil.example.com/dkim", nil)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("GET without manager: got %d, want 501", res.StatusCode)
	}
}
