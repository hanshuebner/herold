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

// withRequestLog attaches a request ID and a pre-scoped slog.Logger to every
// request's context, then emits one access-log record at debug after the
// downstream handler returns. Method handlers retrieve the logger via
// loggerFromContext rather than repeating the subsystem/request_id attrs.
//
// Level, activity tag, and field choices follow REQ-OPS-86 / REQ-OPS-86d:
// per-transport echo lines are activity=access at debug.
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-ID", rid)

		// Pre-scope the logger once per request; handlers and the
		// per-method dispatcher pull it back out via loggerFromContext.
		reqLog := s.log.With(
			"subsystem", "protojmap",
			"request_id", rid,
		)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		ctx = context.WithValue(ctx, ctxKeyLogger, reqLog)

		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r.WithContext(ctx))

		// Emit the per-transport access record after the handler
		// returns so it captures the final status code.
		attrs := []slog.Attr{
			slog.String("activity", "access"),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
			slog.Int("status", rec.status),
		}
		if p, ok := PrincipalFromContext(r.Context()); ok {
			attrs = append(attrs, slog.Uint64("principal_id", uint64(p.ID)))
		}
		reqLog.LogAttrs(ctx, slog.LevelDebug, "protojmap.request", attrs...)
	})
}

// withPanicRecover catches handler panics, logs them with a stack
// trace, and returns a 500 problem response. One rogue handler does
// not crash the server (STANDARDS.md §6).
func (s *Server) withPanicRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log := loggerFromContext(r.Context(), s.log)
				log.Error("protojmap.panic",
					"activity", "internal",
					"err", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()))
				WriteJMAPError(w, http.StatusInternalServerError,
					"serverFail", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// loggerFromContext returns the pre-scoped logger stashed by withRequestLog,
// or fallback when no logger is in context (e.g. tests that bypass the
// middleware chain). Callers add their own per-call attrs on top.
func loggerFromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return fallback
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
