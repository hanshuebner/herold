package observe

import (
	"net/http"
	"sync/atomic"
)

// Health tracks liveness and readiness for the /healthz endpoints
// (REQ-OPS-110, REQ-OPS-111). Liveness is always true for a running process;
// readiness flips to true only once MarkReady is called.
type Health struct {
	ready atomic.Bool
}

// NewHealth returns a fresh Health with readiness set to false.
func NewHealth() *Health {
	return &Health{}
}

// MarkReady flips readiness to true. Safe for concurrent use.
func (h *Health) MarkReady() {
	h.ready.Store(true)
}

// MarkNotReady flips readiness back to false (e.g. during graceful shutdown).
func (h *Health) MarkNotReady() {
	h.ready.Store(false)
}

// Ready reports the current readiness state.
func (h *Health) Ready() bool {
	return h.ready.Load()
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
