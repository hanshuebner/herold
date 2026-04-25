package admin

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// withPanicRecover wraps next so a panic inside the downstream handler
// is recovered, logged with a stack trace and the requesting URL path,
// and translated into a 500 response (or, for a hijacked WebSocket,
// silently swallowed because the headers are already on the wire).
//
// One panic must not crash the suite (STANDARDS §6). The middleware
// lives here rather than in a per-protocol package because the admin
// composer pulls handlers from three different upstream packages
// (protochat, protocall, protoimg), each with its own logger and
// response shape; a single shared wrapper keeps the recovery posture
// uniform.
func withPanicRecover(logger *slog.Logger, name string, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			logger.LogAttrs(r.Context(), slog.LevelError, "admin: handler panic",
				slog.String("handler", name),
				slog.String("path", r.URL.Path),
				slog.String("err", fmt.Sprintf("%v", rec)),
				slog.String("stack", string(debug.Stack())),
			)
			// If the handler is a WebSocket hijack the response writer's
			// headers are already on the wire and a write here would
			// race the hijacked goroutine; we swallow silently. For
			// every other handler, emit a minimal 500 — the upstream
			// recover policy returns plain text rather than RFC 7807
			// because the failure mode is "handler exploded", not a
			// validated business error.
			defer func() { _ = recover() }()
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal server error\n"))
		}()
		next.ServeHTTP(w, r)
	})
}
