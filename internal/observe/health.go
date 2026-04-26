package observe

import (
	"net/http"
	"sync/atomic"
)

// Health tracks liveness and readiness for the /healthz endpoints
// (REQ-OPS-110, REQ-OPS-111). Liveness is always true for a running process;
// readiness is gated on all registered conditions being satisfied.
//
// REQ-OPS-111: readiness reports not-ready until (a) ACME is not configured,
// or (b) the ACME account is loaded and at least one usable cert is in the
// TLS store. The acmeReady gate is set via MarkACMEReady; when ACME is not
// configured the gate is pre-cleared by MarkACMENotRequired so it never
// blocks readiness.
type Health struct {
	ready    atomic.Bool
	acmeOK   atomic.Bool // true when ACME is ready or not required
	acmeReqd atomic.Bool // true when [acme] is configured (gate is active)
}

// NewHealth returns a fresh Health with readiness set to false.
func NewHealth() *Health {
	return &Health{}
}

// MarkACMENotRequired signals that ACME is not configured, so the ACME
// readiness gate is permanently satisfied. Call this before MarkReady when
// no [acme] block is present in system.toml.
func (h *Health) MarkACMENotRequired() {
	h.acmeOK.Store(true)
}

// MarkACMERequired records that [acme] is configured; the gate is NOT yet
// satisfied. Call this before MarkReady when [acme] is present.
func (h *Health) MarkACMERequired() {
	h.acmeReqd.Store(true)
}

// MarkACMEReady signals that the ACME account is loaded and at least one
// usable cert is present (REQ-OPS-111). Safe for concurrent use.
func (h *Health) MarkACMEReady() {
	h.acmeOK.Store(true)
}

// MarkReady flips overall readiness to true. Safe for concurrent use.
func (h *Health) MarkReady() {
	h.ready.Store(true)
}

// MarkNotReady flips readiness back to false (e.g. during graceful shutdown).
func (h *Health) MarkNotReady() {
	h.ready.Store(false)
}

// Ready reports the current readiness state: true when MarkReady has been
// called AND (ACME is not required OR ACME is ready).
func (h *Health) Ready() bool {
	if !h.ready.Load() {
		return false
	}
	if h.acmeReqd.Load() && !h.acmeOK.Load() {
		return false
	}
	return true
}

// LivenessHandler returns an http.Handler that always responds 200 OK.
func (h *Health) LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

// ReadinessHandler returns an http.Handler that responds 200 once MarkReady
// has been called, and 503 otherwise.
func (h *Health) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if h.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
	})
}
