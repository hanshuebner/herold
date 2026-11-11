package admin

// oidc_suite_signin_test.go drives the Suite's external-OIDC flows
// through the fully booted server's public listener, the way a browser
// reaches a deployment: the operator registers the provider with the
// documented REST call (secret as a file reference), an end user links
// the provider from settings, and a fresh browser then signs in through
// it. The redirect_uri asserted here is the one an operator registers at
// the provider: <public base URL>/api/v1/oidc/callback, where the public
// base URL defaults to https://<server.hostname>.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

type suiteBrowser struct {
	t      *testing.T
	base   string
	jar    *cookiejar.Jar
	client *http.Client
}

func newSuiteBrowser(t *testing.T, publicAddr string) *suiteBrowser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &suiteBrowser{
		t:    t,
		base: "http://" + publicAddr,
		jar:  jar,
		client: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (b *suiteBrowser) do(method, path string, body any) (*http.Response, []byte) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.base+path, rdr)
	if err != nil {
		b.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	u, _ := url.Parse(b.base + "/")
	for _, c := range b.jar.Cookies(u) {
		if c.Name == "herold_public_csrf" && method != http.MethodGet {
			req.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res, raw
}

// start begins a flow and returns the provider authorization URL.
func (b *suiteBrowser) start(path string) *url.URL {
	b.t.Helper()
	res, raw := b.do(http.MethodPost, path, map[string]string{"provider_id": "PocketID"})
	if res.StatusCode != http.StatusOK {
		b.t.Fatalf("POST %s: status=%d body=%s", path, res.StatusCode, raw)
	}
	var out struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(raw, &out)
	u, err := url.Parse(out.AuthURL)
	if err != nil || out.AuthURL == "" {
		b.t.Fatalf("POST %s: bad auth_url in %s", path, raw)
	}
	return u
}

// approveAndReturn has the provider approve the request and follows its
// redirect back to the callback on this listener. The provider redirects
// to the registered public URL (https://test.local/...), which in a
// deployment the reverse proxy maps to this listener; the test sends the
// same path and query straight to the listener.
func (b *suiteBrowser) approveAndReturn(authURL *url.URL) *http.Response {
	b.t.Helper()
	cb := fakeoidc.FollowAuthorizeURL(b.t, authURL.String())
	if cb.Scheme+"://"+cb.Host+cb.Path != "https://test.local/api/v1/oidc/callback" {
		b.t.Fatalf("provider redirected to %s, want https://test.local/api/v1/oidc/callback", cb)
	}
	res, _ := b.do(http.MethodGet, cb.Path+"?"+cb.RawQuery, nil)
	return res
}

func TestSuiteOIDC_RegisterLinkSignIn_FullServer(t *testing.T) {
	addrs, done, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	_, apiKey, _, _ := bootstrapAndGetAPIKey(t, addrs["admin"])

	stub := fakeoidc.New(t, fakeoidc.Options{ClientID: "herold-mail", ClientSecret: "pocket-id-secret", RequirePKCE: true})
	secretFile := filepath.Join(t.TempDir(), "pocketid-client-secret")
	if err := os.WriteFile(secretFile, []byte("pocket-id-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	apiCall := func(method, path string, body any) (int, []byte) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, "http://"+publicAddr+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, out
	}

	// The operator's registration call, as documented in
	// docs/manual/admin/administer.mdoc.
	if status, raw := apiCall(http.MethodPost, "/api/v1/oidc/providers", map[string]any{
		"name":              "PocketID",
		"issuer":            stub.IssuerURL(),
		"client_id":         "herold-mail",
		"client_secret_ref": "file:" + secretFile,
		"scopes":            []string{"openid", "email", "profile"},
	}); status != http.StatusCreated {
		t.Fatalf("register provider: status=%d body=%s", status, raw)
	}

	const email, password = "carol@example.com", "correct-horse-battery-staple"
	status, raw := apiCall(http.MethodPost, "/api/v1/principals", map[string]any{"email": email, "password": password})
	if status != http.StatusCreated {
		t.Fatalf("create principal: status=%d body=%s", status, raw)
	}
	var created struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(raw, &created)

	// The sign-in page lists the provider without a session.
	anon := newSuiteBrowser(t, publicAddr)
	res, raw := anon.do(http.MethodGet, "/api/v1/auth/oidc/providers", nil)
	if res.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"PocketID"`)) {
		t.Fatalf("provider list: status=%d body=%s", res.StatusCode, raw)
	}

	// Link from settings after a password sign-in.
	stub.SetIdentity(fakeoidc.Identity{Subject: "6d0d1f1e-pocket-uuid", Email: "carol@private.example", EmailVerified: true})
	settings := newSuiteBrowser(t, publicAddr)
	if res, raw := settings.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password}); res.StatusCode != http.StatusOK {
		t.Fatalf("password login: status=%d body=%s", res.StatusCode, raw)
	}
	res = settings.approveAndReturn(settings.start(fmt.Sprintf("/api/v1/principals/%d/oidc-links/begin", created.ID)))
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusFound || loc != "/#/settings/security?oidc_link=linked" {
		t.Fatalf("link callback: status=%d Location=%q", res.StatusCode, loc)
	}

	// A fresh browser signs in through the provider.
	fresh := newSuiteBrowser(t, publicAddr)
	res = fresh.approveAndReturn(fresh.start("/api/v1/auth/oidc/begin"))
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusFound || loc != "/" {
		t.Fatalf("sign-in callback: status=%d Location=%q", res.StatusCode, loc)
	}
	res, raw = fresh.do(http.MethodGet, "/api/v1/auth/me", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/auth/me after OIDC sign-in: status=%d body=%s", res.StatusCode, raw)
	}
	var me struct {
		PrincipalID uint64 `json:"principal_id"`
		Email       string `json:"email"`
	}
	_ = json.Unmarshal(raw, &me)
	if me.PrincipalID != created.ID || me.Email != email {
		t.Fatalf("/auth/me = %+v, want principal %d %s", me, created.ID, email)
	}
	// The session reaches JMAP, like a password session does.
	res, raw = fresh.do(http.MethodGet, "/.well-known/jmap", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("JMAP session with OIDC cookie: status=%d body=%s", res.StatusCode, raw)
	}
}
