package protoadmin

// telemetry_clientlog.go implements the self-service per-user opt-out for
// client-log behavioural telemetry (REQ-OPS-208, REQ-CLOG-06).
//
// PUT /api/v1/me/clientlog/telemetry_enabled
//   Body: {"enabled": true|false|null}
//   null clears the principal's override and falls back to the system default.
//   Audit-logged per REQ-ADM-300.
//
// The endpoint is self-service only: the caller may only modify their own
// principal's telemetry flag.  Admins who wish to change another principal's
// flag should use the standard PATCH /api/v1/principals/{pid} endpoint.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// telemetryEnabledRequest is the body accepted by
// PUT /api/v1/me/clientlog/telemetry_enabled.
// The Enabled field is a **bool so JSON null decodes to nil (clear override).
type telemetryEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// handlePutTelemetryEnabled handles PUT /api/v1/me/clientlog/telemetry_enabled.
//
// The caller updates their own telemetry opt-out flag.  A null body value
// clears the per-user override, causing the server to use the system default
// at next resolution.  The change is audit-logged and, when the caller is
// authenticated via a session cookie, the live session row is updated
// immediately so TelemetryGate.IsEnabled returns the new value without
// waiting for the next session creation or refresh.
func (s *Server) handlePutTelemetryEnabled(w http.ResponseWriter, r *http.Request) {
	caller, ok := principalFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return
	}

	// Decode the body.  A missing or null "enabled" field clears the override.
	var req telemetryEnabledRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest,
			"bad_request", "request body must be JSON {enabled: bool|null}", "")
		return
	}

	// Resolve the session ID so SetTelemetry can update the live session row.
	// If authentication was via Bearer key there is no session row, so we pass
	// the empty string and SetTelemetry skips the live-session update.
	sessID := s.sessionIDFromRequest(r)

	if err := s.dir.SetTelemetry(r.Context(), caller.ID, req.Enabled, sessID); err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}

	var afterStr string
	if req.Enabled != nil {
		if *req.Enabled {
			afterStr = "true"
		} else {
			afterStr = "false"
		}
	} else {
		afterStr = "null"
	}
	s.loggerFrom(r.Context()).InfoContext(r.Context(),
		"protoadmin.telemetry.set",
		"activity", observe.ActivityUser,
		"principal_id", uint64(caller.ID),
		"enabled", afterStr,
	)
	s.appendAudit(r.Context(), "principal.clientlog_telemetry.set",
		fmt.Sprintf("principal:%d", caller.ID),
		store.OutcomeSuccess,
		"",
		map[string]string{"enabled": afterStr},
	)

	w.WriteHeader(http.StatusNoContent)
}

// sessionIDFromRequest extracts the CSRF token from the session cookie, which
// doubles as the session row's primary key.  Returns "" when the request is
// not cookie-authenticated (Bearer API key) so callers can pass it safely to
// SetTelemetry without a separate auth-mode check.
func (s *Server) sessionIDFromRequest(r *http.Request) string {
	if len(s.opts.Session.SigningKey) < 32 {
		return ""
	}
	cookieName := s.opts.Session.CookieName
	if cookieName == "" {
		cookieName = "herold_admin_session"
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	sess, err := authsession.DecodeSession(c.Value, s.opts.Session.SigningKey, s.clk.Now())
	if err != nil {
		return ""
	}
	return sess.CSRFToken
}
