package protosend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/hanshuebner/herold/internal/store"
)

// ctxKey is a private type to namespace context keys.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota + 1
	ctxKeyLogger
	ctxKeyPrincipal
	ctxKeyAPIKey
	ctxKeyRemoteAddr
)

// requestID returns the request ID attached to ctx, or "" if none.
func requestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// loggerFrom returns the request-scoped logger, or the server default.
func (s *Server) loggerFrom(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok {
		return v
	}
	return s.logger
}

// principalFrom returns the authenticated principal attached to ctx.
func principalFrom(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(ctxKeyPrincipal).(store.Principal); ok {
		return v, true
	}
	return store.Principal{}, false
}

// apiKeyFrom returns the API key row used to authenticate the request.
func apiKeyFrom(ctx context.Context) (store.APIKey, bool) {
	if v, ok := ctx.Value(ctxKeyAPIKey).(store.APIKey); ok {
		return v, true
	}
	return store.APIKey{}, false
}

// newRequestID produces a 32-character hex token.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// withRequestLog attaches a request ID and scoped slog.Logger.
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-ID", rid)
		lg := s.logger.With(
			slog.String("request_id", rid),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
		)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		ctx = context.WithValue(ctx, ctxKeyLogger, lg)
		ctx = context.WithValue(ctx, ctxKeyRemoteAddr, r.RemoteAddr)
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r.WithContext(ctx))
		lg.LogAttrs(ctx, slog.LevelInfo, "protosend.request",
			slog.Int("status", rec.status))
	})
}

// withPanicRecover catches panics in downstream handlers and returns a
// 500 problem response. One rogue handler does not crash the server
// (STANDARDS.md §6).
func (s *Server) withPanicRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.loggerFrom(r.Context()).Error("protosend.panic",
					"err", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()))
				writeProblem(w, r, http.StatusInternalServerError,
					"internal-error", "internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withConcurrencyLimit rejects requests that exceed the server-wide
// in-flight cap. The check is non-blocking so a single slow handler
// cannot stall fresh requests behind the gate.
func (s *Server) withConcurrencyLimit(sem chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			writeProblem(w, r, http.StatusServiceUnavailable,
				"server-busy", "server is at its concurrency limit", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status so the request log line
// can report it after the downstream handler returns.
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
