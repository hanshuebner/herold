package protoadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// ExternalProbe is the function signature that handlePutSubmission calls to
// validate credentials and send capability before persisting. The default
// implementation calls extsubmit.Submitter.Probe; tests inject a fake via
// Options.ExternalProbe.
//
// The sub argument already has sealed credential fields (PasswordCT /
// OAuthAccessCT etc.) because the prober must unseal them internally. The
// DataKey on the Submitter must match the key used to seal. fromAddr is the
// identity's own addr-spec, used as the MAIL FROM / RCPT TO target in the
// send-capability probe (REQ-AUTH-EXT-SUBMIT-11).
type ExternalProbe func(ctx context.Context, sub store.IdentitySubmission, fromAddr string) extsubmit.Outcome

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
//
// The literal id "default" is the per-principal synthesised identity and
// has no row in jmap_identities. Callers that need to act on the default
// identity must short-circuit before calling this function (the
// submission table FKs jmap_identities, so the default cannot persist a
// submission row at all).
func resolveIdentityOwner(ctx context.Context, meta store.Metadata, identityID string) (store.PrincipalID, error) {
	identity, err := meta.GetJMAPIdentity(ctx, identityID)
	if err != nil {
		return 0, err
	}
	return identity.PrincipalID, nil
}

// resolveIdentity looks up the JMAP identity by id, returning the whole row
// (owner and addr-spec). Callers that need the identity's own address for
// the submission probe (REQ-AUTH-EXT-SUBMIT-11) use this instead of
// resolveIdentityOwner.
func resolveIdentity(ctx context.Context, meta store.Metadata, identityID string) (store.JMAPIdentity, error) {
	return meta.GetJMAPIdentity(ctx, identityID)
}

// isSyntheticDefault reports whether identityID refers to the per-
// principal synthesised default identity (no row in jmap_identities;
// id is the literal "default" by JMAP-wire convention).
func isSyntheticDefault(identityID string) bool {
	return identityID == "default"
}

// handleGetSubmission implements GET /api/v1/identities/{id}/submission.
// Returns the submission config for the identity without any credential
// material (REQ-AUTH-EXT-SUBMIT-04). Gated by requireSelfOnly: the caller
// must own the identity.
//
// Always includes available_oauth_providers (server-level, identity-independent)
// and domain_authoritative (identity-specific) in the response (re #73, #74).
func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, callerOK := principalFrom(r.Context())

	// Provider list is identity-independent: compute once and include in all
	// response paths so the Suite can gate OAuth buttons without a separate
	// request (re #73).
	providers := s.sortedConfiguredOAuthProviders()

	// Synthesised default identity ("default" id, no row in
	// jmap_identities). It always reports as not configured: the
	// FK in identity_submission references jmap_identities so the
	// default cannot persist a submission row. Returning 200
	// {configured:false} (instead of a 404) lets the SPA render the
	// per-row badge without polling forever. Domain authoritativeness
	// is determined from the caller's own canonical email: the synthetic
	// default identity always uses the principal's own address, so the
	// domain check must use caller.CanonicalEmail (re #107).
	if isSyntheticDefault(identityID) {
		if !callerOK {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "session required", "")
			return
		}
		authoritative, err := s.isDomainAuthoritative(r.Context(), emailDomainOf(caller.CanonicalEmail))
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, submissionGetResponse{
			Configured:              false,
			AvailableOAuthProviders: providers,
			DomainAuthoritative:     authoritative,
		})
		return
	}

	// Resolve the full identity (not just the owner id) so we have
	// identity.Email for the domain_authoritative check (re #74).
	identity, err := resolveIdentity(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, identity.PrincipalID) {
		return
	}

	// Determine whether the identity's domain is locally authoritative.
	authoritative, err := s.isDomainAuthoritative(r.Context(), emailDomainOf(identity.Email))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	sub, err := s.store.Meta().GetIdentitySubmission(r.Context(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, submissionGetResponse{
				Configured:              false,
				AvailableOAuthProviders: providers,
				DomainAuthoritative:     authoritative,
			})
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, submissionGetResponse{
		Configured:              true,
		SubmitHost:              sub.SubmitHost,
		SubmitPort:              sub.SubmitPort,
		SubmitSecurity:          sub.SubmitSecurity,
		SubmitAuthMethod:        sub.SubmitAuthMethod,
		State:                   string(sub.State),
		AvailableOAuthProviders: providers,
		DomainAuthoritative:     authoritative,
	})
}

