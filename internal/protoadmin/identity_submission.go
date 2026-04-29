package protoadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// ExternalProbe is the function signature that handlePutSubmission calls to
// validate credentials before persisting. The default implementation calls
// extsubmit.Submitter.Probe; tests inject a fake via Options.ExternalProbe.
//
// The sub argument already has sealed credential fields (PasswordCT /
// OAuthAccessCT etc.) because the prober must unseal them internally. The
// DataKey on the Submitter must match the key used to seal.
type ExternalProbe func(ctx context.Context, sub store.IdentitySubmission) extsubmit.Outcome

// writeProbeFailed writes a 422 RFC 7807 response for a probe failure. The
// type is "external_submission_probe_failed" and the body carries the failure
// category and diagnostic text. Nothing is written to the store.
func writeProbeFailed(w http.ResponseWriter, r *http.Request, outcome extsubmit.Outcome) {
	category := string(outcome.State)
	diag := outcome.Diagnostic
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(struct {
		Type       string `json:"type"`
		Title      string `json:"title"`
		Status     int    `json:"status"`
		Instance   string `json:"instance,omitempty"`
		Category   string `json:"category"`
		Diagnostic string `json:"diagnostic,omitempty"`
	}{
		Type:       problemTypeBase + "external_submission_probe_failed",
		Title:      "external submission probe failed",
		Status:     http.StatusUnprocessableEntity,
		Instance:   r.URL.Path,
		Category:   category,
		Diagnostic: diag,
	})
}

// resolveIdentityOwner looks up the JMAP identity by id and returns its
// owning PrincipalID. Returns ErrNotFound when the identity does not exist.
func resolveIdentityOwner(ctx context.Context, meta store.Metadata, identityID string) (store.PrincipalID, error) {
	identity, err := meta.GetJMAPIdentity(ctx, identityID)
	if err != nil {
		return 0, err
	}
	return identity.PrincipalID, nil
}

// handleGetSubmission implements GET /api/v1/identities/{id}/submission.
// Returns the submission config for the identity without any credential
// material (REQ-AUTH-EXT-SUBMIT-04). Gated by requireSelfOnly: the caller
// must own the identity.
func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	ownerID, err := resolveIdentityOwner(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, ownerID) {
		return
	}

	sub, err := s.store.Meta().GetIdentitySubmission(r.Context(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, submissionGetResponse{Configured: false})
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, submissionGetResponse{
		Configured:       true,
		SubmitHost:       sub.SubmitHost,
		SubmitPort:       sub.SubmitPort,
		SubmitSecurity:   sub.SubmitSecurity,
		SubmitAuthMethod: sub.SubmitAuthMethod,
		State:            string(sub.State),
	})
}

