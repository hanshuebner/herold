package protoadmin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

type harness struct {
	t       *testing.T
	h       *testharness.Server
	srv     *protoadmin.Server
	client  *http.Client
	baseURL string
	clk     *clock.FakeClock
	dir     *directory.Directory
	rp      *directoryoidc.RP
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "admin"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
}

// doRequest builds and executes a request. The body, if non-nil, is
// JSON-encoded. When key is non-empty, adds Authorization.
func (h *harness) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.baseURL+path, rdr)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// bootstrap performs the bootstrap call and returns (principalID, apiKey).
func (h *harness) bootstrap(email string) (uint64, string) {
	h.t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email":        email,
		"display_name": "Initial Admin",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("bootstrap: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		PrincipalID   uint64 `json:"principal_id"`
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		h.t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	return out.PrincipalID, out.InitialAPIKey
}

// createPrincipal creates a non-admin principal, returning its ID.
func (h *harness) createPrincipal(adminKey, email string) uint64 {
	h.t.Helper()
	res, buf := h.doRequest("POST", "/api/v1/principals", adminKey, map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("create %s: %d: %s", email, res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return p.ID
}

func TestHealthz_Live_Ready(t *testing.T) {
	h := newHarness(t)
	res, _ := h.doRequest("GET", "/api/v1/healthz/live", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", res.StatusCode)
	}
	res, _ = h.doRequest("GET", "/api/v1/healthz/ready", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ready = %d", res.StatusCode)
	}
}

func TestBootstrap_CreatesFirstAdminAndKey(t *testing.T) {
	h := newHarness(t)
	pid, key := h.bootstrap("admin@example.com")
	if pid == 0 {
		t.Fatalf("pid zero")
	}
	if !strings.HasPrefix(key, protoadmin.APIKeyPrefix) {
		t.Fatalf("api key missing prefix: %q", key)
	}

	// Second call must fail with 409.
	res, buf := h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email": "admin2@example.com",
	})
	// With 1 req / window rate limit we may either see 429 or 409
	// depending on the order. Default limit is 1 so the second call
	// is rate-limited; advance to clear the window, then expect 409.
	if res.StatusCode == http.StatusTooManyRequests {
		h.clk.Advance(6 * time.Minute)
		res, buf = h.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
			"email": "admin2@example.com",
		})
	}
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second bootstrap = %d: %s", res.StatusCode, buf)
	}
}

