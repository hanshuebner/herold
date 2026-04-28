package protologin

// server.go defines the Options struct, the Server type, and the Mount
// method that registers the login/logout routes on a given mux.
//
// Design note: this package has ~75 lines of handler logic duplicated from
// internal/protoadmin/session_auth.go (handleLogin / handleLogout bodies).
// The duplication is intentional for Phase 3c-i: protoadmin's handler wires
// to the protoadmin.Server struct; this package is struct-free so any listener
// can instantiate it with plain Options. Phase 3c-iii may collapse the
// duplication once the admin handler is also migrated here.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// Options configures a protologin Server. All fields except AuditAppender
// and RateLimiter are required for correct operation; zero values produce a
// server that rejects every request.
type Options struct {
	// Session carries the cookie name, signing key, TTL, and Secure flag
	// that govern cookies issued on this listener.
	Session authsession.SessionConfig
	// Store is used to load the principal after authentication and to
	// confirm the principal is not disabled.
	Store store.Store
	// Directory authenticates credentials and verifies TOTP codes.
	Directory *directory.Directory
	// Clock is used for session expiry and audit timestamps.
	Clock clock.Clock
	// Logger is the structured logger. A nil logger discards output.
	Logger *slog.Logger
	// Listener names the listener for audit metadata ("admin" or "public").
	Listener string
	// Scopes computes the scope set to embed in the issued session cookie.
	// The admin listener passes a constant {admin} set; the public listener
	// passes the end-user default set (REQ-AUTH-SCOPE-01).
	Scopes func(p store.Principal) auth.ScopeSet
	// AuditAppender records login events in the durable audit log
	// (REQ-ADM-300). Nil disables auditing (acceptable in tests that do
	// not exercise the audit-log surface).
	AuditAppender func(ctx context.Context, action, subject string, outcome store.AuditOutcome, message string, meta map[string]string)
	// RateLimiter brackets the endpoints with a per-source-IP bucket.
	// Returns true when the request should proceed, false when it should be
	// rejected (the implementation is expected to write the rejection
	// response before returning false). Nil means no rate limiting.
	RateLimiter func(w http.ResponseWriter, r *http.Request, ipKey string) bool
}

// Server holds the configured handlers. Construct with New; mount with Mount.
type Server struct {
	opts Options
	log  *slog.Logger
}

// New validates opts and returns a ready-to-mount Server.
func New(opts Options) *Server {
	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Server{opts: opts, log: l}
}

// Mount registers POST /api/v1/auth/login, POST /api/v1/auth/logout, and
// GET /api/v1/auth/me on mux. login/logout are unprotected by requireAuth --
// they ARE the authentication boundary; /auth/me reads the session cookie
// directly and returns 401 if it is missing or invalid. Rate limiting
// (if configured) gates login/logout before any principal is resolved.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
}
