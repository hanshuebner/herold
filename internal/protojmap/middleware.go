package protojmap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// withRequestLog attaches a request ID and scoped slog.Logger to every
// request's context. Log lines emit on request start and finish with
// standard attrs.
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-ID", rid)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r.WithContext(ctx))
		attrs := []slog.Attr{
			slog.String("request_id", rid),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
			slog.Int("status", rec.status),
		}
		if p, ok := PrincipalFromContext(r.Context()); ok {
			attrs = append(attrs, slog.Uint64("principal_id", uint64(p.ID)))
		}
		s.log.LogAttrs(ctx, slog.LevelInfo, "protojmap.request", attrs...)
	})
}

// withPanicRecover catches handler panics, logs them with a stack
// trace, and returns a 500 problem response. One rogue handler does
// not crash the server (STANDARDS.md §6).
func (s *Server) withPanicRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("protojmap.panic",
					"err", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()))
				WriteJMAPError(w, http.StatusInternalServerError,
					"serverFail", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// newRequestID produces a 32-character hex token used as X-Request-ID
// when the client did not supply one.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder captures the response status so the request log line
// can report it after the downstream handler returns. The
// ResponseWriter is a passthrough; the EventSource handler implements
// http.Flusher on its own.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer's Flush implementation if it
// has one; required so the EventSource SSE writer's flush works
// through our wrapping middleware. http.NewResponseController is the
// modern stdlib idiom.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
