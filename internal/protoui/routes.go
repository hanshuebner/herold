package protoui

import (
	"io/fs"
	"net/http"
	"strings"
)

// registerRoutes mounts every UI handler on mux. Routes are absolute
// (`/ui/login`, …) so the parent mux can route `/ui/` here without
// rewriting.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	p := s.pathPrefix

	// Static assets — vendored HTMX, Alpine, css. No auth, no CSRF.
	// We strip the leading prefix so the embed FS lookup works
	// (`/ui/static/htmx.min.js` -> `static/htmx.min.js`).
	staticPrefix := p + "/static/"
	staticHandler := http.StripPrefix(staticPrefix, s.staticFileServer())
	mux.Handle("GET "+staticPrefix, staticHandler)

	// Auth pages.
	mux.HandleFunc("GET "+p+"/login", s.handleLoginGet)
	mux.HandleFunc("POST "+p+"/login", s.handleLoginPost)
	mux.HandleFunc("POST "+p+"/logout", s.handleLogoutPost)
	mux.HandleFunc("GET "+p+"/oidc/{provider}/begin", s.handleOIDCBegin)
	mux.HandleFunc("GET "+p+"/oidc/{provider}/callback", s.handleOIDCCallback)

	// Authenticated pages.
	mux.HandleFunc("GET "+p+"/", s.requireSession(s.handleDashboardRedirect))
	mux.HandleFunc("GET "+p+"/dashboard", s.requireSession(s.handleDashboard))

	// Principals.
	mux.HandleFunc("GET "+p+"/principals", s.requireSession(s.handlePrincipalsList))
	mux.HandleFunc("GET "+p+"/principals/new", s.requireSession(s.handlePrincipalsNewForm))
	mux.HandleFunc("POST "+p+"/principals", s.requireSession(s.requireCSRF(s.handlePrincipalsCreate)))
	mux.HandleFunc("GET "+p+"/principals/{pid}", s.requireSession(s.handlePrincipalsDetail))
	mux.HandleFunc("POST "+p+"/principals/{pid}", s.requireSession(s.requireCSRF(s.handlePrincipalsUpdate)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/delete", s.requireSession(s.requireCSRF(s.handlePrincipalsDelete)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/password", s.requireSession(s.requireCSRF(s.handlePrincipalsPassword)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/totp/enroll", s.requireSession(s.requireCSRF(s.handleTOTPEnroll)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/totp/confirm", s.requireSession(s.requireCSRF(s.handleTOTPConfirm)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/totp/disable", s.requireSession(s.requireCSRF(s.handleTOTPDisable)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/api-keys/new", s.requireSession(s.requireCSRF(s.handleAPIKeyNew)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/api-keys/{id}/revoke", s.requireSession(s.requireCSRF(s.handleAPIKeyRevoke)))
	mux.HandleFunc("POST "+p+"/principals/{pid}/oidc/{provider_id}/unlink", s.requireSession(s.requireCSRF(s.handleOIDCUnlink)))
	mux.HandleFunc("GET "+p+"/principals/{pid}/oidc/{provider_id}/link", s.requireSession(s.handleOIDCLinkBegin))

	// Domains + aliases.
	mux.HandleFunc("GET "+p+"/domains", s.requireSession(s.handleDomainsList))
	mux.HandleFunc("POST "+p+"/domains", s.requireSession(s.requireCSRF(s.handleDomainsCreate)))
	mux.HandleFunc("GET "+p+"/domains/{name}", s.requireSession(s.handleDomainDetail))
	mux.HandleFunc("POST "+p+"/domains/{name}/delete", s.requireSession(s.requireCSRF(s.handleDomainDelete)))
	mux.HandleFunc("POST "+p+"/domains/{name}/aliases", s.requireSession(s.requireCSRF(s.handleAliasCreate)))
	mux.HandleFunc("POST "+p+"/domains/{name}/aliases/{id}/delete", s.requireSession(s.requireCSRF(s.handleAliasDelete)))

	// Queue monitor.
	mux.HandleFunc("GET "+p+"/queue", s.requireSession(s.handleQueueList))
	mux.HandleFunc("GET "+p+"/queue/{id}", s.requireSession(s.handleQueueDetail))
	mux.HandleFunc("POST "+p+"/queue/{id}/retry", s.requireSession(s.requireCSRF(s.handleQueueRetry)))
	mux.HandleFunc("POST "+p+"/queue/{id}/hold", s.requireSession(s.requireCSRF(s.handleQueueHold)))
	mux.HandleFunc("POST "+p+"/queue/{id}/release", s.requireSession(s.requireCSRF(s.handleQueueRelease)))
	mux.HandleFunc("POST "+p+"/queue/{id}/delete", s.requireSession(s.requireCSRF(s.handleQueueDelete)))

	// Email research.
	mux.HandleFunc("GET "+p+"/research", s.requireSession(s.handleResearch))

	// Audit log.
	mux.HandleFunc("GET "+p+"/audit", s.requireSession(s.handleAudit))
}

// staticFileServer serves the embedded /static directory. Content-Type
// is left to net/http's auto-detection; we add a long Cache-Control
// since the assets are versioned by binary release.
func (s *Server) staticFileServer() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Should never happen — staticFS is a compile-time constant.
		// Fall back to a 500 handler so the broken case is loud.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "static fs unavailable", http.StatusInternalServerError)
		})
	}
	fileSrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block any path that escapes the embed FS root via "..".
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileSrv.ServeHTTP(w, r)
	})
}
