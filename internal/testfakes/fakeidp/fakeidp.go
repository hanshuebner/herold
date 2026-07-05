// Package fakeidp is a deterministic OAuth 2.0 / OIDC authorization server
// for tests and development. It lets herold's external-identity OAuth flow
// (per-user token acquisition and the XOAUTH2 SMTP-submission path) be
// exercised without any external network or a real provider.
//
// The server implements two endpoints:
//
//   - GET  /authorize — validates client_id + redirect_uri + state and
//     302-redirects back to redirect_uri with a fresh authorization
//     code and the caller's state echoed verbatim.
//   - POST /token — exchanges an authorization code for an access token,
//     a refresh token, and an expiry (grant_type=authorization_code), and
//     also honours grant_type=refresh_token so re-authentication can be
//     tested.
//
// Every identifier (code, access token, refresh token) is derived from a
// monotonic counter — there is no crypto/rand and no wall-clock read on
// any request path, so a run is fully reproducible. Time is injected via
// Options.Now and only feeds the reported expires_in.
//
// For tests, use New which registers cleanup on testing.TB. For standalone
// dev-tooling that runs outside a test, use NewServer and call Close when done.
package fakeidp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Token is a set of credentials issued by the /token endpoint.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// Options configures a Server. The zero value is usable: it yields a
// provider with a default client_id/secret, a real-clock Now, and a
// one-hour access-token lifetime.
type Options struct {
	// ClientID is the OAuth client_id the provider expects. Defaults to
	// "fakeidp-client".
	ClientID string
	// ClientSecret is the OAuth client_secret the provider expects on the
	// token endpoint. Defaults to "fakeidp-secret".
	ClientSecret string
	// Now injects the clock used to compute token expiry. Defaults to
	// time.Now. It is never used for entropy — identifiers are counter-based.
	Now func() time.Time
	// AccessTTL is the lifetime reported as expires_in for issued access
	// tokens. Defaults to one hour.
	AccessTTL time.Duration
}

// Server is a running fake authorization server backed by httptest.
type Server struct {
	ts           *httptest.Server
	clientID     string
	clientSecret string
	now          func() time.Time
	accessTTL    time.Duration

	mu      sync.Mutex
	counter int
	codes   map[string]codeRecord // authorization code -> record
	refresh map[string]bool       // valid refresh tokens
}

type codeRecord struct {
	redirectURI string
	scope       string
}

