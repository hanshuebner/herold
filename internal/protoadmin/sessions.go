package protoadmin

// sessions.go — REST handlers for session list + revocation (REQ-AUTH-77,
// issue #80). Self-service surface:
//
//	GET    /api/v1/auth/sessions           list the calling principal's sessions
//	DELETE /api/v1/auth/sessions/{id}      revoke one session by session_id
//
// Admin surface (requires step-up elevation):
//
//	GET    /api/v1/admin/principals/{pid}/sessions              list any principal's sessions
//	DELETE /api/v1/admin/principals/{pid}/sessions/{session_id} revoke any session
//
// Revocation tombstones the row (revoked_at_us = now) with a short
// expires_at so the eviction sweeper cleans it up within tombstoneTTL.
// The next request on a tombstoned cookie receives 401
// {"type": "session_revoked"} (REQ-AUTH-76) rather than a generic 401.
// If the revoked session is the caller's current session, cookies are
// cleared from the response and the session row is also deleted via
// the normal logout path.

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/store"
)

// tombstoneTTL is the duration a revoked session row remains in the store
// before the EvictExpiredSessions sweeper removes it. Long enough for any
// in-flight requests carrying the cookie to receive the typed 401; short
// enough that the row does not accumulate indefinitely.
const tombstoneTTLMicros = int64(10 * 60 * 1e6) // 10 minutes in microseconds

// sessionDTO is the JSON representation of a session returned by the list
// and (implicitly) by revocation responses. All timestamps are RFC 3339.
type sessionDTO struct {
	SessionID  string `json:"session_id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	LastSeenIP string `json:"last_seen_ip"`
	UserAgent  string `json:"user_agent"`
	IsCurrent  bool   `json:"is_current"`
}

func toSessionDTO(row store.SessionRow, currentSessionID string) sessionDTO {
	return sessionDTO{
		SessionID:  row.SessionID,
		CreatedAt:  row.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		LastSeenAt: row.LastSeenAt.Format("2006-01-02T15:04:05Z07:00"),
		LastSeenIP: row.LastSeenIP,
		UserAgent:  row.UserAgent,
		IsCurrent:  row.SessionID == currentSessionID,
	}
}

// handleListSessions handles GET /api/v1/auth/sessions.
// Returns a paginated list of the caller's active sessions; the current
// session is identified by the is_current field.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	now := s.clk.Now()

	rows, err := s.store.Meta().ListSessionsByPrincipal(r.Context(), caller.ID, now.UnixMicro())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	currentSessionID := s.sessionIDFromRequest(r)
	items := make([]sessionDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toSessionDTO(row, currentSessionID))
	}
	writeJSON(w, http.StatusOK, pageDTO[sessionDTO]{Items: items, Next: nil})
}

// handleRevokeSession handles DELETE /api/v1/auth/sessions/{session_id}.
// Tombstones the named session. If the named session is the caller's current
// session, the response also clears the session cookies (effectively logging
// the caller out). Responds 204 No Content on success.
func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"session_id path parameter is required", "")
		return
	}

	now := s.clk.Now()
	if err := s.store.Meta().TombstoneSession(r.Context(), sessionID, caller.ID, now.UnixMicro(), tombstoneTTLMicros); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Revoke any active elevation for this session as well (REQ-AUTH-77).
	_ = s.store.Meta().DeleteElevation(r.Context(), sessionID)

	s.appendAudit(r.Context(), "auth.session.revoke",
		fmt.Sprintf("session/%s", sessionID),
		store.OutcomeSuccess, "", map[string]string{
			"principal_id": strconv.FormatUint(uint64(caller.ID), 10),
		})

	// If the caller just revoked their own current session, clear cookies.
	currentSessionID := s.sessionIDFromRequest(r)
	if sessionID == currentSessionID {
		cfg := s.sessionConfig()
		authsession.ClearSessionCookies(w, cfg)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleAdminListSessions handles GET /api/v1/admin/principals/{pid}/sessions.
// Returns a paginated list of any principal's active sessions. Requires admin
// elevation (enforced by the authAdmin middleware in routes.go).
func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	now := s.clk.Now()

	rows, err := s.store.Meta().ListSessionsByPrincipal(r.Context(), pid, now.UnixMicro())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// The admin is not acting as the target principal so no session is
	// marked is_current from the target's perspective.
	items := make([]sessionDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toSessionDTO(row, ""))
	}
	writeJSON(w, http.StatusOK, pageDTO[sessionDTO]{Items: items, Next: nil})
}

// handleAdminRevokeSession handles DELETE /api/v1/admin/principals/{pid}/sessions/{session_id}.
// Tombstones any session belonging to the target principal. Requires admin
// elevation (enforced by the authAdmin middleware in routes.go). Audit log
// records the acting admin principal as actor and the target session as subject.
func (s *Server) handleAdminRevokeSession(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"session_id path parameter is required", "")
		return
	}

	caller, _ := principalFrom(r.Context())
	now := s.clk.Now()
	if err := s.store.Meta().TombstoneSession(r.Context(), sessionID, pid, now.UnixMicro(), tombstoneTTLMicros); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Revoke any active elevation for the target session as well.
	_ = s.store.Meta().DeleteElevation(r.Context(), sessionID)

	s.appendAudit(r.Context(), "auth.session.revoke",
		fmt.Sprintf("session/%s", sessionID),
		store.OutcomeSuccess, "", map[string]string{
			"actor_principal_id":  strconv.FormatUint(uint64(caller.ID), 10),
			"target_principal_id": strconv.FormatUint(uint64(pid), 10),
		})

	w.WriteHeader(http.StatusNoContent)
}
