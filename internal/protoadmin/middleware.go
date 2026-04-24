package protoadmin

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

// principalFrom returns the authenticated principal attached to ctx, or
// zero-value Principal + false.
func principalFrom(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(ctxKeyPrincipal).(store.Principal); ok {
		return v, true
	}
	return store.Principal{}, false
}

// newRequestID produces a 32-character hex token.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// withRequestLog attaches a request ID and scoped slog.Logger to every
// request's context. Log lines emit on request start and finish with
// standard attrs (request_id, method, path, status, principal_id,
// remote_addr).
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-ID", rid)
		remote := r.RemoteAddr
		lg := s.logger.With(
			slog.String("request_id", rid),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", remote),
		)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		ctx = context.WithValue(ctx, ctxKeyLogger, lg)
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r.WithContext(ctx))
		attrs := []slog.Attr{slog.Int("status", rec.status)}
		if p, ok := principalFrom(ctx); ok {
			attrs = append(attrs, slog.Uint64("principal_id", uint64(p.ID)))
		}
		lg.LogAttrs(ctx, slog.LevelInfo, "protoadmin.request", attrs...)
	})
}

// withPanicRecover catches panics in downstream handlers, logs them
// with a stack trace, and returns a 500 problem response. One rogue
// handler does not crash the server (STANDARDS.md §6).
func (s *Server) withPanicRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.loggerFrom(r.Context()).Error("protoadmin.panic",
					"err", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()))
				writeProblem(w, r, http.StatusInternalServerError,
					"internal_error", "internal server error", "")
			}
		}()
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

// appendAudit writes an audit log entry describing a successful or
// failed mutation. The store is the durable sink; a separate slog line
// lets log tailers see the same event in real time.
func (s *Server) appendAudit(
	ctx context.Context,
	action, subject string,
	outcome store.AuditOutcome,
	message string,
	metadata map[string]string,
) {
	actorKind := store.ActorSystem
	actorID := "system"
	if p, ok := principalFrom(ctx); ok {
		actorKind = store.ActorPrincipal
		actorID = fmt.Sprintf("%d", p.ID)
	}
	remote := ""
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		// request_id is not remote_addr; put the remote on the entry via
		// metadata instead. The request_id is recorded for cross-ref with
		// slog.
		if metadata == nil {
			metadata = make(map[string]string, 1)
		}
		metadata["request_id"] = v
	}
	if v, ok := ctx.Value(ctxKeyRemoteAddr).(string); ok {
		remote = v
	}
	entry := store.AuditLogEntry{
		At:         s.clk.Now(),
		ActorKind:  actorKind,
		ActorID:    actorID,
		Action:     action,
		Subject:    subject,
		RemoteAddr: remote,
		Outcome:    outcome,
		Message:    message,
		Metadata:   metadata,
	}
	if err := s.store.Meta().AppendAuditLog(ctx, entry); err != nil {
		s.loggerFrom(ctx).Warn("protoadmin.audit.append_failed",
			"err", err, "action", action, "subject", subject)
	}
}

const ctxKeyRemoteAddr ctxKey = 99
