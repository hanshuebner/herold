// Package fakeoidc is a deterministic OpenID Connect provider for tests
// and development. It speaks exactly the subset of OIDC that go-oidc's
// oidc.NewProvider + Verifier require -- discovery at
// /.well-known/openid-configuration, JWKS, a /authorize endpoint that
// mints a code and redirects, and a /token endpoint that returns a
// signed RS256 ID token -- so internal/directoryoidc.RP's relying-party
// code path (including REQ-AUTH-56 first-login auto-provisioning) can be
// exercised end to end without any external network or a real IdP.
//
// Every login carries an Identity (subject, email, email_verified, name,
// and arbitrary extra claims such as epic #188's "groups"). SetIdentity
// changes the default used by the next /authorize call that does not
// override it via query parameters (?sub=&email=&name=&email_verified=),
// which lets a human at a browser (or scripts/dev-instance.sh) drive a
// login for an address of their choosing without recompiling anything.
//
// For tests, use New which registers cleanup on testing.TB. For
// standalone dev-tooling that runs outside a test (cmd/heroldfakeoidc),
// use NewServer and call Close when done.
package fakeoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Identity is the set of claims a login asserts.
type Identity struct {
	// Subject is the OIDC `sub` claim -- the provider-unique identifier
	// internal/directoryoidc matches links and auto-provisioning
	// decisions on.
	Subject string
	// Email is the `email` claim.
	Email string
	// EmailVerified is the `email_verified` claim. REQ-AUTH-56
	// auto-provisioning refuses any login where this is not true.
	EmailVerified bool
	// Name is the `name` claim (the display-name source for
	// auto-provisioning).
	Name string
	// Extra is merged into the ID token's claim set on top of the
	// standard claims above -- e.g. epic #188's "groups"/"roles" claims
	// for claim-to-grant mapping tests.
	Extra map[string]any
}

// defaultIdentity is used by /authorize whenever neither SetIdentity nor
// a per-request query-parameter override supplies one. It names a
// syntactically valid, pre-verified address so a bare click-through (no
// query parameters at all) always produces a working login -- useful for
// a maintainer poking at scripts/dev-instance.sh's registered provider.
var defaultIdentity = Identity{
	Subject:       "dev-user",
	Email:         "dev-user@fakeoidc.local",
	EmailVerified: true,
	Name:          "Dev User",
}

// Options configures a Server.
type Options struct {
	// ClientID is the OAuth client_id the provider expects. Defaults to
	// "fakeoidc-client".
	ClientID string
	// ClientSecret is the OAuth client_secret the provider expects on
	// the token endpoint. Defaults to "fakeoidc-secret".
	ClientSecret string
}

// Server is a running fake OIDC provider backed by httptest.
type Server struct {
	ts           *httptest.Server
	clientID     string
	clientSecret string
	key          *rsa.PrivateKey
	kid          string
	issuer       string

	mu       sync.Mutex
	counter  int
	def      Identity
	authReqs map[string]authRequest // code -> pending request
}

// authRequest is the state captured at /authorize time and consumed at
// /token time.
type authRequest struct {
	redirectURI string
	nonce       string
	identity    Identity
}

// NewServer starts a fake OIDC provider and returns it. The server is
// reachable at BaseURL/IssuerURL until Close is called. For test code
// that wants automatic cleanup, use New instead.
func NewServer(opts Options) *Server {
	if opts.ClientID == "" {
		opts.ClientID = "fakeoidc-client"
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = "fakeoidc-secret"
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("fakeoidc: generate key: " + err.Error())
	}
	s := &Server{
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		key:          key,
		kid:          "fakeoidc-key",
		def:          defaultIdentity,
		authReqs:     make(map[string]authRequest),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/jwks", s.handleJWKS)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	s.ts = httptest.NewServer(mux)
	s.issuer = s.ts.URL
	return s
}

// New starts a fake OIDC provider and registers cleanup on t.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s := NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// Close stops the fake provider.
func (s *Server) Close() { s.ts.Close() }

// BaseURL is the provider's root URL, equal to IssuerURL.
func (s *Server) BaseURL() string { return s.ts.URL }

// IssuerURL is the OIDC issuer URL to register with
// directoryoidc.RP.AddProvider (discovery is served at
// IssuerURL + "/.well-known/openid-configuration").
func (s *Server) IssuerURL() string { return s.issuer }

// ClientID is the client_id the provider expects.
func (s *Server) ClientID() string { return s.clientID }

// ClientSecret is the client_secret the provider expects.
func (s *Server) ClientSecret() string { return s.clientSecret }

// SetIdentity changes the default identity asserted by /authorize
// requests that do not override individual fields via query parameters.
// Tests call this before driving a BeginLink/BeginSignIn round-trip to
// control the sub/email/claims of that specific login.
func (s *Server) SetIdentity(id Identity) {
	s.mu.Lock()
	s.def = id
	s.mu.Unlock()
}

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/authorize",
		"token_endpoint":                        s.issuer + "/token",
		"jwks_uri":                              s.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	n := s.key.PublicKey.N
	e := big.NewInt(int64(s.key.PublicKey.E))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"kid": s.kid,
			"n":   base64.RawURLEncoding.EncodeToString(n.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(e.Bytes()),
		}},
	})
}

