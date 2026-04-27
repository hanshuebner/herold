package protoui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// loginPageData is the body payload for templates/login.html.
type loginPageData struct {
	Email     string
	Redirect  string
	Providers []store.OIDCProvider
	NeedTOTP  bool
	Error     string
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	// If already logged in, skip straight to the dashboard.
	if _, ok := s.readSession(r); ok {
		http.Redirect(w, r, s.pathPrefix+"/dashboard", http.StatusSeeOther)
		return
	}
	// Accept both ?redirect= (canonical) and ?return= (the suite's form)
	// with redirect taking precedence when both are present.
	redirectTarget := r.URL.Query().Get("redirect")
	if redirectTarget == "" {
		redirectTarget = r.URL.Query().Get("return")
	}
	body := loginPageData{
		Redirect: redirectTarget,
	}
	if s.rp != nil {
		providers, _ := s.store.Meta().ListOIDCProviders(r.Context())
		body.Providers = providers
	}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Sign in",
		BodyTmpl: "login_body",
		Body:     body,
	})
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	// Login is its own CSRF root: the session does not yet exist, so
	// the double-submit pattern has nothing to compare against. The
	// SameSite=Strict cookie attribute is the protection here, plus
	// per-source rate limiting in the directory layer.
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "form parse failed")
		return
	}
	email := strings.TrimSpace(r.PostForm.Get("email"))
	password := r.PostForm.Get("password")
	totpCode := strings.TrimSpace(r.PostForm.Get("totp"))
	// Accept both redirect= (canonical) and return= (the suite's param)
	// with redirect taking precedence when both are present.
	redirect := r.PostForm.Get("redirect")
	if redirect == "" {
		redirect = r.PostForm.Get("return")
	}

	body := loginPageData{Email: email, Redirect: redirect}

	// Tag the auth source with the remote address so the directory's
	// per-(email,source) rate limiter buckets correctly.
	ctx := directory.WithAuthSource(r.Context(), remoteHost(r.RemoteAddr))
	pid, err := s.dir.Authenticate(ctx, email, password)
	if err != nil {
		body.Error = humanAuthError(err)
		s.renderPage(w, r, http.StatusUnauthorized, &pageData{
			Title:    "Sign in",
			BodyTmpl: "login_body",
			Body:     body,
		})
		return
	}
	// TOTP gating: if the principal has TOTP enrolled, require a code.
	p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "principal load failed")
		return
	}
	// REQ-AUTH-SCOPE-03: the admin listener requires TOTP for
	// principals with 2FA enabled before issuing an admin-scoped
	// cookie. The public listener follows the same TOTP gating but
	// the issued cookie carries end-user scopes only.
	totpRequired := p.Flags.Has(store.PrincipalFlagTOTPEnabled)
	if totpRequired {
		if totpCode == "" {
			body.NeedTOTP = true
			s.renderPage(w, r, http.StatusOK, &pageData{
				Title:    "Sign in",
				BodyTmpl: "login_body",
				Body:     body,
			})
			return
		}
		if err := s.dir.VerifyTOTP(ctx, pid, totpCode); err != nil {
			body.NeedTOTP = true
			body.Error = humanAuthError(err)
			s.renderPage(w, r, http.StatusUnauthorized, &pageData{
				Title:    "Sign in",
				BodyTmpl: "login_body",
				Body:     body,
			})
			return
		}
	}

	// Mint the session with the listener-appropriate scope set
	// (REQ-AUTH-SCOPE-01..03). admin-listener login issues
	// [admin] scope ONLY (no implicit end-user grant per
	// REQ-AUTH-SCOPE-02); public-listener login issues
	// AllEndUserScopes. An operator who wants both must log in on
	// both listeners separately -- different ports, different
	// cookies (REQ-OPS-ADMIN-LISTENER-03).
	sessScopes := s.scopeForLogin(p)
	sess := session{
		PrincipalID: pid,
		ExpiresAt:   s.clk.Now().Add(s.cfg.TTL),
		CSRFToken:   newCSRFToken(),
		Scopes:      sessScopes,
	}
	s.setSessionCookie(w, sess)

	target := s.pathPrefix + "/dashboard"
	if redirect != "" && safeRedirect(redirect, s.pathPrefix, s.listenerKind) {
		target = redirect
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// scopeForLogin returns the scope set to attach to a freshly issued
// cookie. REQ-AUTH-SCOPE-01..03:
//   - admin listener -> [admin]
//   - public listener -> AllEndUserScopes
//
// Admin scope on a non-TOTP principal is permitted (REQ-AUTH-SCOPE-03's
// "operator's call" branch), but the admin login flow only fires on
// the admin listener and the listener bind itself is loopback by
// default so an internet attacker cannot reach the issuance path
// (REQ-OPS-ADMIN-LISTENER-02).
func (s *Server) scopeForLogin(p store.Principal) auth.ScopeSet {
	if s.listenerKind == "admin" {
		return auth.NewScopeSet(auth.ScopeAdmin)
	}
	return auth.NewScopeSet(auth.AllEndUserScopes...)
}

// safeRedirect reports whether target is a safe post-login redirect
// destination for the given listenerKind.
//
// For both kinds: absolute URLs, URLs with a Host component, and
// protocol-relative URLs starting with "//" are always rejected.
//
// For "admin": the target must begin with the UI prefix (e.g. "/ui/")
// or exactly equal it. This prevents a login on the admin listener
// from bouncing the browser outside the admin UI surface.
//
// For "public": the target may be "/", any hash-route starting with
// "/#", or any path beginning with the UI prefix. Other paths are
// rejected to prevent open-redirect abuse while still covering the
// full set of in-app destinations the suite can supply via ?return=.
func safeRedirect(target, prefix, listenerKind string) bool {
	if strings.HasPrefix(target, "//") {
		return false
	}
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	if u.IsAbs() || u.Host != "" {
		return false
	}
	if listenerKind == "public" {
		// Allow root, SPA hash routes, and /ui/... paths on the
		// public listener. Reject all other server-side paths so
		// an attacker can't embed an arbitrary /foo redirect.
		return u.Path == "/" ||
			u.Path == "" ||
			strings.HasPrefix(u.Path, prefix+"/") ||
			u.Path == prefix
	}
	// Admin listener: must stay within the UI prefix.
	return strings.HasPrefix(u.Path, prefix+"/") || u.Path == prefix
}

func (s *Server) handleLogoutPost(w http.ResponseWriter, r *http.Request) {
	// We deliberately do not require CSRF here: the worst case is a
	// cross-origin force-logout, which is a denial-of-service-of-self
	// the SameSite cookie already prevents. Skipping the CSRF check
	// avoids the chicken-and-egg of needing a valid session token to
	// log out of an expired session.
	s.clearSessionCookie(w)
	http.Redirect(w, r, s.pathPrefix+"/login", http.StatusSeeOther)
}

func humanAuthError(err error) string {
	switch {
	case errors.Is(err, directory.ErrUnauthorized):
		return "Email or password is incorrect."
	case errors.Is(err, directory.ErrRateLimited):
		return "Too many attempts. Please wait a minute and try again."
	case errors.Is(err, directory.ErrTOTPNotEnrolled):
		return "Two-factor authentication is not enrolled."
	default:
		return "Sign-in failed."
	}
}

// remoteHost is a tiny copy of the protoadmin helper. Deliberate
// duplication: protoadmin's helper is unexported and only one extra
// copy makes the dependency-acyclic. Two-caller duplication; a third
// caller earns a shared helper (mirrors the protosend/problem.go
// duplication-justification pattern).
func remoteHost(addr string) string {
	if len(addr) > 0 && addr[0] == '[' {
		for i := 1; i < len(addr); i++ {
			if addr[i] == ']' {
				return addr[1:i]
			}
		}
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
