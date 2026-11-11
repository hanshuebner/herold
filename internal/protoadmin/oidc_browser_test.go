package protoadmin_test

// oidc_browser_test.go drives the Suite's browser OIDC flows over HTTP
// against the in-tree fake IdP (internal/testfakes/fakeoidc, PKCE
// required like a Pocket ID client with PKCE enabled): listing providers
// on the sign-in page, linking a password-authenticated principal from
// its settings, signing in through the linked identity into a session
// cookie, and the browser binding that refuses a callback completed in a
// different browser than the one that started the flow.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

const (
	browserOIDCProvider = "PocketID"
	browserOIDCPassword = "correct-horse-battery-staple"
)

// oidcBrowser is one user agent: its own cookie jar, redirects not
// followed so each hop can be asserted.
type oidcBrowser struct {
	t      *testing.T
	base   string
	client *http.Client
	jar    *cookiejar.Jar
}

func newOIDCBrowser(t *testing.T, sh *sessionHarness) *oidcBrowser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &oidcBrowser{
		t:    t,
		base: sh.baseURL,
		jar:  jar,
		client: &http.Client{
			Transport: sh.client.Transport,
			Jar:       jar,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (b *oidcBrowser) cookie(name string) string {
	u, _ := url.Parse(b.base + "/")
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (b *oidcBrowser) do(method, rawURL string, body any) (*http.Response, []byte) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	if strings.HasPrefix(rawURL, "/") {
		rawURL = b.base + rawURL
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		b.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := b.cookie("herold_public_csrf"); tok != "" && method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", tok)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res, raw
}

// authURL starts a flow (sign-in or link) and returns the provider
// authorization URL the SPA would navigate to.
func (b *oidcBrowser) authURL(path string) string {
	b.t.Helper()
	res, raw := b.do(http.MethodPost, path, map[string]string{"provider_id": browserOIDCProvider})
	if res.StatusCode != http.StatusOK {
		b.t.Fatalf("POST %s: status=%d body=%s", path, res.StatusCode, raw)
	}
	var out struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AuthURL == "" {
		b.t.Fatalf("POST %s: no auth_url in %s", path, raw)
	}
	return out.AuthURL
}

// providerRedirect simulates the user approving at the provider and
// returns the callback URL the provider redirects the browser to.
func providerRedirect(t *testing.T, authURL string) string {
	t.Helper()
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("provider authorize: %v", err)
	}
	res.Body.Close()
	loc, err := res.Location()
	if err != nil {
		t.Fatalf("provider authorize: status=%d, no redirect: %v", res.StatusCode, err)
	}
	return loc.String()
}

// oidcBrowserEnv is a session harness with a non-admin principal that has
// a password and a PKCE-requiring fake provider registered under a
// secret reference.
type oidcBrowserEnv struct {
	sh    *sessionHarness
	stub  *fakeoidc.Server
	email string
	pid   uint64
}

func newOIDCBrowserEnv(t *testing.T) *oidcBrowserEnv {
	t.Helper()
	sh := newSessionHarness(t)
	ctx := context.Background()
	const email = "alice@example.test"
	pid, err := createLocalPrincipal(t, sh, email)
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-suite", ClientSecret: "pocket-secret", RequirePKCE: true})
	secretFile := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secretFile, []byte("pocket-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if _, err := sh.rp.AddProvider(ctx, directoryoidc.ProviderConfig{
		Name:            browserOIDCProvider,
		IssuerURL:       stub.IssuerURL(),
		ClientID:        "herold-suite",
		ClientSecretRef: "file:" + secretFile,
		Scopes:          []string{"email", "profile"},
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	return &oidcBrowserEnv{sh: sh, stub: stub, email: email, pid: uint64(pid)}
}

// passwordLogin signs b in with the principal's password.
func (e *oidcBrowserEnv) passwordLogin(b *oidcBrowser) {
	b.t.Helper()
	res, raw := b.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": e.email, "password": browserOIDCPassword,
	})
	if res.StatusCode != http.StatusOK {
		b.t.Fatalf("password login: status=%d body=%s", res.StatusCode, raw)
	}
}

// link runs the settings link flow in b (already signed in).
func (e *oidcBrowserEnv) link(t *testing.T, b *oidcBrowser) {
	t.Helper()
	authURL := b.authURL("/api/v1/principals/" + pidStr(e.pid) + "/oidc-links/begin")
	if got := mustParse(t, authURL).Query().Get("redirect_uri"); got != b.base+"/api/v1/oidc/callback" {
		t.Fatalf("link redirect_uri = %q, want %q", got, b.base+"/api/v1/oidc/callback")
	}
	res, _ := b.do(http.MethodGet, providerRedirect(t, authURL), nil)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("link callback: status=%d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/#/settings/security?oidc_link=linked" {
		t.Fatalf("link callback Location = %q", loc)
	}
}

// createLocalPrincipal registers example.test as a local domain (once)
// and creates a password principal in it.
func createLocalPrincipal(t *testing.T, sh *sessionHarness, email string) (store.PrincipalID, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := sh.h.Store.Meta().GetDomain(ctx, "example.test"); err != nil {
		if err := sh.h.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
			t.Fatalf("InsertDomain: %v", err)
		}
	}
	return sh.dir.CreatePrincipal(ctx, email, browserOIDCPassword)
}

func pidStr(n uint64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func (b *oidcBrowser) mePrincipalID() (int, uint64) {
	b.t.Helper()
	res, raw := b.do(http.MethodGet, "/api/v1/auth/me", nil)
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, 0
	}
	var me struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	_ = json.Unmarshal(raw, &me)
	return res.StatusCode, me.PrincipalID
}

func TestOIDCBrowser_SignInProvidersListedUnauthenticated(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	b := newOIDCBrowser(t, e.sh)
	res, raw := b.do(http.MethodGet, "/api/v1/auth/oidc/providers", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	var out struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != browserOIDCProvider || out.Items[0].Name != browserOIDCProvider {
		t.Fatalf("items = %+v", out.Items)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "issuer") {
		t.Fatalf("public provider list exposes configuration: %s", raw)
	}
}

// TestOIDCBrowser_LinkThenSignIn is the acceptance path: a password user
// links the provider from settings, then a fresh browser signs in through
// the provider and holds a session for the same principal.
func TestOIDCBrowser_LinkThenSignIn(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	e.stub.SetIdentity(fakeoidc.Identity{Subject: "pocket-uuid-1", Email: "alice@private.example", EmailVerified: true})

	settings := newOIDCBrowser(t, e.sh)
	e.passwordLogin(settings)
	e.link(t, settings)

	res, raw := settings.do(http.MethodGet, "/api/v1/principals/"+pidStr(e.pid)+"/oidc-links", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list links: status=%d body=%s", res.StatusCode, raw)
	}
	var links struct {
		Items []struct {
			ProviderID      string `json:"provider_id"`
			Subject         string `json:"subject"`
			EmailAtProvider string `json:"email_at_provider"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &links)
	if len(links.Items) != 1 || links.Items[0].ProviderID != browserOIDCProvider ||
		links.Items[0].Subject != "pocket-uuid-1" || links.Items[0].EmailAtProvider != "alice@private.example" {
		t.Fatalf("links = %+v", links.Items)
	}

	fresh := newOIDCBrowser(t, e.sh)
	if status, _ := fresh.mePrincipalID(); status != http.StatusUnauthorized {
		t.Fatalf("fresh browser /auth/me before sign-in: status=%d, want 401", status)
	}
	authURL := fresh.authURL("/api/v1/auth/oidc/begin")
	if got := mustParse(t, authURL).Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	res, _ = fresh.do(http.MethodGet, providerRedirect(t, authURL), nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("sign-in callback: status=%d Location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	status, pid := fresh.mePrincipalID()
	if status != http.StatusOK || pid != e.pid {
		t.Fatalf("/auth/me after OIDC sign-in: status=%d pid=%d, want 200 pid=%d", status, pid, e.pid)
	}
	// The session is an ordinary end-user session: CSRF-protected writes work.
	if fresh.cookie("herold_public_csrf") == "" {
		t.Fatal("OIDC sign-in did not set the CSRF cookie")
	}

	// Password sign-in is unaffected by the link.
	again := newOIDCBrowser(t, e.sh)
	e.passwordLogin(again)
	if status, pid := again.mePrincipalID(); status != http.StatusOK || pid != e.pid {
		t.Fatalf("password sign-in after link: status=%d pid=%d", status, pid)
	}

	// Unlink, and the provider no longer signs the user in.
	res, raw = settings.do(http.MethodDelete, "/api/v1/principals/"+pidStr(e.pid)+"/oidc-links/"+browserOIDCProvider, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink: status=%d body=%s", res.StatusCode, raw)
	}
	late := newOIDCBrowser(t, e.sh)
	res, _ = late.do(http.MethodGet, providerRedirect(t, late.authURL("/api/v1/auth/oidc/begin")), nil)
	if loc := res.Header.Get("Location"); loc != "/?oidc_error=not_linked" {
		t.Fatalf("sign-in after unlink: Location=%q", loc)
	}
	if status, _ := late.mePrincipalID(); status != http.StatusUnauthorized {
		t.Fatalf("sign-in after unlink yielded a session (status %d)", status)
	}
}

func TestOIDCBrowser_UnlinkedIdentityRefused(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	e.stub.SetIdentity(fakeoidc.Identity{Subject: "stranger", Email: "alice@example.test", EmailVerified: true})
	b := newOIDCBrowser(t, e.sh)
	res, _ := b.do(http.MethodGet, providerRedirect(t, b.authURL("/api/v1/auth/oidc/begin")), nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/?oidc_error=not_linked" {
		t.Fatalf("status=%d Location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	if b.cookie("herold_public_session") != "" {
		t.Fatal("unlinked identity got a session cookie (matching must be by sub, never by email)")
	}
}

// TestOIDCBrowser_CallbackInOtherBrowserRefused: a callback URL from a
// flow started in browser A, opened in browser B, neither signs B in nor
// consumes A's pending flow.
func TestOIDCBrowser_CallbackInOtherBrowserRefused(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	e.stub.SetIdentity(fakeoidc.Identity{Subject: "pocket-uuid-2", Email: "a@private.example", EmailVerified: true})
	settings := newOIDCBrowser(t, e.sh)
	e.passwordLogin(settings)
	e.link(t, settings)

	attacker := newOIDCBrowser(t, e.sh)
	callback := providerRedirect(t, attacker.authURL("/api/v1/auth/oidc/begin"))

	victim := newOIDCBrowser(t, e.sh)
	res, _ := victim.do(http.MethodGet, callback, nil)
	if loc := res.Header.Get("Location"); loc != "/?oidc_error=browser_mismatch" {
		t.Fatalf("foreign callback: Location=%q", loc)
	}
	if status, _ := victim.mePrincipalID(); status != http.StatusUnauthorized {
		t.Fatalf("foreign callback produced a session (status %d)", status)
	}

	res, _ = attacker.do(http.MethodGet, callback, nil)
	if res.Header.Get("Location") != "/" {
		t.Fatalf("originating browser after refused replay: Location=%q", res.Header.Get("Location"))
	}
}

// TestOIDCBrowser_LinkCallbackInOtherBrowserRefused: an attacker cannot
// attach a victim's provider identity to the attacker's principal by
// handing the victim the attacker's link authorization URL.
func TestOIDCBrowser_LinkCallbackInOtherBrowserRefused(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	attacker := newOIDCBrowser(t, e.sh)
	e.passwordLogin(attacker)
	authURL := attacker.authURL("/api/v1/principals/" + pidStr(e.pid) + "/oidc-links/begin")

	victim := newOIDCBrowser(t, e.sh)
	e.stub.SetIdentity(fakeoidc.Identity{Subject: "victim-sub"})
	res, _ := victim.do(http.MethodGet, providerRedirect(t, authURL), nil)
	if loc := res.Header.Get("Location"); loc != "/#/settings/security?oidc_link=browser_mismatch" {
		t.Fatalf("Location=%q", loc)
	}
	links, err := e.sh.rp.ListProviders(context.Background())
	if err != nil || len(links) != 1 {
		t.Fatalf("ListProviders: %v %v", links, err)
	}
	res2, raw := attacker.do(http.MethodGet, "/api/v1/principals/"+pidStr(e.pid)+"/oidc-links", nil)
	if res2.StatusCode != http.StatusOK || strings.Contains(string(raw), "victim-sub") {
		t.Fatalf("link was created from a foreign browser: %s", raw)
	}
}

func TestOIDCBrowser_ProviderDeniedConsent(t *testing.T) {
	t.Parallel()
	e := newOIDCBrowserEnv(t)
	b := newOIDCBrowser(t, e.sh)
	authURL := b.authURL("/api/v1/auth/oidc/begin")
	state := mustParse(t, authURL).Query().Get("state")
	res, _ := b.do(http.MethodGet, "/api/v1/oidc/callback?error=access_denied&state="+url.QueryEscape(state), nil)
	if loc := res.Header.Get("Location"); loc != "/?oidc_error=cancelled" {
		t.Fatalf("Location=%q", loc)
	}
}

// TestOIDCBrowser_ProviderLoadedFromStore covers a restarted server: the
// provider rows exist in the store but nothing is in the RP's memory. A
// provider registered with a secret reference is listed and signs a
// linked user in; one registered with an inline secret (not persisted)
// is withheld from the sign-in page and refused with 503.
func TestOIDCBrowser_ProviderLoadedFromStore(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	ctx := context.Background()
	pid, err := createLocalPrincipal(t, sh, "bob@example.test")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-suite", ClientSecret: "pocket-secret", RequirePKCE: true})
	secretFile := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secretFile, []byte("pocket-secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	meta := sh.h.Store.Meta()
	for _, row := range []store.OIDCProvider{
		{Name: browserOIDCProvider, IssuerURL: stub.IssuerURL(), ClientID: "herold-suite",
			ClientSecretRef: "file:" + secretFile, Scopes: []string{"openid", "email"}},
		{Name: "legacy", IssuerURL: stub.IssuerURL(), ClientID: "herold-suite",
			ClientSecretRef: "inline:legacy", Scopes: []string{"openid"}},
	} {
		if err := meta.InsertOIDCProvider(ctx, row); err != nil {
			t.Fatalf("InsertOIDCProvider %s: %v", row.Name, err)
		}
	}
	if err := meta.LinkOIDC(ctx, store.OIDCLink{PrincipalID: pid, ProviderName: browserOIDCProvider, Subject: "bob-sub"}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	stub.SetIdentity(fakeoidc.Identity{Subject: "bob-sub"})

	b := newOIDCBrowser(t, sh)
	res, raw := b.do(http.MethodGet, "/api/v1/auth/oidc/providers", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), browserOIDCProvider) || strings.Contains(string(raw), "legacy") {
		t.Fatalf("provider list: status=%d body=%s", res.StatusCode, raw)
	}
	res, raw = b.do(http.MethodPost, "/api/v1/auth/oidc/begin", map[string]string{"provider_id": "legacy"})
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("begin with inline-secret provider: status=%d body=%s, want 503", res.StatusCode, raw)
	}

	res, _ = b.do(http.MethodGet, providerRedirect(t, b.authURL("/api/v1/auth/oidc/begin")), nil)
	if res.Header.Get("Location") != "/" {
		t.Fatalf("sign-in via store-loaded provider: Location=%q", res.Header.Get("Location"))
	}
	if status, got := b.mePrincipalID(); status != http.StatusOK || got != uint64(pid) {
		t.Fatalf("/auth/me: status=%d pid=%d want %d", status, got, pid)
	}
}
