package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubServer is an in-process ACME server (RFC 8555) that drives the
// client through the happy path and a few error injections. The server
// is wired with an httptest.Server; the client points its
// DirectoryURL at the test server's URL+"/dir".
//
// Only enough of the spec is implemented to drive the four test
// scenarios: HTTP-01, TLS-ALPN-01, DNS-01, resume + retry. State
// transitions are crisp (single-step) so tests do not need to advance
// a fake clock to flip authorisation status.
type stubServer struct {
	t           *testing.T
	mu          sync.Mutex
	srv         *httptest.Server
	caKey       *rsa.PrivateKey
	caCert      *x509.Certificate
	caCertDER   []byte
	accountKID  int
	orders      map[string]*stubOrder
	orderByID   int
	authzs      map[string]*stubAuthz
	authzByID   int
	jwsErrors   int
	httpFetcher func(token string) (string, error) // optional HTTP-01 validator
	failOrder   bool                               // injects 500 on newOrder
}

type stubOrder struct {
	ID          string
	Status      string
	URL         string
	Authzs      []string
	Finalize    string
	Certificate string
	Identifiers []orderIdentifier
	CertPEM     string
}

type stubAuthz struct {
	ID         string
	URL        string
	Status     string
	Identifier orderIdentifier
	Token      string
	ChalURLs   map[string]string // type -> URL
	ChalStatus map[string]string
}

func newStubServer(t *testing.T) *stubServer {
	s := &stubServer{
		t:         t,
		orders:    make(map[string]*stubOrder),
		authzs:    make(map[string]*stubAuthz),
		orderByID: 1,
		authzByID: 1,
	}
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stub ACME CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("stub: build CA: %v", err)
	}
	caCert, _ := x509.ParseCertificate(der)
	s.caKey = caKey
	s.caCert = caCert
	s.caCertDER = der
	mux := http.NewServeMux()
	s.srv = httptest.NewServer(mux)
	mux.HandleFunc("/dir", s.handleDirectory)
	mux.HandleFunc("/new-nonce", s.handleNewNonce)
	mux.HandleFunc("/new-account", s.handleNewAccount)
	mux.HandleFunc("/new-order", s.handleNewOrder)
	mux.HandleFunc("/order/", s.handleOrder)
	mux.HandleFunc("/finalize/", s.handleFinalize)
	mux.HandleFunc("/cert/", s.handleCert)
	mux.HandleFunc("/authz/", s.handleAuthz)
	mux.HandleFunc("/chal/", s.handleChallenge)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubServer) directoryURL() string { return s.srv.URL + "/dir" }

func (s *stubServer) writeNonce(w http.ResponseWriter) {
	w.Header().Set("Replay-Nonce", fmt.Sprintf("nonce-%d", mrand.Int63()))
}

func (s *stubServer) handleDirectory(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	body := map[string]string{
		"newNonce":   s.srv.URL + "/new-nonce",
		"newAccount": s.srv.URL + "/new-account",
		"newOrder":   s.srv.URL + "/new-order",
		"revokeCert": s.srv.URL + "/revoke-cert",
		"keyChange":  s.srv.URL + "/key-change",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

func (s *stubServer) handleNewNonce(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	w.WriteHeader(http.StatusOK)
}

// readJWS is a permissive parser: we accept the request as long as the
// JWS structure is well-formed; signature validation is out of scope
// for this test stub (the client's signing logic is exercised in the
// jws_test.go unit tests).
func (s *stubServer) readJWS(r *http.Request) (header jwsHeader, payload []byte, err error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return header, nil, err
	}
	var sr signedRequest
	if err := json.Unmarshal(body, &sr); err != nil {
		return header, nil, err
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(sr.Protected)
	if err != nil {
		return header, nil, err
	}
	if err := json.Unmarshal(hdrBytes, &header); err != nil {
		return header, nil, err
	}
	if sr.Payload == "" {
		return header, nil, nil
	}
	pl, err := base64.RawURLEncoding.DecodeString(sr.Payload)
	if err != nil {
		return header, nil, err
	}
	return header, pl, nil
}

func (s *stubServer) handleNewAccount(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	s.mu.Lock()
	s.accountKID++
	kid := s.accountKID
	s.mu.Unlock()
	w.Header().Set("Location", fmt.Sprintf("%s/account/%d", s.srv.URL, kid))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"status": "valid",
	})
}

