package protoadmin

import (
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/store"
)

// totpEnrollResponse is the POST /totp/enroll response body.
type totpEnrollResponse struct {
	Secret          string `json:"secret"`
	ProvisioningURI string `json:"provisioning_uri"`
}

func (s *Server) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	secret, uri, err := s.dir.EnrollTOTP(r.Context(), pid)
	if err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, totpEnrollResponse{Secret: secret, ProvisioningURI: uri})
}

type totpConfirmRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	var req totpConfirmRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Code == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"code is required", "")
		return
	}
	if err := s.dir.ConfirmTOTP(r.Context(), pid, req.Code); err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	// Consume any one-shot keys owned by this principal.  The bootstrap and
	// recovery paths mint one-shot keys so the operator can reach this
	// endpoint exactly once; after a successful confirm the key must not be
	// reusable (REQ-AUTH-44, re #21).  Failures are logged but do not fail
	// the request: TOTP is already enrolled, so the key's one-shot purpose
	// is fulfilled regardless of whether the cleanup succeeds.
	if _, err := s.store.Meta().DeleteOneShotAPIKeysByPrincipal(r.Context(), pid); err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.totp.consume_oneshot_keys",
			"activity", "audit",
			"principal_id", pid,
			"err", err)
	}
	s.appendAudit(r.Context(), "principal.totp.confirm",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

type totpDisableRequest struct {
	CurrentPassword string `json:"current_password"`
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFrom(r.Context())
	if !requireSelfOrAdmin(w, r, caller, pid) {
		return
	}
	// Self-service path: gate on TOTP step-up when the caller is disabling
	// their own TOTP and has it enrolled (REQ-AUTH-78, issue #79).
	// Admin-disabling-for-other is not gated here.
	if caller.ID == pid {
		if !s.requireSelfServiceElevation(w, r, caller) {
			return
		}
	}
	var req totpDisableRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.CurrentPassword == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"current_password is required", "")
		return
	}
	if err := s.dir.DisableTOTP(r.Context(), pid, req.CurrentPassword); err != nil {
		s.writeDirectoryError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "principal.totp.disable",
		fmt.Sprintf("principal:%d", pid),
		store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}
