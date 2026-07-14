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
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// oauthHarness wraps a test server with OAuth provider configuration.
type oauthHarness struct {
	t           *testing.T
	fs          store.Store
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
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
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

// newOAuthHarnessWithBaseURL is newOAuthHarness with the server's
// Options.BaseURL explicitly configured to a canonical origin, used to
// assert that a configured canonical origin -- not a request's Host /
// X-Forwarded-Host header -- determines the OIDC/OAuth callback URL
// (re #240).
func newOAuthHarnessWithBaseURL(t *testing.T, baseURL string) *oauthHarness {
	t.Helper()

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
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
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
		BaseURL:                   baseURL,
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

// TestOAuthStart_IgnoresForeignHostAndXForwardedHost asserts that once
// Options.BaseURL is configured (the deployment's canonical
// [server] public_base_url), a request carrying a foreign Host header or
// a foreign X-Forwarded-Host header does not steer the redirect_uri
// registered with the OAuth provider (re #240). Before the fix,
// buildCallbackURL built the redirect_uri directly from these headers.
func TestOAuthStart_IgnoresForeignHostAndXForwardedHost(t *testing.T) {
	const canonicalOrigin = "https://mail.example.com"
	oh := newOAuthHarnessWithBaseURL(t, canonicalOrigin)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth-foreign-host@example.com")

	cases := []struct {
		name    string
		setForm func(*http.Request)
	}{
		{
			name: "foreign Host header",
			setForm: func(r *http.Request) {
				r.Host = "evil.example"
			},
		},
		{
			name: "foreign X-Forwarded-Host header",
			setForm: func(r *http.Request) {
				r.Header.Set("X-Forwarded-Host", "evil.example")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST",
				oh.baseURL+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail",
				nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+apiKey)
			tc.setForm(req)

			res, err := oh.client.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusFound {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("expected 302, got %d: %s", res.StatusCode, body)
			}
			loc := res.Header.Get("Location")
			u, err := url.Parse(loc)
			if err != nil {
				t.Fatalf("parse Location: %v", err)
			}
			redirectURI := u.Query().Get("redirect_uri")
			if !strings.HasPrefix(redirectURI, canonicalOrigin) {
				t.Errorf("redirect_uri = %q; want prefix %q (the configured BaseURL, not the request header)",
					redirectURI, canonicalOrigin)
			}
			if strings.Contains(redirectURI, "evil.example") {
				t.Errorf("redirect_uri = %q; must not reflect the attacker-supplied host", redirectURI)
			}
		})
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
	// The redirect_uri must be the FIXED callback path — no identity id in URL.
	redirectURI := q.Get("redirect_uri")
	if !strings.HasSuffix(redirectURI, "/api/v1/oauth/external-submission/callback") {
		t.Errorf("redirect_uri = %q; want suffix /api/v1/oauth/external-submission/callback", redirectURI)
	}
	if strings.Contains(redirectURI, identityID) {
		t.Errorf("redirect_uri %q must not contain identity id %q", redirectURI, identityID)
	}
}

// TestOAuthStart_JSONResponse verifies that when Accept: application/json is
// set the start endpoint returns 200 with a JSON body containing auth_url
// rather than a 302 redirect. This is the path taken by the Suite SPA's
// fetch-based call which carries X-CSRF-Token as a header (re #72).
func TestOAuthStart_JSONResponse(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauthjson@example.com")

	req, err := http.NewRequest("POST",
		oh.baseURL+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail",
		nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	res, err := oh.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	buf, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, buf)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
	var body struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body.AuthURL == "" {
		t.Fatal("auth_url is empty")
	}
	u, err := url.Parse(body.AuthURL)
	if err != nil {
		t.Fatalf("parse auth_url: %v", err)
	}
	q := u.Query()
	if q.Get("state") == "" {
		t.Error("state param missing from auth_url")
	}
	if q.Get("code_challenge") == "" {
		t.Error("code_challenge param missing from auth_url")
	}
	if !strings.Contains(u.Host, "accounts.google.com") {
		t.Errorf("auth_url host = %q; want accounts.google.com", u.Host)
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
	// The callback path is fixed (no identity id in URL; identity id lives in
	// the state token).
	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=fake-code",
		url.QueryEscape(stateTok))
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
	// RefreshDue must be 5 minutes before expiry (extsubmit.RefreshLeadTime),
	// not 80% of token lifetime. The fake provider returns expires_in=3600;
	// expected RefreshDue = OAuthExpiresAt - 5m = now + 3600s - 300s = now + 3300s.
	if sub.OAuthExpiresAt.IsZero() {
		t.Error("OAuthExpiresAt is zero; want non-zero")
	} else {
		wantRefreshDue := sub.OAuthExpiresAt.Add(-extsubmit.RefreshLeadTime)
		if !sub.RefreshDue.Equal(wantRefreshDue) {
			t.Errorf("RefreshDue = %v; want OAuthExpiresAt - RefreshLeadTime = %v", sub.RefreshDue, wantRefreshDue)
		}
	}
}

// TestOAuthCallback_MarksIdentityVerified asserts that after a successful
// OAuth callback the identity row's VerifiedAtUs is set (i.e. MarkIdentityVerified
// was called). This is the acceptance criterion from #92 that was missing before
// the fix in re #99: UpsertIdentitySubmission was called but MarkIdentityVerified
// was not, so verified_at_us stayed NULL and the identity showed "Nicht verifiziert"
// on every subsequent JMAP fetch.
func TestOAuthCallback_MarksIdentityVerified(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth-verify@example.com")

	// Confirm the identity starts unverified.
	before, err := oh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity before: %v", err)
	}
	if before.VerifiedAtUs != 0 {
		t.Fatalf("VerifiedAtUs before callback = %d; want 0 (unverified)", before.VerifiedAtUs)
	}

	// Run a complete start -> callback flow via the stubbed token endpoint.
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

	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=fake-code",
		url.QueryEscape(stateTok))
	cbRes, cbBody := oh.doRequest("GET", callbackPath, apiKey, nil)
	if cbRes.StatusCode != http.StatusNoContent {
		t.Fatalf("callback: expected 204, got %d: %s", cbRes.StatusCode, cbBody)
	}

	// The identity must now be marked verified.
	after, err := oh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity after: %v", err)
	}
	if after.VerifiedAtUs == 0 {
		t.Fatal("VerifiedAtUs is still 0 after successful OAuth callback; MarkIdentityVerified was not called")
	}

	// The submission row must also be present (regression guard for the
	// existing behaviour tested in TestOAuthCallback_ExchangesAndPersists).
	sub, err := oh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if sub.SubmitAuthMethod != "oauth2" {
		t.Errorf("SubmitAuthMethod = %q; want oauth2", sub.SubmitAuthMethod)
	}
}

