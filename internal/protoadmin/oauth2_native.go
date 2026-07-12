package protoadmin

// oauth2_native.go implements the OAuth2 authorization-code + PKCE grant
// for native clients (issue #199, REQ-AND-AUTH-01/02): a system browser
// / Custom Tab drives GET/POST /oauth2/authorize (herold's own login
// page, reusing password + TOTP verification), then the client
// exchanges the resulting code at POST /oauth2/token for a short-lived
// access token + rotating refresh token. All business logic (client
// registry, PKCE verification, code/token issuance, rotation, reuse
// detection) lives in internal/directory (oauth2.go, oauth2client.go,
// oauth2_authreq.go); this file is the thin REST/HTML adapter plus rate
// limiting, CSRF, and audit logging -- mirroring the existing
// device_token.go split between directory logic and protoadmin surface.
//
// Endpoints (unauthenticated -- these ARE the auth boundary, mounted the
// same way POST /api/v1/auth/login and POST /api/v1/auth/device-token
// are):
//
//	GET  /oauth2/authorize   Renders the login form. Validates
//	                         client_id/redirect_uri/PKCE up front so a
//	                         forged or misconfigured request never
//	                         reaches the credential step, and never
//	                         redirects to an unvalidated redirect_uri
//	                         (no open redirect).
//	POST /oauth2/authorize   Submits the login form. CSRF-checked via a
//	                         double-submit cookie whose value is also
//	                         embedded (signed) inside the request token,
//	                         so the cookie, the hidden form field, and the
//	                         signed token's own CSRFToken must all agree.
//	POST /oauth2/token       The token endpoint (RFC 6749 §3.2):
//	                         grant_type=authorization_code (+PKCE
//	                         code_verifier) or grant_type=refresh_token.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// oauthCSRFCookieName carries the double-submit CSRF token for the
// /oauth2/authorize login form. Distinct from the Suite/admin session
// CSRF cookie (authsession) because this flow is not cookie-session
// authenticated at all -- it is the credential exchange itself.
const oauthCSRFCookieName = "herold_oauth2_csrf"

// handleOAuthAuthorize dispatches GET/POST /oauth2/authorize. Registered
// as one route (routes.go) so both methods share the same path per RFC
// 6749's authorization endpoint.
func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleOAuthAuthorizeGet(w, r)
	case http.MethodPost:
		s.handleOAuthAuthorizePost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeProblem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
	}
}

// handleOAuthAuthorizeGet validates the authorization request and
// renders the login form. Errors that occur before redirect_uri has
// been validated are reported directly (never as a redirect); errors
// after that point are reported to the client via a redirect carrying
// error/error_description (RFC 6749 §4.1.2.1).
func (s *Server) handleOAuthAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")

	client, ok := directory.LookupOAuthClient(clientID)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "invalid_client", "unknown client_id", "")
		return
	}
	if !directory.ValidateRedirectURI(client, redirectURI) {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client", "")
		return
	}

	state := q.Get("state")
	redirectError := func(errCode, desc string) {
		u, _ := url.Parse(redirectURI)
		qq := u.Query()
		qq.Set("error", errCode)
		if desc != "" {
			qq.Set("error_description", desc)
		}
		if state != "" {
			qq.Set("state", state)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}

	if q.Get("response_type") != "code" {
		redirectError("unsupported_response_type", "response_type must be \"code\"")
		return
	}
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	if codeChallenge == "" || codeChallengeMethod != "S256" {
		redirectError("invalid_request", "code_challenge is required and code_challenge_method must be S256 (PKCE is mandatory)")
		return
	}

	csrfTok, err := directory.NewAuthorizeRequestCSRFToken(rand.Reader)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to prepare login form", "")
		return
	}
	req := directory.AuthorizeRequest{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               q.Get("scope"),
		State:               state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		CSRFToken:           csrfTok,
		ExpiresAt:           s.clk.Now().UTC().Add(directory.AuthorizeRequestTTL),
	}
	encoded := s.dir.EncodeAuthorizeRequest(req)

	http.SetCookie(w, &http.Cookie{
		Name:     oauthCSRFCookieName,
		Value:    csrfTok,
		Path:     "/oauth2/authorize",
		HttpOnly: true,
		Secure:   s.opts.Session.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(directory.AuthorizeRequestTTL.Seconds()),
	})

	s.renderOAuthLoginForm(w, encoded, csrfTok, "", false)
}