// sortedConfiguredOAuthProviders returns the sorted list of OAuth provider ids
// whose ClientSecret is non-empty. The list reflects server-level configuration
// and is identity-independent: the same value is returned for every caller.
// The returned slice is never nil (an empty or unconfigured provider map yields []).
func (s *Server) sortedConfiguredOAuthProviders() []string {
	ids := make([]string, 0, len(s.opts.OAuthProviders))
	for id, p := range s.opts.OAuthProviders {
		if p.ClientSecret != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// emailDomainOf extracts the domain part of an RFC 5321 addr-spec. Returns an
// empty string when the address does not contain an "@" separator.
func emailDomainOf(email string) string {
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	return domain
}

// isDomainAuthoritative reports whether domain is one the server is authoritative
// for (an IsLocal==true row in the domains table). The comparison is
// case-insensitive per RFC 5321. An empty domain always returns false.
func (s *Server) isDomainAuthoritative(ctx context.Context, domain string) (bool, error) {
	if domain == "" {
		return false, nil
	}
	locals, err := s.store.Meta().ListLocalDomains(ctx)
	if err != nil {
		return false, err
	}
	lc := strings.ToLower(domain)
	for _, d := range locals {
		if strings.ToLower(d.Name) == lc {
			return true, nil
		}
	}
	return false, nil
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
// Gated by requireSelfOnly (REQ-AUTH-EXT-SUBMIT-04). No TOTP step-up is
// required: saving external-SMTP credentials is a routine end-user
// self-service operation, not a privileged action (issue #119).
func (s *Server) handlePutSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	// External submission requires a persisted Identity (the table FKs
	// jmap_identities). The synthesised default identity is not
	// persisted, so reject the PUT with a clear 422 and surface the
	// constraint to the caller.
	if isSyntheticDefault(identityID) {
		writeProblem(w, r, http.StatusUnprocessableEntity, "synthetic_default_unsupported",
			"the default identity has no persistent row; create a custom Identity to configure external SMTP submission",
			identityID)
		return
	}

	identity, err := resolveIdentity(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, identity.PrincipalID) {
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
	probeOutcome := s.opts.ExternalProbe(r.Context(), sub, identity.Email)
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
	caller, callerOK := principalFrom(r.Context())

	// Synthetic default never has a submission row to delete; treat
	// DELETE as idempotent no-op rather than 404.
	if isSyntheticDefault(identityID) {
		if !callerOK {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "session required", "")
			return
		}
		_ = caller
		w.WriteHeader(http.StatusNoContent)
		return
	}

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
func noopProbe(_ context.Context, _ store.IdentitySubmission, _ string) extsubmit.Outcome {
	return extsubmit.Outcome{State: extsubmit.OutcomeOK}
}

// DefaultProbeFromSubmitter wraps a *extsubmit.Submitter as an ExternalProbe.
// admin/server.go uses this to pass the real probe into protoadmin.Options
// (REQ-MAIL-SUBMIT-03 — the probe validates credentials before persisting).
func DefaultProbeFromSubmitter(sub *extsubmit.Submitter) ExternalProbe {
	return func(ctx context.Context, row store.IdentitySubmission, fromAddr string) extsubmit.Outcome {
		return sub.Probe(ctx, row, fromAddr)
	}
}

// ExternalTestSender relays one test message through the external smart host
// using the persisted submission credentials. It wraps extsubmit.Submitter.Submit
// and backs the on-demand connection test (re #113).
type ExternalTestSender func(ctx context.Context, sub store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome

// DefaultTestSenderFromSubmitter wraps a *extsubmit.Submitter as an
// ExternalTestSender. admin/server.go passes this into protoadmin.Options so
// the connection-test endpoint reaches the real SMTP path.
func DefaultTestSenderFromSubmitter(sub *extsubmit.Submitter) ExternalTestSender {
	return func(ctx context.Context, row store.IdentitySubmission, env extsubmit.Envelope) extsubmit.Outcome {
		return sub.Submit(ctx, row, env)
	}
}

// submissionTestResponse is the wire form returned by
// POST /api/v1/identities/{id}/submission/test (re #113). It carries the
// connection-test result the identity editor renders: ok reports whether the
// smart host accepted the authenticated test message, and detail carries the
// human-readable diagnostic (the SMTP reply on failure, the accept notice on
// success). No credential material is ever included.
type submissionTestResponse struct {
	// OK is true when the test message was accepted by the smart host.
	OK bool `json:"ok"`
	// Detail is the human-readable diagnostic (SMTP reply / accept notice).
	Detail string `json:"detail"`
}

// handleTestSubmission implements POST /api/v1/identities/{id}/submission/test.
//
// It loads the persisted submission config for the identity and relays a small
// test message through the external smart host to the identity's own address,
// exercising the full authenticated SMTP path (AUTH + MAIL + RCPT + DATA). The
// response is a 200 {ok, detail} envelope in every reachable case: ok reports
// whether the smart host accepted the message, and detail carries the SMTP
// diagnostic. A misconfigured or unreachable endpoint yields ok=false with the
// diagnostic in detail — never a bare status (re #113).
//
// Gated by requireSelfOnly: the caller must own the identity. Returns 404 when
// the identity does not exist, 422 when no submission row is configured, and
// 503 when external submission is not configured on this server.
func (s *Server) handleTestSubmission(w http.ResponseWriter, r *http.Request) {
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "identity id is required", "")
		return
	}
	caller, _ := principalFrom(r.Context())

	if isSyntheticDefault(identityID) {
		writeProblem(w, r, http.StatusUnprocessableEntity, "synthetic_default_unsupported",
			"the default identity has no persistent submission row; create a custom Identity to configure external SMTP submission", identityID)
		return
	}

	identity, err := resolveIdentity(r.Context(), s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found", "identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, identity.PrincipalID) {
		return
	}

	if len(s.opts.ExternalSubmissionDataKey) == 0 || s.opts.ExternalTestSender == nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "not_configured",
			"external submission is not configured on this server", "")
		return
	}

	sub, err := s.store.Meta().GetIdentitySubmission(r.Context(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusUnprocessableEntity, "submission_not_configured",
				"no external submission is configured for this identity", identityID)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	// Parse the optional request body to allow the caller to specify a
	// recipient address (re #122). When absent or empty the endpoint
	// defaults to the identity's own address so callers that send no body
	// (e.g. the existing e2e testConnection helper) are unaffected.
	var testReq submissionTestRequest
	if !decodeOptionalJSONBody(w, r, &testReq) {
		return
	}

	// Build a small test message. The identity's own address is always the
	// MAIL FROM. The RCPT TO is the caller-supplied address when non-empty,
	// and falls back to the identity's own address otherwise so the probe
	// never injects mail to an unintended third party.
	from := identity.Email
	rcptTo := testReq.To
	if rcptTo == "" {
		rcptTo = from
	}
	body := "From: " + from + "\r\n" +
		"To: " + rcptTo + "\r\n" +
		"Subject: herold external submission test\r\n" +
		"Message-ID: <conn-test-" + newRequestID() + "@herold>\r\n" +
		"\r\n" +
		"This message confirms that herold can send mail through the configured\r\n" +
		"external submission server for this identity.\r\n"
	env := extsubmit.Envelope{
		MailFrom:      from,
		RcptTo:        []string{rcptTo},
		Body:          strings.NewReader(body),
		CorrelationID: "conn-test:" + identityID,
	}

	outcome := s.opts.ExternalTestSender(r.Context(), sub, env)
	ok := outcome.State == extsubmit.OutcomeOK

	s.appendAudit(r.Context(), "identity.submission.test",
		fmt.Sprintf("identity:%s", identityID),
		outcomeToAuditResult(ok), outcome.Diagnostic,
		map[string]string{
			"principal_id": fmt.Sprintf("%d", caller.ID),
			"identity_id":  identityID,
			"category":     string(outcome.State),
		})

	writeJSON(w, http.StatusOK, submissionTestResponse{OK: ok, Detail: outcome.Diagnostic})
}

// outcomeToAuditResult maps a connection-test success flag to the audit
// outcome vocabulary.
func outcomeToAuditResult(ok bool) store.AuditOutcome {
	if ok {
		return store.OutcomeSuccess
	}
	return store.OutcomeFailure
}