// NewServer starts a fake IdP HTTP server and returns it. The server is
// reachable at BaseURL until Close is called. For test code that wants
// automatic cleanup, use New instead.
func NewServer(opts Options) *Server {
	if opts.ClientID == "" {
		opts.ClientID = "fakeidp-client"
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = "fakeidp-secret"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AccessTTL == 0 {
		opts.AccessTTL = time.Hour
	}
	s := &Server{
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		now:          opts.Now,
		accessTTL:    opts.AccessTTL,
		codes:        make(map[string]codeRecord),
		refresh:      make(map[string]bool),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	s.ts = httptest.NewServer(mux)
	return s
}

// Close stops the fake IdP server.
func (s *Server) Close() { s.ts.Close() }

// New starts a fake IdP and registers cleanup on t. The provider is
// reachable at BaseURL until the test finishes.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s := NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// BaseURL is the provider's root URL (e.g. "http://127.0.0.1:53312").
func (s *Server) BaseURL() string { return s.ts.URL }

// AuthorizeURL is the absolute /authorize endpoint URL.
func (s *Server) AuthorizeURL() string { return s.ts.URL + "/authorize" }

// TokenURL is the absolute /token endpoint URL.
func (s *Server) TokenURL() string { return s.ts.URL + "/token" }

// ClientID is the client_id the provider expects.
func (s *Server) ClientID() string { return s.clientID }

// ClientSecret is the client_secret the provider expects.
func (s *Server) ClientSecret() string { return s.clientSecret }

// next returns the next monotonic counter value under the lock.
func (s *Server) nextLocked() int {
	s.counter++
	return s.counter
}

// handleAuthorize implements GET /authorize. It validates response_type,
// client_id, and redirect_uri, mints an authorization code, and redirects
// back to redirect_uri carrying the code and the caller's state verbatim.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if rt := q.Get("response_type"); rt != "" && rt != "code" {
		http.Error(w, "unsupported_response_type", http.StatusBadRequest)
		return
	}
	if q.Get("client_id") != s.clientID {
		http.Error(w, "invalid_client", http.StatusUnauthorized)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		http.Error(w, "invalid_request: redirect_uri required", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid_request: bad redirect_uri", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	code := "code-" + strconv.Itoa(s.nextLocked())
	s.codes[code] = codeRecord{redirectURI: redirectURI, scope: q.Get("scope")}
	s.mu.Unlock()

	rq := u.Query()
	rq.Set("code", code)
	if state := q.Get("state"); state != "" {
		rq.Set("state", state)
	}
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken implements POST /token for the authorization_code and
// refresh_token grants.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	// The client_secret may arrive either as a form field or via HTTP
	// Basic auth (golang.org/x/oauth2 defaults to Basic). Accept both.
	clientSecret := r.Form.Get("client_secret")
	if _, bp, ok := r.BasicAuth(); ok && clientSecret == "" {
		clientSecret = bp
	}
	if clientSecret != s.clientSecret {
		writeTokenError(w, http.StatusUnauthorized, "invalid_client", "bad client_secret")
		return
	}

	switch r.Form.Get("grant_type") {
	case "authorization_code":
		code := r.Form.Get("code")
		s.mu.Lock()
		rec, ok := s.codes[code]
		if ok {
			delete(s.codes, code) // authorization codes are single-use
		}
		if !ok {
			s.mu.Unlock()
			writeTokenError(w, http.StatusBadRequest, "invalid_grant", "unknown or used code")
			return
		}
		n := s.nextLocked()
		access := "access-" + strconv.Itoa(n)
		refresh := "refresh-" + strconv.Itoa(n)
		s.refresh[refresh] = true
		s.mu.Unlock()
		s.writeToken(w, access, refresh, rec.scope)

	case "refresh_token":
		rt := r.Form.Get("refresh_token")
		s.mu.Lock()
		ok := s.refresh[rt]
		if !ok {
			s.mu.Unlock()
			writeTokenError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh_token")
			return
		}
		n := s.nextLocked()
		access := "access-" + strconv.Itoa(n)
		// The refresh token remains valid across refreshes (providers
		// typically keep it stable), so re-auth can be exercised repeatedly.
		s.mu.Unlock()
		s.writeToken(w, access, rt, r.Form.Get("scope"))

	default:
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

// writeToken emits a successful token response.
func (s *Server) writeToken(w http.ResponseWriter, access, refresh, scope string) {
	tok := map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    int(s.accessTTL.Seconds()),
	}
	if scope != "" {
		tok["scope"] = scope
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(tok)
}

func writeTokenError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Authenticate performs the full authorization-code dance in-process and
// returns the issued token set. It drives GET /authorize (asserting the
// redirect echoes state and carries a code) and then exchanges the code at
// POST /token. It exercises both endpoints exactly as a real relying party
// would, without a browser.
func (s *Server) Authenticate(t testing.TB, redirectURI, state, scope string) Token {
	t.Helper()

	// Step 1: hit /authorize and capture the redirect (do not follow it).
	authURL, _ := url.Parse(s.AuthorizeURL())
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", s.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL.RawQuery = q.Encode()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL.String())
	if err != nil {
		t.Fatalf("fakeidp: GET /authorize: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("fakeidp: /authorize status = %d; want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("fakeidp: bad Location header: %v", err)
	}
	if got := loc.Query().Get("state"); got != state {
		t.Fatalf("fakeidp: state mismatch: got %q want %q", got, state)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("fakeidp: /authorize redirect carried no code: %s", resp.Header.Get("Location"))
	}

	// Step 2: exchange the code for tokens.
	return s.exchange(t, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
	})
}

// RevokeRefreshToken removes refreshToken from the server's valid set so that
// any subsequent grant_type=refresh_token request using it returns
// invalid_grant (HTTP 400). This simulates a provider revoking a refresh
// token after the user has completed a new full OAuth authorization (re #131).
func (s *Server) RevokeRefreshToken(refreshToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refresh, refreshToken)
}

// Refresh exchanges a refresh token for a fresh access token, exercising
// the refresh_token grant (the re-authentication path).
func (s *Server) Refresh(t testing.TB, refreshToken string) Token {
	t.Helper()
	return s.exchange(t, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
	})
}

func (s *Server) exchange(t testing.TB, form url.Values) Token {
	t.Helper()
	resp, err := http.PostForm(s.TokenURL(), form)
	if err != nil {
		t.Fatalf("fakeidp: POST /token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fakeidp: /token status = %d", resp.StatusCode)
	}
	var tok Token
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		t.Fatalf("fakeidp: decode token: %v", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		t.Fatalf("fakeidp: token response carried no access_token")
	}
	return tok
}