// oauthLoginFormTemplate renders herold's native-client sign-in page.
// autocomplete attributes are set so platform password managers /
// passkey autofill can act on the fields from a system browser / Custom
// Tab context (REQ-AND-AUTH-01, REQ-AND-AUTH-12).
var oauthLoginFormTemplate = template.Must(template.New("oauth2-login").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
</head>
<body>
<h1>Sign in</h1>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="POST" action="/oauth2/authorize" autocomplete="on">
  <input type="hidden" name="req" value="{{.Req}}">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <p>
    <label for="email">Email</label><br>
    <input type="email" id="email" name="email" autocomplete="username" required autofocus>
  </p>
  <p>
    <label for="password">Password</label><br>
    <input type="password" id="password" name="password" autocomplete="current-password" required>
  </p>
  {{if .ShowTOTP}}
  <p>
    <label for="totp_code">Authentication code</label><br>
    <input type="text" id="totp_code" name="totp_code" inputmode="numeric" pattern="[0-9]*" autocomplete="one-time-code" maxlength="6">
  </p>
  {{end}}
  <p><button type="submit">Sign in</button></p>
</form>
</body>
</html>`))

func (s *Server) renderOAuthLoginForm(w http.ResponseWriter, encodedReq, csrf, errMsg string, showTOTP bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_ = oauthLoginFormTemplate.Execute(w, struct {
		Req      string
		CSRF     string
		Error    string
		ShowTOTP bool
	}{encodedReq, csrf, errMsg, showTOTP})
}

// handleOAuthAuthorizePost processes the submitted login form.
func (s *Server) handleOAuthAuthorizePost(w http.ResponseWriter, r *http.Request) {
	ipKey := "oauth2-authorize-ip:" + remoteHost(r.RemoteAddr)
	if !s.checkRateLimit(w, r, ipKey) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad_request", "malformed form body", "")
		return
	}
	encodedReq := r.FormValue("req")
	formCSRF := r.FormValue("csrf")
	email := r.FormValue("email")
	password := r.FormValue("password")
	totpCode := r.FormValue("totp_code")

	cookie, cookieErr := r.Cookie(oauthCSRFCookieName)
	if cookieErr != nil || formCSRF == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formCSRF)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "csrf_mismatch", "CSRF check failed; please restart sign-in", "")
		return
	}

	req, err := s.dir.DecodeAuthorizeRequest(encodedReq, s.clk.Now().UTC())
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "sign-in request expired or invalid; please restart sign-in", "")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.CSRFToken), []byte(formCSRF)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "csrf_mismatch", "CSRF check failed; please restart sign-in", "")
		return
	}

	ctx := directory.WithAuthSource(r.Context(), remoteHost(r.RemoteAddr))
	code, err := s.dir.IssueAuthorizationCode(ctx, email, password, totpCode, req)
	if err != nil {
		switch {
		case errors.Is(err, directory.ErrTOTPRequired):
			s.auditAuthFailure(r, "auth.oauth2.authorize", email, 0, "totp step-up required")
			s.renderOAuthLoginForm(w, encodedReq, formCSRF, "Enter your 6-digit authentication code.", true)
			return
		case errors.Is(err, directory.ErrRateLimited):
			s.auditAuthFailure(r, "auth.oauth2.authorize", email, 0, "rate limited")
			writeProblem(w, r, http.StatusTooManyRequests, "rate_limited", "too many attempts; please wait and try again", "")
			return
		default:
			s.auditAuthFailure(r, "auth.oauth2.authorize", email, 0, humanLoginError(err))
			s.renderOAuthLoginForm(w, encodedReq, formCSRF, humanLoginError(err), false)
			return
		}
	}

	s.appendAudit(ctx, "auth.oauth2.authorize", "principal:"+email, store.OutcomeSuccess, "",
		map[string]string{"remote": remoteHost(r.RemoteAddr), "client_id": req.ClientID})

	// Clear the CSRF cookie: the authorize request has been consumed.
	http.SetCookie(w, &http.Cookie{
		Name: oauthCSRFCookieName, Value: "", Path: "/oauth2/authorize",
		HttpOnly: true, Secure: s.opts.Session.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})

	u, _ := url.Parse(req.RedirectURI)
	qq := u.Query()
	qq.Set("code", code)
	if req.State != "" {
		qq.Set("state", req.State)
	}
	u.RawQuery = qq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// oauthTokenResponse is the RFC 6749 §5.1 successful token response.
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
}

// writeOAuthTokenError writes an RFC 6749 §5.2 error response.
func writeOAuthTokenError(w http.ResponseWriter, status int, errCode, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description,omitempty"`
	}{errCode, desc})
}

