package authsession

import (
	"net/http"
	"time"
)

// WriteSessionCookie writes both the session cookie and the matching
// CSRF cookie to w using the supplied SessionConfig. The CSRF cookie
// is intentionally NOT HttpOnly so the SPA's JS can read it and attach
// it as X-CSRF-Token on mutating requests (REQ-AUTH-CSRF).
//
// Used by protologin (public-listener JSON login) and protoadmin
// (admin-listener JSON login, REQ-AUTH-SESSION-REST). The session
// cookie Path is "/" so the same cookie accompanies /api/v1/* and
// /admin/* on the same listener (REQ-AUTH-COOKIE-PATH).
func WriteSessionCookie(w http.ResponseWriter, cfg SessionConfig, sess Session) {
	encoded := encodeSession(sess, cfg.SigningKey)
	maxAge := int(cfg.TTL.Seconds())
	if maxAge <= 0 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   cfg.SecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CSRFCookieName,
		Value:    sess.CSRFToken,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   cfg.SecureCookies,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookies writes Set-Cookie headers that expire both the
// session cookie and the CSRF cookie immediately. The cookie path MUST
// match the path used at issuance (WriteSessionCookie uses "/") so the
// browser drops the right cookies (REQ-AUTH-COOKIE-PATH).
func ClearSessionCookies(w http.ResponseWriter, cfg SessionConfig) {
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		Secure:   cfg.SecureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CSRFCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		Secure:   cfg.SecureCookies,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
}
