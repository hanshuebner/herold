package protoui

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// CSRFFormField is the hidden form input name used by every UI form
// that POSTs.
const CSRFFormField = "_csrf"

// newCSRFToken returns a 24-byte random URL-safe token. 24 bytes ->
// 192 bits — comfortably above the recommended 128-bit minimum and
// short enough to land in a cookie without trimming.
func newCSRFToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// requireCSRF wraps a state-changing handler with the double-submit
// CSRF check.
//
// The cookie token is the source of truth (it was issued at session
// creation and is bound to the session signature). The form posts the
// same token in a hidden field; the middleware compares the two with
// hmac.Equal so a timing channel does not leak the cookie's bytes
// during a guess loop.
//
// Methods exempt from CSRF: GET / HEAD / OPTIONS (RFC 7231 safe
// methods, no side effects). Every other method is gated.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next(w, r)
			return
		}
		// Cookie token (server's view).
		c, err := r.Cookie(s.cfg.CSRFCookieName)
		if err != nil || c.Value == "" {
			s.renderCSRFError(w, r, "missing CSRF cookie")
			return
		}
		// Form token (client's view). For multipart forms / HTMX
		// triggers we accept either ParseForm (form-urlencoded) or the
		// X-CSRF-Token header — HTMX 1.x does not always re-attach the
		// cookie value on hx-post, so the header path keeps the
		// boilerplate light for hx-get -> swap-into-form flows.
		if err := r.ParseForm(); err != nil {
			s.renderCSRFError(w, r, "form parse failed")
			return
		}
		formTok := r.PostForm.Get(CSRFFormField)
		if formTok == "" {
			formTok = r.Header.Get("X-CSRF-Token")
		}
		if formTok == "" {
			s.renderCSRFError(w, r, "missing CSRF token in form")
			return
		}
		if !hmac.Equal([]byte(c.Value), []byte(formTok)) {
			s.renderCSRFError(w, r, "CSRF token mismatch")
			return
		}
		next(w, r)
	}
}

// renderCSRFError writes a 403 with a flash-style page so the operator
// sees a useful message instead of a bare problem JSON. We do not log
// these at warn level: the most common cause is a stale tab whose
// session expired between page load and form submit, which is
// expected.
func (s *Server) renderCSRFError(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = s.tmpl.ExecuteTemplate(w, "layout.html", &pageData{
		Title:    "Forbidden",
		Active:   "",
		Flash:    &flashMessage{Kind: "error", Body: "Forbidden: " + detail + ". Reload the page and try again."},
		BodyTmpl: "csrf_error_body",
		PathBase: s.pathPrefix,
	})
}
