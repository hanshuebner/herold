package protoui

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

// Options configures a Server. Required fields are documented; zero
// values for optional fields apply the documented defaults.
type Options struct {
	// PathPrefix is the URL prefix every UI route lives under. Defaults
	// to "/ui". The handler returned by Handler is mountable verbatim
	// under this prefix; routes are registered with their absolute
	// paths so a parent http.ServeMux can route by prefix without
	// trimming.
	PathPrefix string
	// Session configures cookie session behaviour. See SessionConfig.
	Session SessionConfig
	// Logger overrides the package's default slog.Logger.
	Logger *slog.Logger
}

// Server is the protoui handle. One *Server backs the whole UI; tests
// construct one against a testharness server, production constructs
// one in internal/admin/server.go and mounts it onto the parent mux.
type Server struct {
	store      store.Store
	dir        *directory.Directory
	rp         *directoryoidc.RP
	clk        clock.Clock
	logger     *slog.Logger
	cfg        SessionConfig
	pathPrefix string

	tmpl *template.Template

	mux *http.ServeMux
}

// NewServer constructs a Server. store, dir, and rp are required; rp
// may be nil when the deployment has no OIDC providers configured.
func NewServer(
	st store.Store,
	dir *directory.Directory,
	rp *directoryoidc.RP,
	clk clock.Clock,
	opts Options,
) (*Server, error) {
	if st == nil {
		return nil, errors.New("protoui: nil Store")
	}
	if dir == nil {
		return nil, errors.New("protoui: nil Directory")
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	prefix := opts.PathPrefix
	if prefix == "" {
		prefix = "/ui"
	}
	prefix = strings.TrimRight(prefix, "/")
	cfg := opts.Session
	if cfg.CookieName == "" {
		cfg.CookieName = "herold_ui_session"
	}
	if cfg.CSRFCookieName == "" {
		cfg.CSRFCookieName = "herold_ui_csrf"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if len(cfg.SigningKey) < 32 {
		// Generate a per-process signing key so the server is usable
		// out of the box; operators who want session continuity across
		// restarts must supply a stable key. We do NOT log the key.
		var k [32]byte
		if _, err := io.ReadFull(rand.Reader, k[:]); err != nil {
			return nil, fmt.Errorf("protoui: generate signing key: %w", err)
		}
		cfg.SigningKey = k[:]
	}

	tmpl, err := loadTemplates()
	if err != nil {
		return nil, fmt.Errorf("protoui: load templates: %w", err)
	}

	s := &Server{
		store:      st,
		dir:        dir,
		rp:         rp,
		clk:        clk,
		logger:     logger,
		cfg:        cfg,
		pathPrefix: prefix,
		tmpl:       tmpl,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.mux = mux

	return s, nil
}

// Handler returns the UI mux ready to mount. The handler is wrapped
// with the package's standard middleware chain (panic recovery,
// security headers); auth and CSRF are per-route.
func (s *Server) Handler() http.Handler {
	return s.withSecurityHeaders(s.withPanicRecover(s.mux))
}

// PathPrefix reports the configured path prefix (e.g. "/ui"). Used by
// the parent mux to register the right pattern.
func (s *Server) PathPrefix() string { return s.pathPrefix }

// loadTemplates parses every templates/*.html and templates/fragments/*.html
// from the embedded FS into a single *template.Template. The base
// layout.html defines the outer chrome; per-page templates use
// {{define "title"}} / {{define "body"}} / etc. to slot into it.
func loadTemplates() (*template.Template, error) {
	root := template.New("").Funcs(funcMap())
	// Walk the embedded FS so test code that snapshot-tests the
	// template registration sees a stable order.
	patterns := []string{"templates/*.html", "templates/fragments/*.html"}
	for _, pat := range patterns {
		matches, err := fs.Glob(templatesFS, pat)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			b, err := fs.ReadFile(templatesFS, m)
			if err != nil {
				return nil, err
			}
			if _, err := root.New(stripDir(m)).Parse(string(b)); err != nil {
				return nil, fmt.Errorf("parse %s: %w", m, err)
			}
		}
	}
	return root, nil
}

// stripDir reduces "templates/fragments/audit_entry.html" to
// "audit_entry.html" so handlers can reference templates by basename.
func stripDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// withSecurityHeaders attaches a strict CSP, Referrer-Policy, and the
// usual X-Content-Type-Options + X-Frame-Options to every response.
// The CSP allows inline styles (we ship a tiny <style> block in
// layout.html) and the vendored scripts under /ui/static/, but no
// remote origins.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Inline styles permitted for the small layout-level <style>;
		// scripts permitted only from self (the embedded vendored copies).
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// withPanicRecover catches panics from any UI handler, logs them, and
// returns a 500. Mirrors protoadmin's middleware so a rogue handler
// does not crash the process.
func (s *Server) withPanicRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("protoui.panic",
					"err", fmt.Sprintf("%v", rec),
					"path", r.URL.Path)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("<h1>Internal error</h1><p>Please try again.</p>"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// principalFromCtx returns the authenticated principal attached by
// requireSession.
func principalFromCtx(ctx context.Context) (store.Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(store.Principal)
	return p, ok
}

// sessionFromCtx returns the session attached by requireSession.
func sessionFromCtx(ctx context.Context) (session, bool) {
	v, ok := ctx.Value(ctxKeySession).(session)
	return v, ok
}

// ctxKey is a package-private type for context-value keys.
type ctxKey int

const (
	ctxKeyPrincipal ctxKey = iota + 1
	ctxKeySession
)
