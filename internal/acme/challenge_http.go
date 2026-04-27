package acme

import (
	"net/http"
	"strings"
	"sync"
)

// HTTPChallenger answers ACME HTTP-01 challenges (RFC 8555 §8.3). The
// operator wires Handler() under "/.well-known/acme-challenge/" on the
// port-80 listener; Provision and Cleanup are called by the order
// pipeline as challenges are presented and torn down.
//
// Concurrency: a single HTTPChallenger is shared by the renewal loop
// and any in-flight EnsureCert calls. All methods are safe for
// concurrent use.
type HTTPChallenger struct {
	mu     sync.RWMutex
	tokens map[string]string
}

// NewHTTPChallenger returns an HTTPChallenger with no challenges
// presented.
func NewHTTPChallenger() *HTTPChallenger {
	return &HTTPChallenger{tokens: make(map[string]string)}
}

// Provision registers the keyAuth response for token. The combination
// is what the ACME server fetches at
// http://<host>/.well-known/acme-challenge/<token>. Calling Provision
// twice for the same token replaces the prior key authorisation; that
// supports retries against the same token without leaking entries.
func (h *HTTPChallenger) Provision(token, keyAuth string) {
	if token == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tokens[token] = keyAuth
}

// Cleanup removes the entry for token. Safe to call when the token was
// never provisioned (no-op).
func (h *HTTPChallenger) Cleanup(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tokens, token)
}

// Handler returns an http.Handler that serves /.well-known/
// acme-challenge/<token> with the registered keyAuth or 404 when the
// token is unknown. The caller mounts this under the ACME prefix; the
// handler itself accepts the unprefixed final path segment.
func (h *HTTPChallenger) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the prefix if the operator did not strip it before
		// mounting; this keeps the handler usable with both http.ServeMux
		// (which keeps the prefix) and http.StripPrefix wrappers.
		const prefix = "/.well-known/acme-challenge/"
		token := r.URL.Path
		if i := strings.Index(token, prefix); i >= 0 {
			token = token[i+len(prefix):]
		} else {
			token = strings.TrimPrefix(token, "/")
		}
		if token == "" {
			http.NotFound(w, r)
			return
		}
		h.mu.RLock()
		keyAuth, ok := h.tokens[token]
		h.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keyAuth))
	})
}