// TestOAuthCallback_BadState verifies that an unknown or expired state token
// returns 400 with type oauth_state_invalid.
func TestOAuthCallback_BadState(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, _, _ := oh.bootstrapAndIdentity("oauth3@example.com")

	callbackPath := "/api/v1/oauth/external-submission/callback?state=nonexistent&code=x"
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

	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=x",
		url.QueryEscape(stateTok))
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

// TestOAuthCallback_NoCookieSucceeds verifies that the callback endpoint
// succeeds when called with a valid state token but NO session cookie and NO
// Authorization header. This is the real-world case: the browser arrives at
// the callback via a cross-site top-level redirect from Google and
// SameSite=Strict cookies are not sent.
//
// Before the fix (re #95) the route was gated by requireAuth, which would
// reject the cookieless request with 401 before any code exchange ran.
func TestOAuthCallback_NoCookieSucceeds(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth-nocookie@example.com")

	// Start flow to get the state token. This call IS authenticated (apiKey
	// bearer) — the start endpoint retains its auth+requireSelfOnly gate.
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

	// Call the callback with NO authorization header and NO cookie — simulating
	// the cross-site browser redirect from Google (SameSite=Strict means no
	// session cookie is sent, re #95).
	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=fake-code",
		url.QueryEscape(stateTok))
	req, err := http.NewRequest("GET", oh.baseURL+callbackPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Deliberately omit Authorization header and send no cookies.
	res, err := oh.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("callback without cookie: expected 204, got %d: %s", res.StatusCode, body)
	}

	// Verify the row was persisted (same as the authenticated path).
	sub, err := oh.fs.Meta().GetIdentitySubmission(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if sub.SubmitAuthMethod != "oauth2" {
		t.Errorf("SubmitAuthMethod = %q; want oauth2", sub.SubmitAuthMethod)
	}
	// OAuthClientID must equal the identity's email address (not the now-absent
	// caller.CanonicalEmail, re #95).
	if sub.OAuthClientID != "oauth-nocookie@example.com" {
		t.Errorf("OAuthClientID = %q; want oauth-nocookie@example.com", sub.OAuthClientID)
	}
}