// handlePutSubmission implements PUT /api/v1/identities/{id}/submission.
//
// Execution order (per architectural decision 2):
//  1. Decode and validate the request body.
//  2. Seal the credential material.
//  3. Run the probe (ExternalProbe). If it fails, write 422 and discard the
//     sealed credential — nothing is written to the store.
//  4. Upsert the row in the store.
//  5. Emit an audit log entry.
//
// Gated by requireSelfOnly (REQ-AUTH-EXT-SUBMIT-04).
func (s *Server) handlePutSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	ownerID, err := resolveIdentityOwner(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, ownerID) {
		return
	}

	var req submissionPutRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if err := validatePutRequest(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}

	dataKey := s.opts.ExternalSubmissionDataKey
	if len(dataKey) == 0 {
		writeProblem(w, r, http.StatusServiceUnavailable, "not_configured",
			"external submission is not configured on this server; set [server.secrets].data_key_ref", "")
		return
	}

	// Seal the credential material.
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       req.SubmitHost,
		SubmitPort:       req.SubmitPort,
		SubmitSecurity:   req.SubmitSecurity,
		SubmitAuthMethod: req.SubmitAuthMethod,
		State:            store.IdentitySubmissionStateOK,
	}
	switch req.SubmitAuthMethod {
	case "password":
		ct, serr := secrets.Seal(dataKey, []byte(req.Password))
		if serr != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.submission.seal_failed",
				"activity", observe.ActivityInternal, "err", serr)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal credential", "")
			return
		}
		sub.PasswordCT = ct
		// Use AuthUser as the probe user if provided.
		if req.AuthUser != "" {
			sub.OAuthClientID = req.AuthUser
		}
	case "oauth2":
		at, serr := secrets.Seal(dataKey, []byte(req.OAuthAccessToken))
		if serr != nil {
			s.loggerFrom(r.Context()).Error("protoadmin.submission.seal_failed",
				"activity", observe.ActivityInternal, "err", serr)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal access token", "")
			return
		}
		sub.OAuthAccessCT = at
		if req.OAuthRefreshToken != "" {
			rt, serr := secrets.Seal(dataKey, []byte(req.OAuthRefreshToken))
			if serr != nil {
				s.loggerFrom(r.Context()).Error("protoadmin.submission.seal_failed",
					"activity", observe.ActivityInternal, "err", serr)
				writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to seal refresh token", "")
				return
			}
			sub.OAuthRefreshCT = rt
		}
		sub.OAuthTokenEndpoint = req.OAuthTokenEndpoint
		sub.OAuthClientID = req.OAuthClientID
		if req.AuthUser != "" {
			sub.OAuthClientID = req.AuthUser
		}
	}

	// Probe the external endpoint before persisting. On any failure, emit
	// an audit entry and return 422 — nothing is written to the store.
	correlationID := fmt.Sprintf("probe:%s", newRequestID())
	probeOutcome := s.opts.ExternalProbe(r.Context(), sub)
	if probeOutcome.State != extsubmit.OutcomeOK {
		s.appendAudit(r.Context(), "submission.external.failure",
			fmt.Sprintf("identity:%s", identityID),
			store.OutcomeFailure,
			fmt.Sprintf("probe failed: %s", probeOutcome.Diagnostic),
			map[string]string{
				"category":       string(probeOutcome.State),
				"correlation_id": correlationID,
				"auth_method":    req.SubmitAuthMethod,
			})
		writeProbeFailed(w, r, probeOutcome)
		return
	}

	// Persist.
	if err := s.store.Meta().UpsertIdentitySubmission(r.Context(), sub); err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	s.appendAudit(r.Context(), "identity.submission.set",
		fmt.Sprintf("identity:%s", identityID),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", caller.ID),
			"identity_id":  identityID,
			"auth_method":  req.SubmitAuthMethod,
		})

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSubmission implements DELETE /api/v1/identities/{id}/submission.
// Removes the submission config; subsequent submissions revert to herold's
// outbound queue (REQ-AUTH-EXT-SUBMIT-04). Gated by requireSelfOnly.
func (s *Server) handleDeleteSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	ownerID, err := resolveIdentityOwner(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, ownerID) {
		return
	}

	if err := s.store.Meta().DeleteIdentitySubmission(r.Context(), identityID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "submission config not found", "")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	s.appendAudit(r.Context(), "identity.submission.delete",
		fmt.Sprintf("identity:%s", identityID),
		store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", caller.ID),
			"identity_id":  identityID,
		})

	w.WriteHeader(http.StatusNoContent)
}

// validatePutRequest checks the required fields of a submissionPutRequest.
func validatePutRequest(req *submissionPutRequest) error {
	if req.SubmitHost == "" {
		return errors.New("submit_host is required")
	}
	if req.SubmitPort <= 0 || req.SubmitPort > 65535 {
		return fmt.Errorf("submit_port %d is out of range (1..65535)", req.SubmitPort)
	}
	switch req.SubmitSecurity {
	case "implicit_tls", "starttls", "none":
		// ok
	default:
		return fmt.Errorf("submit_security %q must be one of: implicit_tls, starttls, none", req.SubmitSecurity)
	}
	switch req.SubmitAuthMethod {
	case "password":
		if req.Password == "" {
			return errors.New("password is required when submit_auth_method is password")
		}
	case "oauth2":
		if req.OAuthAccessToken == "" {
			return errors.New("oauth_access_token is required when submit_auth_method is oauth2")
		}
		if req.OAuthClientID == "" && req.AuthUser == "" {
			return errors.New("oauth_client_id (or auth_user) is required when submit_auth_method is oauth2")
		}
	default:
		return fmt.Errorf("submit_auth_method %q must be one of: password, oauth2", req.SubmitAuthMethod)
	}
	return nil
}

// noopProbe is the default ExternalProbe used when
// Options.ExternalProbe is nil and no Submitter is configured. It always
// returns OutcomeOK, which means PUT will persist without a live probe. This
// is the correct behaviour for operators who have not configured
// [server.secrets].data_key_ref: the data-key gate fires first (503), so
// noopProbe is never reachable in that code path. In test builds where
// the data key IS set but no real SMTP server is available, tests inject
// their own probe via Options.ExternalProbe.
func noopProbe(_ context.Context, _ store.IdentitySubmission) extsubmit.Outcome {
	return extsubmit.Outcome{State: extsubmit.OutcomeOK}
}

// DefaultProbeFromSubmitter wraps a *extsubmit.Submitter as an ExternalProbe.
// admin/server.go uses this to pass the real probe into protoadmin.Options
// (REQ-MAIL-SUBMIT-03 — the probe validates credentials before persisting).
func DefaultProbeFromSubmitter(sub *extsubmit.Submitter) ExternalProbe {
	return func(ctx context.Context, row store.IdentitySubmission) extsubmit.Outcome {
		return sub.Probe(ctx, row)
	}
}