func (s *stubServer) handleNewOrder(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	if s.failOrder {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"type":   "urn:ietf:params:acme:error:serverInternal",
			"detail": "stub injected 500",
			"status": 500,
		})
		return
	}
	_, payload, err := s.readJWS(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req orderRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	id := fmt.Sprintf("o%d", s.orderByID)
	s.orderByID++
	authzs := make([]string, 0, len(req.Identifiers))
	for _, ident := range req.Identifiers {
		aid := fmt.Sprintf("a%d", s.authzByID)
		s.authzByID++
		token := fmt.Sprintf("tok-%s", aid)
		authz := &stubAuthz{
			ID:         aid,
			URL:        fmt.Sprintf("%s/authz/%s", s.srv.URL, aid),
			Status:     "pending",
			Identifier: ident,
			Token:      token,
			ChalURLs: map[string]string{
				"http-01":     fmt.Sprintf("%s/chal/%s/http-01", s.srv.URL, aid),
				"tls-alpn-01": fmt.Sprintf("%s/chal/%s/tls-alpn-01", s.srv.URL, aid),
				"dns-01":      fmt.Sprintf("%s/chal/%s/dns-01", s.srv.URL, aid),
			},
			ChalStatus: map[string]string{
				"http-01":     "pending",
				"tls-alpn-01": "pending",
				"dns-01":      "pending",
			},
		}
		s.authzs[aid] = authz
		authzs = append(authzs, authz.URL)
	}
	o := &stubOrder{
		ID:          id,
		Status:      "pending",
		URL:         fmt.Sprintf("%s/order/%s", s.srv.URL, id),
		Authzs:      authzs,
		Finalize:    fmt.Sprintf("%s/finalize/%s", s.srv.URL, id),
		Identifiers: req.Identifiers,
	}
	s.orders[id] = o
	s.mu.Unlock()
	w.Header().Set("Location", o.URL)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(s.orderJSON(o))
}

func (s *stubServer) orderJSON(o *stubOrder) map[string]any {
	out := map[string]any{
		"status":         o.Status,
		"identifiers":    o.Identifiers,
		"authorizations": o.Authzs,
		"finalize":       o.Finalize,
	}
	if o.Certificate != "" {
		out["certificate"] = o.Certificate
	}
	return out
}

func (s *stubServer) handleOrder(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	id := strings.TrimPrefix(r.URL.Path, "/order/")
	s.mu.Lock()
	o, ok := s.orders[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.orderJSON(o))
}

func (s *stubServer) handleFinalize(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	id := strings.TrimPrefix(r.URL.Path, "/finalize/")
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, payload, err := s.readJWS(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var fr finalizeRequest
	if err := json.Unmarshal(payload, &fr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	csrDER, err := base64.RawURLEncoding.DecodeString(fr.CSR)
	if err != nil {
		http.Error(w, "csr decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		http.Error(w, "csr parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := csr.CheckSignature(); err != nil {
		http.Error(w, "csr verify: "+err.Error(), http.StatusBadRequest)
		return
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(s.orderByID + 1000)),
		Subject:      pkix.Name{CommonName: csr.Subject.CommonName},
		DNSNames:     csr.DNSNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, s.caCert, csr.PublicKey, s.caKey)
	if err != nil {
		http.Error(w, "issue: "+err.Error(), http.StatusInternalServerError)
		return
	}
	chainPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.caCertDER})...)
	o.CertPEM = string(chainPEM)
	o.Certificate = fmt.Sprintf("%s/cert/%s", s.srv.URL, o.ID)
	o.Status = "valid"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.orderJSON(o))
}

