// Package fakeapp is an in-tree stand-in for an external herold app
// (docs/design/server/requirements/29-apps.md REQ-APP-70) for tests and
// the dev instance. It serves a v1 manifest, verifies Herold-Signature
// on every invocation the way a real app must (constant-time HMAC
// compare, 300 s timestamp window, replayed invocation ids refused),
// records each invocation for inspection, and answers per extension:
//
//	open-url  {"type":"open_url"} to its own /opened/<invocation_id> page
//	notice    {"type":"notice"} naming the number of messages received
//	reject    422 {"error": ...}
//	fail      500 without a body
//	slow      waits Options.SlowDelay (past herold's invoke timeout)
//
// The origin is plain HTTP on 127.0.0.1, so herold reaches it only when
// the origin is listed in [server.apps] allowed_insecure_origins
// (REQ-APP-61). Herold generates the signing secret at registration, so
// the fake learns it afterwards through SetSecret or PUT /secret.
//
// Inspection endpoints:
//
//	GET    /requests  JSON array of Invocation
//	DELETE /requests  clears the recorded invocations
//	PUT    /secret    sets the signing secret (request body, as presented)
//	GET    /opened/<id>  HTML page for an open-url invocation
//
// For tests use New, which registers cleanup on testing.TB; for
// standalone tooling (cmd/heroldfakeapp) use NewServer and Close.
package fakeapp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// DefaultAppKey is the manifest app_key the fake serves.
const DefaultAppKey = "local.herold.fakeapp"

// Extension ids the fake declares.
const (
	ExtOpenURL = "open-url"
	ExtNotice  = "notice"
	ExtReject  = "reject"
	ExtFail    = "fail"
	ExtSlow    = "slow"
)

// RejectText is the error text the reject extension answers with.
const RejectText = "The fake app rejected this request."

// maxBody bounds an accepted invocation body.
const maxBody = 256 << 20

// signatureWindow is the accepted clock skew on Herold-Signature.
const signatureWindow = 300 * time.Second

// Part is one message part of a recorded invocation.
type Part struct {
	Name        string `json:"name"`
	FileName    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	SHA256      string `json:"sha256"`
}

// Invocation is one recorded POST to an extension endpoint.
type Invocation struct {
	Extension      string          `json:"extension"`
	InvocationID   string          `json:"invocation_id"`
	AppAPI         string          `json:"app_api"`
	UserAgent      string          `json:"user_agent"`
	ContentType    string          `json:"content_type"`
	SignatureValid bool            `json:"signature_valid"`
	SignatureError string          `json:"signature_error,omitempty"`
	Context        json.RawMessage `json:"context,omitempty"`
	ContextType    string          `json:"context_type"`
	PartCount      int             `json:"part_count"`
	Parts          []Part          `json:"parts"`
	BodyBytes      int             `json:"body_bytes"`
	Status         int             `json:"status"`
}

// Options configures a Server.
type Options struct {
	// Addr is the listen address; default "127.0.0.1:0".
	Addr string
	// AppKey is the manifest app_key; default DefaultAppKey.
	AppKey string
	// Secret is the initial signing secret (as presented by herold).
	Secret string
	// SlowDelay is how long the slow extension waits before answering;
	// default 30 s.
	SlowDelay time.Duration
	// Now is the clock the signature window is checked against;
	// default time.Now.
	Now func() time.Time
}

// Server is a running fake app.
type Server struct {
	ln     net.Listener
	srv    *http.Server
	appKey string
	slow   time.Duration
	now    func() time.Time

	mu          sync.Mutex
	secret      string
	invocations []Invocation
	seen        map[string]bool
	opened      map[string]int
}