// handleOAuthToken implements POST /oauth2/token (RFC 6749 §3.2).
func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	ipKey := "oauth2-token-ip:" + remoteHost(r.RemoteAddr)
	if !s.checkRateLimit(w, r, ipKey) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthTokenError(w, http.StatusBadRequest, "invalid_request", "malformed request body")
		return
	}

	grantType := r.FormValue("grant_type")
	clientID := r.FormValue("client_id")
	if clientID == "" {
		writeOAuthTokenError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}

	ctx := directory.WithAuthSource(r.Context(), remoteHost(r.RemoteAddr))

	var result directory.OAuthTokenResult
	var err error
	switch grantType {
	case "authorization_code":
		code := r.FormValue("code")
		redirectURI := r.FormValue("redirect_uri")
		codeVerifier := r.FormValue("code_verifier")
		if code == "" || redirectURI == "" || codeVerifier == "" {
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_request",
				"code, redirect_uri, and code_verifier are required")
			return
		}
		result, err = s.dir.ExchangeAuthorizationCode(ctx, clientID, code, redirectURI, codeVerifier)
	case "refresh_token":
		refreshToken := r.FormValue("refresh_token")
		if refreshToken == "" {
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
			return
		}
		result, err = s.dir.RefreshOAuthToken(ctx, clientID, refreshToken)
	case "":
		writeOAuthTokenError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	default:
		writeOAuthTokenError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		return
	}

	if err != nil {
		switch {
		case errors.Is(err, directory.ErrUnknownOAuthClient):
			s.appendAudit(ctx, "auth.oauth2.token", "client:"+clientID, store.OutcomeFailure, "unknown client", nil)
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		case errors.Is(err, directory.ErrPKCEMismatch):
			s.appendAudit(ctx, "auth.oauth2.token", "client:"+clientID, store.OutcomeFailure, "pkce mismatch", nil)
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		case errors.Is(err, directory.ErrRefreshReuse):
			s.appendAudit(ctx, "auth.oauth2.token", "client:"+clientID, store.OutcomeFailure, "refresh token reuse detected; family revoked", nil)
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_grant", "refresh token already used")
		case errors.Is(err, directory.ErrInvalidGrant):
			s.appendAudit(ctx, "auth.oauth2.token", "client:"+clientID, store.OutcomeFailure, "invalid grant", nil)
			writeOAuthTokenError(w, http.StatusBadRequest, "invalid_grant", "the provided authorization grant is invalid, expired, or revoked")
		default:
			s.loggerFrom(ctx).Error("protoadmin.oauth2.token_failed", "err", err)
			writeOAuthTokenError(w, http.StatusInternalServerError, "server_error", "")
		}
		return
	}

	scopeStrs := make([]string, len(result.Scope))
	for i, sc := range result.Scope {
		scopeStrs[i] = string(sc)
	}

	writeJSON(w, http.StatusOK, oauthTokenResponse{
		AccessToken:  result.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    result.ExpiresIn,
		RefreshToken: result.RefreshToken,
		Scope:        strings.Join(scopeStrs, " "),
	})
}
