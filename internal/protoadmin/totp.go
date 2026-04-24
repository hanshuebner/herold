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
