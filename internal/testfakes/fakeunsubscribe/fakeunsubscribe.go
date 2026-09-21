// Package fakeunsubscribe is an RFC 8058 one-click unsubscribe origin
// for tests and development. It stands in for a mailing-list sender's
// one-click endpoint so herold's server-side Email/unsubscribe method
// (internal/protojmap/mail/email/unsubscribe.go, issue #412) can be
// exercised end to end without depending on a real third-party sender.
//
// REQ-UNS-04 requires the List-Unsubscribe URL Email/unsubscribe acts
// on to be genuinely HTTPS -- the check is a plain string test on the
// header, so a seeded message pointing at this fake must carry a real
// "https://" URL. The server therefore terminates real TLS with an
// in-process self-signed certificate (the same technique
// internal/testfakes/fakesmtp uses for its STARTTLS/ImplicitTLS
// postures) rather than serving plain HTTP; CertPEM exposes the
// certificate so a caller (scripts/dev-instance.sh, via
// cmd/heroldfakeunsubscribe) can make the herold server process trust
// it for the guarded outbound POST.
//
// The server exposes two fixed POST endpoints -- one that always
// succeeds, one that always fails -- so a seeded message's
// List-Unsubscribe header can point at either to drive both outcomes
// deterministically. Every accepted request is recorded (method,
// headers, body) for assertions that the server honoured RFC 8058's
// POST shape (no cookies, the mandated body, a neutral User-Agent).
//
// The server also serves a static PNG at GET /image.png (issue #443).
// This reuses the same already-SSRF-cleared HTTPS origin -- the same
// port and certificate a caller already allowlists via
// [external_images.network] allowed_ports / extra_ca_file for the
// POST endpoints above -- as the remote-image fixture
// scripts/dev-instance.sh seeds into a message, so
// extimg.Internalize's delivery-time fetch (internalize mode) and the
// operator's SSRF guard configuration exercise one real origin instead
// of two.
//
// For tests, use New which registers cleanup on testing.TB. For
// standalone dev-tooling (scripts/dev-instance.sh), use NewServer and
// call Close when done.
package fakeunsubscribe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Request is one accepted POST recorded by the server.
type Request struct {
	// Path is the request path ("/ok" or "/fail").
	Path string `json:"path"`
	// ContentType is the raw Content-Type header value.
	ContentType string `json:"content_type"`
	// Body is the raw request body, expected to be the RFC 8058 §3.1
	// literal "List-Unsubscribe=One-Click".
	Body string `json:"body"`
	// UserAgent is the raw User-Agent header value.
	UserAgent string `json:"user_agent"`
	// HasCookie reports whether the request carried a Cookie header.
	// RFC 8058 §3.1 requires the POST to omit cookies; a true here
	// signals a caller regression.
	HasCookie bool `json:"has_cookie"`
	// Referer is the raw Referer header value (expected empty).
	Referer string `json:"referer"`
}

// Options configures a Server. The zero value is usable: /fail
// responds 500.
type Options struct {
	// FailStatus is the HTTP status POST /fail returns. Defaults to
	// 500 (Internal Server Error) when zero.
	FailStatus int
}

// Server is a running fake one-click unsubscribe origin.
type Server struct {
	ln         net.Listener
	srv        *http.Server
	failStatus int
	certPEM    []byte

	mu       sync.Mutex
	requests []Request
}

// NewServer starts a fake one-click origin bound to 127.0.0.1 on a
// kernel-picked port, terminating real TLS with an in-process
// self-signed certificate, and returns it. Call Close when done. For
// test code that wants automatic cleanup, use New instead.
func NewServer(opts Options) (*Server, error) {
	if opts.FailStatus == 0 {
		opts.FailStatus = http.StatusInternalServerError
	}
	tlsConfig, certPEM, err := selfSignedTLS("127.0.0.1")
	if err != nil {
		return nil, err
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("fakeunsubscribe: listen: %w", err)
	}
	s := &Server{ln: ln, failStatus: opts.FailStatus, certPEM: certPEM}
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", s.handle(http.StatusOK))
	mux.HandleFunc("/fail", s.handle(s.failStatus))
	mux.HandleFunc("/requests", s.handleRequests)
	mux.HandleFunc("/image.png", s.handleImage)
	s.srv = &http.Server{Handler: mux}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// New starts a fake one-click origin bound to 127.0.0.1 on a
// kernel-picked port and registers cleanup on t.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s, err := NewServer(opts)
	if err != nil {
		t.Fatalf("fakeunsubscribe.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// Addr is the "host:port" the server is listening on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Port is the listening TCP port, for callers that need to allowlist
// it against a [external_images.network] SSRF guard (the guard
// Email/unsubscribe shares with the external-image fetcher).
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// BaseURL is the server's HTTPS base URL ("https://127.0.0.1:<port>").
func (s *Server) BaseURL() string { return "https://" + s.Addr() }

// SuccessURL is the endpoint that always returns 2xx.
func (s *Server) SuccessURL() string { return s.BaseURL() + "/ok" }

// FailureURL is the endpoint that always returns Options.FailStatus.
func (s *Server) FailureURL() string { return s.BaseURL() + "/fail" }

// ImageURL is the endpoint that serves a static, valid, deterministic
// PNG (issue #443): a seeded message's <img src> pointed at this URL
// gives extimg.Internalize a real image to fetch in internalize mode,
// and a real remote URL for the client's blocked-remote-images gate to
// carry in passthrough mode.
func (s *Server) ImageURL() string { return s.BaseURL() + "/image.png" }

// CertPEM returns the server's self-signed certificate in PEM form, so
// a caller can install it as a trusted root for the process making the
// guarded outbound POST (see cmd/heroldfakeunsubscribe, which writes
// this to a file dev-instance.sh points SSL_CERT_FILE at).
func (s *Server) CertPEM() []byte { return append([]byte(nil), s.certPEM...) }

func (s *Server) handle(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		_, hasCookie := r.Header["Cookie"]
		req := Request{
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Body:        string(body),
			UserAgent:   r.Header.Get("User-Agent"),
			HasCookie:   hasCookie,
			Referer:     r.Header.Get("Referer"),
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		w.WriteHeader(status)
	}
}

// handleRequests serves the recorded requests for test/dev assertions.
//
//	GET    /requests — JSON array of Request.
//	DELETE /requests — clears recorded requests; 204 on success.
func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Requests())
	case http.MethodDelete:
		s.mu.Lock()
		s.requests = nil
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// fakeImagePNG is a minimal valid 1x1 PNG (same bytes used by
// internal/protojmap/mail/email's retry-images fixtures), served
// verbatim by handleImage.
var fakeImagePNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

// handleImage serves the static PNG at GET /image.png.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(fakeImagePNG)
}

// Requests returns a snapshot of every accepted POST.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// Close stops the server.
func (s *Server) Close() { _ = s.srv.Close() }

// selfSignedTLS generates a self-signed ECDSA certificate for hostname
// (also covering the 127.0.0.1 IP SAN) and returns a server *tls.Config
// plus the certificate in PEM form. Mirrors fakesmtp's
// selfSignedTLSNoT.
func selfSignedTLS(hostname string) (*tls.Config, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("fakeunsubscribe: gen key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{hostname},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("fakeunsubscribe: create cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("fakeunsubscribe: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("fakeunsubscribe: x509 keypair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, certPEM, nil
}
