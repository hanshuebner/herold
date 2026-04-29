package protoadmin_test

// oauth_init_test.go covers the server-mediated OAuth 2.0 start + callback
// endpoints for external SMTP submission (REQ-MAIL-SUBMIT-02,
// REQ-AUTH-EXT-SUBMIT-03).
//
// Test matrix:
//   - POST /oauth/start?provider=gmail -> 302 to provider auth_url with
//     state, code_challenge query params
//   - GET /oauth/callback?state=...&code=... with fake token endpoint ->
//     persists sub in store; returns 204
//   - Mismatched/expired state -> 400 oauth_state_invalid
//   - Unknown provider name -> 503 oauth_provider_not_configured
//   - Missing client secret -> 503 oauth_provider_not_configured

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// oauthHarness wraps a test server with OAuth provider configuration.
type oauthHarness struct {
	t           *testing.T
	fs          *fakestore.Store
	clk         *clock.FakeClock
	srv         *protoadmin.Server
	client      *http.Client
	baseURL     string
	fakeTokenSv *httptest.Server
}

// newOAuthHarness creates a harness with a fake OAuth provider configured.
// The fake token endpoint echoes back a static access token "fake-access-token"
// and a refresh token "fake-refresh-token".
func newOAuthHarness(t *testing.T) *oauthHarness {
	t.Helper()

	// Fake token endpoint.
	fakeSv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fake-access-token",
			"refresh_token": "fake-refresh-token",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(fakeSv.Close)

	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
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
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe:             alwaysOKProbe,
		OAuthProviders: map[string]protoadmin.OAuthProviderOptions{
			"gmail": {
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
				AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:     fakeSv.URL + "/token",
				Scopes:       []string{"https://mail.google.com/"},
			},
		},
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &oauthHarness{
		t:           t,
		fs:          fs,
		clk:         clk,
		srv:         srv,
		client:      client,
		baseURL:     base,
		fakeTokenSv: fakeSv,
	}
}

// doRequest sends an authenticated HTTP request to the harness.
func (oh *oauthHarness) doRequest(method, path, key string, body any) (*http.Response, []byte) {
	oh.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			oh.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, oh.baseURL+path, rdr)
	if err != nil {
		oh.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := oh.client.Do(req)
	if err != nil {
		oh.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		oh.t.Fatalf("read: %v", err)
	}
	return res, buf
}

// bootstrapAndIdentity creates the first admin and an identity, returning
// (apiKey, identityID, principalID).
func (oh *oauthHarness) bootstrapAndIdentity(email string) (string, string, uint64) {
	oh.t.Helper()
	res, buf := oh.doRequest("POST", "/api/v1/bootstrap", "", map[string]any{
		"email":        email,
		"display_name": "Admin",
	})
	if res.StatusCode != http.StatusCreated {
		oh.t.Fatalf("bootstrap: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
		PrincipalID   uint64 `json:"principal_id"`
	}
	json.Unmarshal(buf, &out)
	apiKey := out.InitialAPIKey
	pid := out.PrincipalID

	identityID := fmt.Sprintf("oauth-identity-%d", pid)
	oh.fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(pid),
		Name:        "Test",
		Email:       email,
		MayDelete:   true,
	})
	return apiKey, identityID, pid
}