func TestPrincipals_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	// Create.
	res, buf := h.doRequest("POST", "/api/v1/principals", key, map[string]any{
		"email":    "alice@example.com",
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Get.
	res, buf = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get = %d: %s", res.StatusCode, buf)
	}
	// PATCH display name.
	res, buf = h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, map[string]any{
		"display_name": "Alice",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d: %s", res.StatusCode, buf)
	}
	var patched struct {
		DisplayName string `json:"display_name"`
	}
	_ = json.Unmarshal(buf, &patched)
	if patched.DisplayName != "Alice" {
		t.Fatalf("display_name = %q", patched.DisplayName)
	}
	// Add an alias so we can assert cascade.
	res, buf = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "alias",
		"domain":              "example.com",
		"target_principal_id": created.ID,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("alias create = %d: %s", res.StatusCode, buf)
	}
	// Delete principal.
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	// Get should now 404.
	res, _ = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", res.StatusCode)
	}
	// List should not include the principal.
	res, buf = h.doRequest("GET", "/api/v1/principals", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []struct {
			ID uint64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	for _, it := range page.Items {
		if it.ID == created.ID {
			t.Fatalf("deleted principal still listed")
		}
	}
}

func TestPrincipals_Keyset_Pagination(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	// Seed 50 principals.
	for i := 0; i < 50; i++ {
		h.createPrincipal(key, fmt.Sprintf("user%02d@example.com", i))
	}
	seen := map[uint64]bool{}
	cursor := ""
	for {
		path := "/api/v1/principals?limit=10"
		if cursor != "" {
			path += "&after=" + cursor
		}
		res, buf := h.doRequest("GET", path, key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list = %d: %s", res.StatusCode, buf)
		}
		var page struct {
			Items []struct {
				ID uint64 `json:"id"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		if err := json.Unmarshal(buf, &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("id %d repeated", it.ID)
			}
			seen[it.ID] = true
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	// 50 created + 1 admin bootstrap = 51
	if len(seen) != 51 {
		t.Fatalf("saw %d principals, want 51", len(seen))
	}
}

func TestPasswords_AdminSet_SelfChange_BothWork(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	bobID := h.createPrincipal(adminKey, "bob@example.com")
	// Admin-set: no current_password required.
	res, buf := h.doRequest("PUT", fmt.Sprintf("/api/v1/principals/%d/password", bobID), adminKey, map[string]any{
		"new_password": "new-strong-password-1",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("admin set = %d: %s", res.StatusCode, buf)
	}
	// Verify via directory Authenticate.
	if _, err := h.dir.Authenticate(context.Background(), "bob@example.com", "new-strong-password-1"); err != nil {
		t.Fatalf("authenticate after admin set: %v", err)
	}
	// Self-change: bob needs an API key to call the endpoint. Mint one
	// as admin (create API key for bob).
	res, buf = h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", bobID), adminKey, map[string]any{
		"label": "bobs-cli",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create bob key = %d: %s", res.StatusCode, buf)
	}
	var keyDoc struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(buf, &keyDoc)
	if keyDoc.Key == "" {
		t.Fatalf("no plaintext key returned")
	}
	// Bob changes his own password.
	res, buf = h.doRequest("PUT", fmt.Sprintf("/api/v1/principals/%d/password", bobID), keyDoc.Key, map[string]any{
		"current_password": "new-strong-password-1",
		"new_password":     "even-stronger-password-2",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("self change = %d: %s", res.StatusCode, buf)
	}
	if _, err := h.dir.Authenticate(context.Background(), "bob@example.com", "even-stronger-password-2"); err != nil {
		t.Fatalf("authenticate after self change: %v", err)
	}
}

func TestTOTP_Enroll_Confirm_Verify_Disable(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	pid := h.createPrincipal(adminKey, "totp@example.com")
	// Enroll.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/totp/enroll", pid), adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d: %s", res.StatusCode, buf)
	}
	var enroll struct {
		Secret          string `json:"secret"`
		ProvisioningURI string `json:"provisioning_uri"`
	}
	_ = json.Unmarshal(buf, &enroll)
	if enroll.Secret == "" || enroll.ProvisioningURI == "" {
		t.Fatalf("missing secret/uri: %+v", enroll)
	}
	// Generate a valid code for the current fake clock.
	code := totpCodeFor(t, enroll.Secret, h.clk.Now())
	res, buf = h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/totp/confirm", pid), adminKey, map[string]any{
		"code": code,
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("confirm = %d: %s", res.StatusCode, buf)
	}
	// Disable.
	res, buf = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d/totp", pid), adminKey, map[string]any{
		"current_password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("disable = %d: %s", res.StatusCode, buf)
	}
}

// totpCodeFor generates a TOTP code for the given secret at instant t
// using the same parameters the directory package uses (SHA-1, 6
// digits, 30 s period). Duplicated here rather than reaching into the
// directory's private helpers.
func totpCodeFor(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	// pquerna/otp lives under the directory's transitive deps; use it
	// the same way that package does.
	code, err := totpGenerate(secret, at)
	if err != nil {
		t.Fatalf("totp generate: %v", err)
	}
	return code
}

func TestDomains_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	res, buf := h.doRequest("POST", "/api/v1/domains", key, map[string]any{
		"name": "example.com",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("GET", "/api/v1/domains", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), "example.com") {
		t.Fatalf("list missing domain: %s", buf)
	}
	res, _ = h.doRequest("DELETE", "/api/v1/domains/example.com", key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	res, _ = h.doRequest("DELETE", "/api/v1/domains/example.com", key, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again = %d", res.StatusCode)
	}
}

func TestAliases_CRUD_ScopedByDomain(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	pid := h.createPrincipal(key, "target@example.com")
	// Create alias.
	res, buf := h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "support",
		"domain":              "example.com",
		"target_principal_id": pid,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(buf, &created)
	// Create an alias in a different domain so filter is exercised.
	_, _ = h.doRequest("POST", "/api/v1/aliases", key, map[string]any{
		"local":               "info",
		"domain":              "other.test",
		"target_principal_id": pid,
	})
	// List scoped.
	res, buf = h.doRequest("GET", "/api/v1/aliases?domain=example.com", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) != 1 {
		t.Fatalf("list len = %d, want 1: %s", len(page.Items), buf)
	}
	// Delete.
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/aliases/%d", created.ID), key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
}

func TestOIDCProviders_CRUD(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")

	stub := newOIDCStubMini(t)
	// Create provider against the stub.
	res, buf := h.doRequest("POST", "/api/v1/oidc/providers", key, map[string]any{
		"name":          "google",
		"issuer":        stub.URL,
		"client_id":     "cid",
		"client_secret": "csecret",
		"scopes":        []string{"email"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	// List.
	res, buf = h.doRequest("GET", "/api/v1/oidc/providers", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if !strings.Contains(string(buf), `"google"`) {
		t.Fatalf("list missing provider: %s", buf)
	}
	// Delete.
	res, _ = h.doRequest("DELETE", "/api/v1/oidc/providers/google", key, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", res.StatusCode)
	}
}

// newOIDCStubMini returns an httptest.Server speaking just enough of
// the OIDC discovery endpoint for RP.AddProvider to succeed. Full
// token-exchange flow is tested in directoryoidc; here we only need
// AddProvider to find a valid issuer.
func newOIDCStubMini(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	srv := httptest.NewServer(mux)
	issuer = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func TestAPIKeys_Create_Returns_Plaintext_Once(t *testing.T) {
	h := newHarness(t)
	pid, adminKey := h.bootstrap("admin@example.com")
	// Admin creates an API key for self.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", pid), adminKey, map[string]any{
		"label": "machine-1",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID  uint64 `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Key == "" {
		t.Fatalf("no plaintext key returned")
	}
	// GET on listing does NOT include the plaintext.
	res, buf = h.doRequest("GET", "/api/v1/api-keys", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d: %s", res.StatusCode, buf)
	}
	if strings.Contains(string(buf), created.Key) {
		t.Fatalf("listing leaked plaintext key: %s", buf)
	}
}

func TestAuditLog_Filters(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	// Produce a few audit entries by creating principals.
	_ = h.createPrincipal(adminKey, "a@example.com")
	_ = h.createPrincipal(adminKey, "b@example.com")
	// GET audit with action filter.
	res, buf := h.doRequest("GET", "/api/v1/audit?action=principal.create&limit=10", adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d: %s", res.StatusCode, buf)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(buf, &page)
	if len(page.Items) == 0 {
		t.Fatalf("no audit rows: %s", buf)
	}
	for _, it := range page.Items {
		if it["action"] != "principal.create" {
			t.Fatalf("filter leaked: %v", it)
		}
	}
}

func TestAuthentication_Bearer_APIKey_Required(t *testing.T) {
	h := newHarness(t)
	_, _ = h.bootstrap("admin@example.com")
	// No auth -> 401.
	res, _ := h.doRequest("GET", "/api/v1/principals", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", res.StatusCode)
	}
	// Wrong scheme -> 401.
	req, _ := http.NewRequest("GET", h.baseURL+"/api/v1/principals", nil)
	req.Header.Set("Authorization", "Basic Zm9vOmJhcg==")
	res, _ = h.client.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic auth = %d, want 401", res.StatusCode)
	}
	res.Body.Close()
}

func TestAuthorization_SelfVsAdmin_Scope(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	bobID := h.createPrincipal(adminKey, "bob@example.com")
	carolID := h.createPrincipal(adminKey, "carol@example.com")
	// Mint a key for bob.
	res, buf := h.doRequest("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", bobID), adminKey, map[string]any{
		"label": "bob",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bob key create = %d: %s", res.StatusCode, buf)
	}
	var bobKeyDoc struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(buf, &bobKeyDoc)
	// Bob cannot DELETE Carol (not admin).
	res, _ = h.doRequest("DELETE", fmt.Sprintf("/api/v1/principals/%d", carolID), bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bob delete carol = %d, want 403", res.StatusCode)
	}
	// Bob CAN GET self.
	res, _ = h.doRequest("GET", fmt.Sprintf("/api/v1/principals/%d", bobID), bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bob get self = %d", res.StatusCode)
	}
	// Bob CANNOT list principals (admin-only).
	res, _ = h.doRequest("GET", "/api/v1/principals", bobKeyDoc.Key, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bob list = %d, want 403", res.StatusCode)
	}
}

func TestRateLimit_PerKey_Burst_ReturnsRetryAfter(t *testing.T) {
	h := newHarness(t)
	_, key := h.bootstrap("admin@example.com")
	// With RequestsPerMinutePerKey=100 and a generous key, 100 calls
	// in a row should succeed; the 101st must 429. Run 100 then one.
	for i := 0; i < 100; i++ {
		res, buf := h.doRequest("GET", "/api/v1/principals", key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("call %d = %d: %s", i, res.StatusCode, buf)
		}
	}
	res, buf := h.doRequest("GET", "/api/v1/principals", key, nil)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate-limited call = %d: %s", res.StatusCode, buf)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing")
	}
}

func TestPanic_InHandler_Returns500_NotCrash(t *testing.T) {
	// The panic-recover middleware is tested via the exported
	// WrapRecover helper so we can drive a stub handler that
	// intentionally panics without forging a store-level fault. One
	// panic must not crash the process: the outer http.Server would
	// normally catch goroutine panics, but the admin middleware
	// catches them earlier and emits a typed 500.
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{})

	panicker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	wrapped := protoadmin.WrapRecoverForTest(srv, panicker)
	rec := httptest.NewRecorder()
	rr, _ := http.NewRequest("GET", "/anything", nil)
	wrapped.ServeHTTP(rec, rr)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic wrapper status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("panic body = %s", rec.Body.String())
	}
}

// -- helpers --------------------------------------------------------

// totpGenerate returns a RFC 6238 TOTP code at instant at for the given
// base32 secret, using the same parameter set the directory package
// uses (SHA-1, 6 digits, 30 s period). Kept as a thin test helper
// rather than reaching into directory's unexported helpers.
func totpGenerate(secret string, at time.Time) (string, error) {
	return otpGenerateCode(secret, at)
}
