package protoui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

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
	body := loginPageData{
		Redirect: r.URL.Query().Get("redirect"),
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
	redirect := r.PostForm.Get("redirect")

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
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
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

	// Mint the session.
	sess := session{
		PrincipalID: pid,
		ExpiresAt:   s.clk.Now().Add(s.cfg.TTL),
		CSRFToken:   newCSRFToken(),
	}
	s.setSessionCookie(w, sess)

	target := s.pathPrefix + "/dashboard"
	if redirect != "" && safeRedirect(redirect, s.pathPrefix) {
		target = redirect
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// safeRedirect rejects redirects that escape the UI or aim at a
// different host. The check is intentionally conservative: any URL
// that does not begin with the configured PathPrefix is rejected.
func safeRedirect(target, prefix string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	if u.IsAbs() || u.Host != "" {
		return false
	}
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
