package protoadmin

// device_token.go implements the bearer-token grant native clients use to
// obtain a long-lived, revocable per-device access token without ever
// holding a browser session cookie (issue #199, docs/design/android/
// requirements/01-auth-and-token.md REQ-AND-AUTH-01/02).
//
// POST /api/v1/auth/device-token -- unauthenticated (it IS a credential
// exchange, like POST /api/v1/auth/login); accepts
// {email, password, totp_code?, device_label?} and returns a Bearer
// token in the "hk_..." namespace already accepted by protojmap and
// protoadmin's requireAuth (Authorization: Bearer hk_...). The minting
// logic (hashing, scope, TOTP handling) lives in
// internal/directory.Directory.IssueDeviceToken; this file is the thin
// REST adapter plus rate limiting and audit logging.
//
// The issued token is a store.APIKey row: it is listed and revoked
// through the existing self-service and admin surfaces
// (GET/DELETE /api/v1/api-keys, REQ-AUTH-SCOPE-04) -- there is no
// separate device-token revocation endpoint.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// deviceTokenRequest is the JSON body accepted by
// POST /api/v1/auth/device-token.
type deviceTokenRequest struct {
	// Email is the principal's canonical email address.
	Email string `json:"email"`
	// Password is the principal's plain-text password for verification.
	Password string `json:"password"`
	// TOTPCode is the current 6-digit TOTP code. Required only when the
	// principal has TOTP enrolled; omitted otherwise.
	TOTPCode string `json:"totp_code,omitempty"`
	// DeviceLabel is free text identifying the device/app instance
	// ("Pixel 8", "iPhone 15 - Mail"), shown back to the principal in the
	// self-service API-key list so they can tell which device to revoke.
	// Defaults to "device" when empty.
	DeviceLabel string `json:"device_label,omitempty"`
}

// deviceTokenResponse is the JSON body returned on success. The plaintext
// token is shown exactly once; subsequent listings (GET /api/v1/api-keys)
// show only id/label/scope, never the token value.
type deviceTokenResponse struct {
	APIKeyID    uint64   `json:"api_key_id"`
	PrincipalID uint64   `json:"principal_id"`
	Label       string   `json:"label"`
	Token       string   `json:"token"`
	Scope       []string `json:"scope"`
	CreatedAt   string   `json:"created_at"`
}

// handleIssueDeviceToken handles POST /api/v1/auth/device-token
// (issue #199). The endpoint is unauthenticated -- it IS the credential
// exchange -- and rate-limited per source IP the same way
// POST /api/v1/auth/login is, before any principal is resolved so
// brute-force is throttled by source before the directory's own
// per-(email,source) budget is even consulted.
func (s *Server) handleIssueDeviceToken(w http.ResponseWriter, r *http.Request) {
	ipKey := "device-token-ip:" + remoteHost(r.RemoteAddr)
	if !s.checkRateLimit(w, r, ipKey) {
		return
	}

	var req deviceTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "request body must be JSON {email, password}", "")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "email and password are required", "")
		return
	}

	ctx := directory.WithAuthSource(r.Context(), remoteHost(r.RemoteAddr))

	plaintext, key, err := s.dir.IssueDeviceToken(ctx, req.Email, req.Password, req.TOTPCode, req.DeviceLabel)
	if err != nil {
		switch {
		case errors.Is(err, directory.ErrTOTPRequired):
			// Mirrors REQ-AUTH-JSON-LOGIN: absent or incorrect totp_code
			// both surface as step_up_required once the password has
			// already been verified, so the client can re-prompt for a
			// code without discarding the password it already has in
			// memory (REQ-AS-15 parity for a native client).
			s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.device_token_step_up",
				"activity", observe.ActivityAudit, "email", req.Email)
			s.auditLoginFailure(r, req.Email, 0, "totp step-up required")
			writeProblemWithExtras(w, r, http.StatusUnauthorized,
				"step_up_required", "TOTP code required", "",
				map[string]any{"step_up_required": true})
			return
		case errors.Is(err, directory.ErrRateLimited):
			s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.device_token_rate_limited",
				"activity", observe.ActivityAudit, "email", req.Email)
			s.auditLoginFailure(r, req.Email, 0, "rate limited")
			writeProblem(w, r, http.StatusTooManyRequests,
				"rate_limited", "too many attempts; please wait and try again", "")
			return
		default:
			s.loggerFrom(ctx).WarnContext(ctx, "protoadmin.auth.device_token_failed",
				"activity", observe.ActivityAudit, "email", req.Email)
			s.auditLoginFailure(r, req.Email, 0, humanLoginError(err))
			writeProblem(w, r, http.StatusUnauthorized,
				"unauthorized", humanLoginError(err), "")
			return
		}
	}

	s.loggerFrom(ctx).InfoContext(ctx, "protoadmin.auth.device_token_issued",
		"activity", observe.ActivityAudit,
		"principal_id", uint64(key.PrincipalID),
		"api_key_id", uint64(key.ID))
	s.appendAudit(ctx, "auth.device_token.issue",
		"principal:"+req.Email,
		store.OutcomeSuccess, "",
		map[string]string{
			"remote":     remoteHost(r.RemoteAddr),
			"api_key_id": strconv.FormatUint(uint64(key.ID), 10),
		})

	var scopes []string
	if err := json.Unmarshal([]byte(key.ScopeJSON), &scopes); err != nil {
		scopes = nil // best-effort; the token itself was already minted correctly
	}
	label, _ := directory.DeviceTokenLabel(key.Name)

	writeJSON(w, http.StatusCreated, deviceTokenResponse{
		APIKeyID:    uint64(key.ID),
		PrincipalID: uint64(key.PrincipalID),
		Label:       label,
		Token:       plaintext,
		Scope:       scopes,
		CreatedAt:   key.CreatedAt.Format(time.RFC3339),
	})
}