// TestOAuthCallback_InvalidStateNoAuth verifies that a missing or forged state
// token is rejected with 400 oauth_state_invalid even when the route is
// unauthenticated. A bad actor who discovers the unauth route cannot complete
// a flow without a valid CSPRNG state token.
func TestOAuthCallback_InvalidStateNoAuth(t *testing.T) {
	oh := newOAuthHarness(t)
	oh.bootstrapAndIdentity("oauth-badstate@example.com")

	// Call with a forged / unknown state token and no credential of any kind.
	callbackPath := "/api/v1/oauth/external-submission/callback?state=forged-token-xxxxx&code=x"
	req, err := http.NewRequest("GET", oh.baseURL+callbackPath, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// No Authorization header, no cookie.
	res, err := oh.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged state: expected 400, got %d: %s", res.StatusCode, body)
	}
	var prob struct {
		Type string `json:"type"`
	}
	json.Unmarshal(body, &prob)
	if !strings.Contains(prob.Type, "oauth_state_invalid") {
		t.Errorf("type = %q; want to contain oauth_state_invalid", prob.Type)
	}
}

// TestOAuthStart_MissingClientSecret verifies that a provider whose
// ClientSecret is empty returns 503 oauth_provider_not_configured.
func TestOAuthStart_MissingClientSecret(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe: func(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
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

// TestOAuthCallback_PopupMode_Success verifies that when the start request
// carries display=popup the callback returns a text/html completion page (not
// a 204 or redirect) that contains the postMessage call with ok=true.
func TestOAuthCallback_PopupMode_Success(t *testing.T) {
	oh := newOAuthHarness(t)
	apiKey, identityID, _ := oh.bootstrapAndIdentity("oauth-popup-ok@example.com")

	// Start flow with display=popup.
	req, err := http.NewRequest("POST",
		oh.baseURL+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail&display=popup",
		nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	res, err := oh.client.Do(req)
	if err != nil {
		t.Fatalf("do start: %v", err)
	}
	buf, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start: expected 200, got %d: %s", res.StatusCode, buf)
	}
	var startBody struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(buf, &startBody); err != nil {
		t.Fatalf("parse start body: %v", err)
	}
	u, _ := url.Parse(startBody.AuthURL)
	stateTok := u.Query().Get("state")
	if stateTok == "" {
		t.Fatal("no state in auth_url")
	}

	// Call the callback. The probe is alwaysOKProbe (injected in newOAuthHarness).
	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=fake-code",
		url.QueryEscape(stateTok))
	cbRes, cbBody := oh.doRequest("GET", callbackPath, "", nil)

	// Popup mode: expect 200 HTML, not 204.
	if cbRes.StatusCode != http.StatusOK {
		t.Fatalf("popup callback: expected 200, got %d: %s", cbRes.StatusCode, cbBody)
	}
	ct := cbRes.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("popup callback Content-Type = %q; want text/html", ct)
	}
	bodyStr := string(cbBody)
	if !strings.Contains(bodyStr, `"herold:oauth-result"`) {
		t.Error("popup page does not contain herold:oauth-result message type")
	}
	if !strings.Contains(bodyStr, `"ok":true`) {
		t.Errorf("popup page does not contain ok:true; body = %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `window.location.origin`) {
		t.Error("popup page does not use window.location.origin as postMessage targetOrigin")
	}
}

// TestOAuthCallback_PopupMode_ProbeFailure verifies that when display=popup
// and the probe rejects the credentials the callback still returns a 200
// HTML page with ok=false and the diagnostic in the postMessage payload.
func TestOAuthCallback_PopupMode_ProbeFailure(t *testing.T) {
	// Build a harness with a probe that always fails auth.
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	// Fake token endpoint used by the harness.
	fakeSv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	t.Cleanup(fakeSv.Close)

	failProbe := func(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
		return extsubmit.Outcome{
			State:      extsubmit.OutcomeAuthFailed,
			Diagnostic: "535 authentication failed",
		}
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:        1,
		BootstrapWindow:           5 * time.Minute,
		RequestsPerMinutePerKey:   100,
		ExternalSubmissionDataKey: testDataKey,
		ExternalProbe:             failProbe,
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

	// Bootstrap + identity.
	bsBody, _ := json.Marshal(map[string]string{"email": "popup-probe-fail@example.com", "display_name": "T"})
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
	identityID := fmt.Sprintf("popup-fail-id-%d", bsOut.PrincipalID)
	fs.Meta().InsertJMAPIdentity(context.Background(), store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: store.PrincipalID(bsOut.PrincipalID),
		Name:        "T",
		Email:       "popup-probe-fail@example.com",
		MayDelete:   true,
	})

	// Start with display=popup.
	startReq, _ := http.NewRequest("POST",
		base+"/api/v1/identities/"+identityID+"/submission/oauth/start?provider=gmail&display=popup",
		nil)
	startReq.Header.Set("Authorization", "Bearer "+bsOut.InitialAPIKey)
	startReq.Header.Set("Accept", "application/json")
	startRes, _ := client.Do(startReq)
	startRaw, _ := io.ReadAll(startRes.Body)
	startRes.Body.Close()
	if startRes.StatusCode != http.StatusOK {
		t.Fatalf("start: expected 200, got %d", startRes.StatusCode)
	}
	var startOut struct {
		AuthURL string `json:"auth_url"`
	}
	json.Unmarshal(startRaw, &startOut)
	u, _ := url.Parse(startOut.AuthURL)
	stateTok := u.Query().Get("state")

	// Call the callback. The failProbe will reject it.
	callbackPath := fmt.Sprintf("/api/v1/oauth/external-submission/callback?state=%s&code=fake-code",
		url.QueryEscape(stateTok))
	cbReq, _ := http.NewRequest("GET", base+callbackPath, nil)
	cbRes, _ := client.Do(cbReq)
	cbBody, _ := io.ReadAll(cbRes.Body)
	cbRes.Body.Close()

	// Popup probe failure: expect 200 HTML with ok=false and the diagnostic.
	if cbRes.StatusCode != http.StatusOK {
		t.Fatalf("popup probe failure: expected 200, got %d: %s", cbRes.StatusCode, cbBody)
	}
	ct := cbRes.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html", ct)
	}
	bodyStr := string(cbBody)
	if !strings.Contains(bodyStr, `"ok":false`) {
		t.Errorf("popup failure page does not contain ok:false; body = %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "535 authentication failed") {
		t.Errorf("popup failure page does not contain the probe diagnostic; body = %s", bodyStr)
	}
}