func (s *stubServer) handleCert(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	id := strings.TrimPrefix(r.URL.Path, "/cert/")
	s.mu.Lock()
	o, ok := s.orders[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	io.WriteString(w, o.CertPEM)
}

func (s *stubServer) handleAuthz(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	id := strings.TrimPrefix(r.URL.Path, "/authz/")
	s.mu.Lock()
	a, ok := s.authzs[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.authzJSON(a))
}

func (s *stubServer) authzJSON(a *stubAuthz) map[string]any {
	chals := make([]map[string]any, 0, 3)
	for _, ct := range []string{"http-01", "tls-alpn-01", "dns-01"} {
		chals = append(chals, map[string]any{
			"type":   ct,
			"url":    a.ChalURLs[ct],
			"token":  a.Token,
			"status": a.ChalStatus[ct],
		})
	}
	return map[string]any{
		"status":     a.Status,
		"identifier": a.Identifier,
		"challenges": chals,
	}
}

// handleChallenge accepts the challenge ACK POST and flips the authz
// to valid, the parent order to ready. URL shape: /chal/<authz>/<type>
func (s *stubServer) handleChallenge(w http.ResponseWriter, r *http.Request) {
	s.writeNonce(w)
	rest := strings.TrimPrefix(r.URL.Path, "/chal/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	aid, ct := parts[0], parts[1]
	s.mu.Lock()
	a, ok := s.authzs[aid]
	if !ok {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if s.httpFetcher != nil && ct == "http-01" {
		if _, err := s.httpFetcher(a.Token); err != nil {
			a.Status = "invalid"
			a.ChalStatus[ct] = "invalid"
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"type":   ct,
				"status": "invalid",
				"url":    a.ChalURLs[ct],
				"token":  a.Token,
				"error":  map[string]any{"detail": err.Error(), "type": "urn:ietf:params:acme:error:unauthorized"},
			})
			return
		}
	}
	a.Status = "valid"
	a.ChalStatus[ct] = "valid"
	for _, o := range s.orders {
		ready := true
		for _, au := range o.Authzs {
			id := strings.TrimPrefix(au, s.srv.URL+"/authz/")
			if s.authzs[id].Status != "valid" {
				ready = false
				break
			}
		}
		if ready && o.Status == "pending" {
			o.Status = "ready"
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"type":   ct,
		"status": "valid",
		"url":    a.ChalURLs[ct],
		"token":  a.Token,
	})
}

// httpFetchChallenge is a tiny helper: builds a synthetic GET against
// the operator's HTTP-01 mux and returns the response body.
func httpFetchChallenge(t *testing.T, h http.Handler, token string) (string, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/"+token, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("http-01 fetch: status %d", rec.Code)
	}
	return rec.Body.String(), nil
}

// signKeyAuth recomputes the JWS thumbprint for a given signer; used
// by the stub's HTTP-01 validator to check the served key
// authorisation matches the request.
func signKeyAuth(s *ecdsaSigner, token string) (string, error) {
	thumb, err := jwsThumbprint(s)
	if err != nil {
		return "", err
	}
	return token + "." + thumb, nil
}

// helper that mints an ECDSA key + CSR self-check; used to assert the
// stub-issued cert parses cleanly.
func parsePEMCert(t *testing.T, pemStr string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatalf("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return c
}

// asn1KeyAuthHash digests keyAuth per RFC 8737 §3 — used to verify a
// presented TLS-ALPN-01 cert carries the right extension value.
func asn1KeyAuthHash(keyAuth string) []byte {
	sum := sha256.Sum256([]byte(keyAuth))
	return sum[:]
}

// errExpected is a sentinel used by tests to mean "we wanted this to
// fail" — kept here so individual tests do not redefine it.
var errExpected = errors.New("test: expected failure")

// curveBytes/sumStub exist only to silence unused-warnings when adding
// new helpers; not used in production.
var (
	_ = elliptic.P256
	_ = ecdsa.GenerateKey
)