// handleAuthorize validates client_id + redirect_uri, computes this
// request's identity (the current default, overridden field-by-field by
// any of ?sub=&email=&name=&email_verified= present on the request),
// mints a single-use code bound to it, and redirects back to
// redirect_uri carrying ?code=&state=.
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
	id := s.def
	if v := q.Get("sub"); v != "" {
		id.Subject = v
	}
	if v := q.Get("email"); v != "" {
		id.Email = v
	}
	if v := q.Get("name"); v != "" {
		id.Name = v
	}
	if v := q.Get("email_verified"); v != "" {
		id.EmailVerified = v == "true" || v == "1"
	}
	s.counter++
	code := "code-" + strconv.Itoa(s.counter)
	s.authReqs[code] = authRequest{
		redirectURI: redirectURI,
		nonce:       q.Get("nonce"),
		identity:    id,
	}
	s.mu.Unlock()

	rq := u.Query()
	rq.Set("code", code)
	if state := q.Get("state"); state != "" {
		rq.Set("state", state)
	}
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken implements POST /token for grant_type=authorization_code,
// returning a signed RS256 ID token bound to the code's identity.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.Form.Get("code")
	s.mu.Lock()
	req, ok := s.authReqs[code]
	if ok {
		delete(s.authReqs, code) // codes are single-use
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid_grant: unknown or used code", http.StatusBadRequest)
		return
	}

	idTok, err := s.signIDToken(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": idTok,
		"token_type":   "Bearer",
		"id_token":     idTok,
		"expires_in":   3600,
	})
}

func (s *Server) signIDToken(req authRequest) (string, error) {
	header := map[string]any{
		"alg": "RS256",
		"kid": s.kid,
		"typ": "JWT",
	}
	now := time.Now().Unix()
	payload := map[string]any{
		"iss":            s.issuer,
		"sub":            req.identity.Subject,
		"aud":            s.clientID,
		"iat":            now,
		"exp":            now + 3600,
		"nonce":          req.nonce,
		"email":          req.identity.Email,
		"email_verified": req.identity.EmailVerified,
	}
	if req.identity.Name != "" {
		payload["name"] = req.identity.Name
	}
	for k, v := range req.identity.Extra {
		payload[k] = v
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signingInput := enc(hb) + "." + enc(pb)
	h := sha256.New()
	h.Write([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, 5 /* crypto.SHA256 */, h.Sum(nil))
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc(sig), nil
}

// FollowAuthorize simulates a user-agent hitting the provider's
// /authorize URL and returns the ?code / ?state values the provider
// appended to the redirect. redirectPrefix, when non-empty, must be a
// prefix of the redirect Location (a defence-in-depth sanity check for
// callers that know their own redirect_uri).
func FollowAuthorize(t testing.TB, authURL, redirectPrefix string) (code, state string) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("fakeoidc: follow authorize: %v", err)
	}
	defer resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("fakeoidc: no redirect (status=%d): %v", resp.StatusCode, err)
	}
	if redirectPrefix != "" && !strings.HasPrefix(loc.String(), redirectPrefix) {
		t.Fatalf("fakeoidc: unexpected redirect target: %s", loc)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}