// TestOAuthStart_RedirectsToProvider verifies that POST /oauth/start?provider=gmail
// returns 302 to the provider's auth_url with state and code_challenge params.
func TestOAuthStart_RedirectsToProvider(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth@example.com")

	res, _ := oh.doRequest("POST",
		"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail",
		apiKey, nil)

	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if loc == "" {
		t.Fatal("no Location header")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := u.Query()
	if q.Get("state") == "" {
		t.Error("state param missing from redirect URL")
	}
	if q.Get("code_challenge") == "" {
		t.Error("code_challenge param missing from redirect URL")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q; want S256", q.Get("code_challenge_method"))
	}
	if !strings.Contains(u.Host, "accounts.google.com") {
		t.Errorf("redirect host = %q; want accounts.google.com", u.Host)
	}
}

// TestOAuthCallback_ExchangesAndPersists verifies that after a start, the
// callback handler exchanges the code at the fake token endpoint, seals the
// tokens, and persists the row.
func TestOAuthCallback_ExchangesAndPersists(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth2@example.com")

	// Start flow to get the state token from the redirect URL.
	startRes, _ := oh.doRequest("POST",
		"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail",
		apiKey, nil)
	if startRes.StatusCode != http.StatusFound {
		t.Fatalf("start: expected 302, got %d", startRes.StatusCode)
	}
	loc := startRes.Header.Get("Location")
	u, _ := url.Parse(loc)
	stateTok := u.Query().Get("state")
	if stateTok == "" {
		t.Fatal("no state in redirect")
	}

	// Call callback with the state token and a fake code.
	callbackPath := fmt.Sprintf("/api/v1/identities/%s/submission/oauth/callback?state=%s&code=fake-code",
		identityID, url.QueryEscape(stateTok))
	cbRes, cbBody := oh.doRequest("GET", callbackPath, apiKey, nil)
	if cbRes.StatusCode != http.StatusNoContent {
		t.Fatalf("callback: expected 204, got %d: %s", cbRes.StatusCode, cbBody)
	}

	// Verify the row was persisted.
	sub, err := oh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if sub.SubmitAuthMethod != "oauth2" {
		t.Errorf("SubmitAuthMethod = %q; want oauth2", sub.SubmitAuthMethod)
	}
	if len(sub.OAuthAccessCT) == 0 {
		t.Errorf("OAuthAccessCT is empty; want sealed access token")
	}
	if len(sub.OAuthRefreshCT) == 0 {
		t.Errorf("OAuthRefreshCT is empty; want sealed refresh token")
	}
}

// TestOAuthCallback_BadState verifies that an unknown or expired state token
// returns 400 with type oauth_state_invalid.
func TestOAuthCallback_BadState(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth3@example.com")

	callbackPath := fmt.Sprintf("/api/v1/identities/%s/submission/oauth/callback?state=nonexistent&code=x",
		identityID)
	res, buf := oh.doRequest("GET", callbackPath, apiKey, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", res.StatusCode, buf)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(buf, &prob)
	if !strings.Contains(prob.Type, "oauth_state_invalid") {
		t.Errorf("type = %q; want to contain oauth_state_invalid", prob.Type)
	}
}

// TestOAuthCallback_ExpiredState verifies that a state token past its TTL
// is rejected.
func TestOAuthCallback_ExpiredState(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth4@example.com")

	// Start to get state token.
	startRes, _ := oh.doRequest("POST",
		"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail",
		apiKey, nil)
	loc := startRes.Header.Get("Location")
	u, _ := url.Parse(loc)
	stateTok := u.Query().Get("state")

	// Advance clock past the 5-minute TTL.
	oh.clk.Advance(6 * time.Minute)

	callbackPath := fmt.Sprintf("/api/v1/identities/%s/submission/oauth/callback?state=%s&code=x",
		identityID, url.QueryEscape(stateTok))
	res, buf := oh.doRequest("GET", callbackPath, apiKey, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expired state: expected 400, got %d: %s", res.StatusCode, buf)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(buf, &prob)
	if !strings.Contains(prob.Type, "oauth_state_invalid") {
		t.Errorf("type = %q; want to contain oauth_state_invalid", prob.Type)
	}
}

// TestOAuthStart_UnknownProvider verifies that an unknown provider name
// returns 503 oauth_provider_not_configured.
func TestOAuthStart_UnknownProvider(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth5@example.com")

	res, buf := oh.doRequest("POST",
		"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=unknownprovider",
		apiKey, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", res.StatusCode, buf)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(buf, &prob)
	if !strings.Contains(prob.Type, "oauth_provider_not_configured") {
		t.Errorf("type = %q; want to contain oauth_provider_not_configured", prob.Type)
	}
}

// TestOAuthStart_MissingClientSecret verifies that a provider whose
// ClientSecret is empty returns 503 oauth_provider_not_configured.
func TestOAuthStart_MissingClientSecret(t *testing.T) {
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
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe: func(_ context.Context, _ store.IdentitySubmission) extsubmit.Outcome {
			return extsubmit.Outcome{State: extsubmit.OutcomeOK}
		},
		OAuthProviders: map[string]protoadmin.OAuthProviderOptions{
			"nogmail": {
				ClientID:     "id",
				ClientSecret: "", // empty — simulates unresolved secret reference
				AuthURL:      "https://example.com/auth",
				TokenURL:     "https://example.com/token",
				Scopes:       []string{"mail"},
			},
		},
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// Bootstrap.
	bsBody, _ := json.Marshal(map[string]string{"email": "nosecret@example.com", "display_name": "A"})
	bsReq, _ := http.NewRequest("POST", base+"/api/v1/bootstrap", bytes.NewReader(bsBody))
	bsReq.Header.Set("Content-Type", "application/json")
	bsRes, _ := client.Do(bsReq)
	bsRaw, _ := io.ReadAll(bsRes.Body)
	bsRes.Body.Close()
	var bsOut struct {
		InitialAPIKey string `json:"initial_api_key"`
		PrincipalID   uint64 `json:"principal_id"`
	}
	json.Unmarshal(bsRaw, &bsOut)

	identityID := fmt.Sprintf("nosecret-id-%d", bsOut.PrincipalID)
	fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(bsOut.PrincipalID),
		Name:        "Test",
		Email:       "nosecret@example.com",
		MayDelete:   true,
	})

	startReq, _ := http.NewRequest("POST",
		base+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=nogmail", nil)
	startReq.Header.Set("Authorization", "Bearer "+bsOut.InitialAPIKey)
	startRes, _ := client.Do(startReq)
	startBody, _ := io.ReadAll(startRes.Body)
	startRes.Body.Close()

	if startRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", startRes.StatusCode, startBody)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(startBody, &prob)
	if !strings.Contains(prob.Type, "oauth_provider_not_configured") {
		t.Errorf("type = %q; want to contain oauth_provider_not_configured", prob.Type)
	}
}