// NewServer starts the fake. Call Close when done.
func NewServer(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	if opts.AppKey == "" {
		opts.AppKey = DefaultAppKey
	}
	if opts.SlowDelay <= 0 {
		opts.SlowDelay = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("fakeapp: listen: %w", err)
	}
	s := &Server{
		ln: ln, appKey: opts.AppKey, slow: opts.SlowDelay, now: opts.Now,
		secret: opts.Secret, seen: map[string]bool{}, opened: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/herold-app.json", s.handleManifest)
	mux.HandleFunc("POST /actions/{ext}", s.handleAction)
	mux.HandleFunc("GET /opened/{id}", s.handleOpened)
	mux.HandleFunc("/requests", s.handleRequests)
	mux.HandleFunc("PUT /secret", s.handleSecret)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// New starts the fake and registers cleanup on t.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s, err := NewServer(opts)
	if err != nil {
		t.Fatalf("fakeapp.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// Close stops the server.
func (s *Server) Close() { _ = s.srv.Close() }

// Port is the listening TCP port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Origin is the fake's origin, "http://127.0.0.1:<port>".
func (s *Server) Origin() string { return "http://" + s.ln.Addr().String() }

// AppKey is the manifest app_key.
func (s *Server) AppKey() string { return s.appKey }

// SetSecret sets the signing secret invocations are verified against.
func (s *Server) SetSecret(secret string) {
	s.mu.Lock()
	s.secret = secret
	s.mu.Unlock()
}

// Invocations returns a snapshot of the recorded invocations.
func (s *Server) Invocations() []Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Invocation(nil), s.invocations...)
}

// Reset clears the recorded invocations.
func (s *Server) Reset() {
	s.mu.Lock()
	s.invocations = nil
	s.mu.Unlock()
}

// Manifest returns the manifest document the fake serves.
func (s *Server) Manifest() map[string]any {
	ext := func(id, en, de, icon string) map[string]any {
		return map[string]any{
			"point":    "thread.action",
			"id":       id,
			"label":    map[string]string{"en": en, "de": de},
			"icon":     icon,
			"scopes":   []string{"thread.raw"},
			"endpoint": "/actions/" + id,
		}
	}
	return map[string]any{
		"herold_app": 1,
		"app_key":    s.appKey,
		"name":       map[string]string{"en": "Fake app", "de": "Test-App"},
		"extensions": []any{
			ext(ExtOpenURL, "Open in fake app", "In Test-App öffnen", "link"),
			ext(ExtNotice, "Send to fake app", "An Test-App senden", "send"),
			ext(ExtReject, "Fake app: reject", "Test-App: ablehnen", "archive"),
			ext(ExtFail, "Fake app: fail", "Test-App: Fehler", "app"),
			ext(ExtSlow, "Fake app: slow", "Test-App: langsam", "ticket"),
		},
	}
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Manifest())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// verifySignature checks header against body with secret, the way an
// app must (REQ-APP-43).
func verifySignature(secret, header string, body []byte, now time.Time) error {
	if secret == "" {
		return fmt.Errorf("no secret configured")
	}
	var ts, sig string
	for _, kv := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return fmt.Errorf("malformed Herold-Signature %q", header)
	}
	if d := now.Sub(time.Unix(t, 0)); d > signatureWindow || d < -signatureWindow {
		return fmt.Errorf("timestamp %d outside the %s window", t, signatureWindow)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1:" + ts + ":"))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	ext := r.PathValue("ext")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	inv := Invocation{
		Extension:    ext,
		InvocationID: r.Header.Get("Herold-Invocation-Id"),
		AppAPI:       r.Header.Get("Herold-App-Api"),
		UserAgent:    r.Header.Get("User-Agent"),
		ContentType:  r.Header.Get("Content-Type"),
		BodyBytes:    len(body),
		Parts:        []Part{},
	}
	s.mu.Lock()
	secret := s.secret
	s.mu.Unlock()
	if err := verifySignature(secret, r.Header.Get("Herold-Signature"), body, s.now()); err != nil {
		inv.SignatureError = err.Error()
	} else {
		inv.SignatureValid = true
	}
	s.parseBody(&inv, body)

	s.mu.Lock()
	replay := inv.InvocationID != "" && s.seen[inv.InvocationID]
	s.seen[inv.InvocationID] = true
	if inv.SignatureValid && replay {
		inv.SignatureValid = false
		inv.SignatureError = "replayed invocation id"
	}
	s.mu.Unlock()

	status := http.StatusOK
	var reply any
	switch {
	case !inv.SignatureValid:
		status, reply = http.StatusUnauthorized, map[string]string{"error": "invalid signature: " + inv.SignatureError}
	case ext == ExtOpenURL:
		s.mu.Lock()
		s.opened[inv.InvocationID] = inv.PartCount
		s.mu.Unlock()
		reply = map[string]string{"type": "open_url", "url": s.Origin() + "/opened/" + inv.InvocationID}
	case ext == ExtNotice:
		reply = map[string]string{"type": "notice", "text": fmt.Sprintf("Fake app received %d messages.", inv.PartCount)}
	case ext == ExtReject:
		status, reply = http.StatusUnprocessableEntity, map[string]string{"error": RejectText}
	case ext == ExtFail:
		status = http.StatusInternalServerError
	case ext == ExtSlow:
		timer := time.NewTimer(s.slow)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
		}
		reply = map[string]string{"type": "notice", "text": "Fake app answered slowly."}
	default:
		status, reply = http.StatusNotFound, map[string]string{"error": "unknown extension"}
	}
	inv.Status = status
	s.mu.Lock()
	s.invocations = append(s.invocations, inv)
	s.mu.Unlock()
	if reply == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, reply)
}

// parseBody records the multipart structure of an invocation body.
func (s *Server) parseBody(inv *Invocation, body []byte) {
	mt, params, err := mime.ParseMediaType(inv.ContentType)
	if err != nil || mt != "multipart/form-data" {
		return
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	first := true
	for {
		p, err := mr.NextPart()
		if err != nil {
			return
		}
		data, _ := io.ReadAll(p)
		if first {
			first = false
			if p.FormName() == "context" {
				inv.ContextType = p.Header.Get("Content-Type")
				if json.Valid(data) {
					inv.Context = json.RawMessage(data)
				}
				continue
			}
		}
		sum := sha256.Sum256(data)
		inv.Parts = append(inv.Parts, Part{
			Name:        p.FormName(),
			FileName:    p.FileName(),
			ContentType: p.Header.Get("Content-Type"),
			Size:        len(data),
			SHA256:      hex.EncodeToString(sum[:]),
		})
		inv.PartCount++
	}
}

func (s *Server) handleOpened(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	n, ok := s.opened[id]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><title>Fake app</title><h1>Fake app</h1>"+
		"<p id=\"received\">Received %d messages.</p><p>Invocation %s</p>", n, html.EscapeString(id))
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Invocations())
	case http.MethodDelete:
		s.Reset()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		http.Error(w, "secret required", http.StatusBadRequest)
		return
	}
	s.SetSecret(strings.TrimSpace(string(b)))
	w.WriteHeader(http.StatusNoContent)
}
