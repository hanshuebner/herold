package protoadmin

// identity_verify.go implements the REST verification endpoints for
// REQ-IDENT-40..43.
//
//   - GET /verify-identity?token=<token>
//     Server-driven link callback. Mounted on the public listener
//     OUTSIDE /api/v1/* so the URL embedded in the verification email
//     stays short. No auth gate: the token IS the auth (single-use,
//     CSPRNG-generated, sha256'd in the store).
//   - POST /api/v1/identities/{id}/verify
//     Self-only code-entry endpoint. CSRF-checked when cookie-
//     authenticated; the suite's "have a code" input POSTs the 6-digit
//     code lifted from the verification email body.
//
// Both endpoints converge on store.Meta().MarkIdentityVerified once a
// matching token/code is found, the row is unexpired, and the request
// is authorised (link path) or self-only (code path). Every callback
// emits an audit-log entry tagged identity.verify.{success,failure}
// (REQ-IDENT-43); tokens themselves are never logged (the row stores
// only the sha256 of the raw value).

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// verifyResultClass tags an identity.verify audit entry's metadata so
// log tailers can distinguish the failure modes without parsing the
// human-readable message. The set is closed; new values require a
// requirements update.
type verifyResultClass string

const (
	verifyResultOK             verifyResultClass = "ok"
	verifyResultInvalidToken   verifyResultClass = "invalid_token"
	verifyResultInvalidCode    verifyResultClass = "invalid_code"
	verifyResultExpired        verifyResultClass = "expired"
	verifyResultMalformedToken verifyResultClass = "malformed_token"
	verifyResultMalformedCode  verifyResultClass = "malformed_code"
	verifyResultForbidden      verifyResultClass = "forbidden"
)

// verifyFailureTemplate is the minimal HTML5 failure page rendered by
// the link-callback handler. Static text in English only — the SPA
// owns the i18n surface; this page exists for the cases where the
// SPA is never reached (token not found, expired, or malformed).
// The page links to /#/settings so the user can request a new
// verification email from the SPA's Identity panel.
var verifyFailureTemplate = template.Must(template.New("verify-fail").Parse(
	`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Verification failed</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; color: #222; }
  h1 { font-size: 1.4rem; margin: 0 0 1rem; }
  p { line-height: 1.5; margin: 0 0 1rem; }
  a.button { display: inline-block; padding: 0.6rem 1.2rem; background: #2a6fdb; color: #fff; text-decoration: none; border-radius: 4px; }
  a.button:hover { background: #1f57b3; }
</style>
</head>
<body>
<h1>Verification failed</h1>
<p>{{.Message}}</p>
<p><a class="button" href="/#/settings">Go to Settings</a></p>
</body>
</html>
`))

// renderVerifyFailure writes a 400 HTML page describing why the link
// could not be honoured. The message is plain English; the SPA owns
// localisation and is reachable via the "Go to Settings" button.
func renderVerifyFailure(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = verifyFailureTemplate.Execute(w, struct{ Message string }{Message: message})
}

// hashTokenOrCode returns sha256(input). Matches the store's invariant
// that all verification hashes are 32 bytes.
func hashTokenOrCode(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// auditVerify writes an identity.verify.{success,failure} entry. The
// caller path (link vs code) is recorded in metadata so the operator
// can correlate the failure mode with the surface that triggered it.
// The token itself is never logged; only the result class, the
// requesting principal (when known), the identity id, the user-agent
// string, and the source IP. REQ-IDENT-43.
func (s *Server) auditVerify(
	ctx context.Context,
	r *http.Request,
	identityID string,
	principalID store.PrincipalID,
	result verifyResultClass,
	via string, // "link" | "code"
	message string,
) {
	action := "identity.verify.success"
	outcome := store.OutcomeSuccess
	if result != verifyResultOK {
		action = "identity.verify.failure"
		outcome = store.OutcomeFailure
	}
	meta := map[string]string{
		"identity_id":  identityID,
		"result_class": string(result),
		"via":          via,
		"user_agent":   r.UserAgent(),
		"source_ip":    r.RemoteAddr,
	}
	if principalID != 0 {
		meta["principal_id"] = fmt.Sprintf("%d", principalID)
	}
	s.appendAudit(ctx, action,
		fmt.Sprintf("identity:%s", identityID),
		outcome, message, meta)
}

// handleVerifyIdentityLink implements GET /verify-identity?token=<token>
// on the public listener (REQ-IDENT-40). NO auth gate: the token is
// the auth. Outcomes:
//
//   - missing / malformed token -> 400 HTML failure
//   - token does not match any row -> 400 HTML failure
//   - row's token expired -> 400 HTML failure
//   - row already verified AND token columns cleared (the consumed
//     token bypassed the reverse lookup, but the user re-clicks an
//     old link that no longer matches anything) -> 400 HTML failure
//     (caught upstream as ErrNotFound)
//   - row matched and unexpired -> MarkIdentityVerified, 302 to /#/settings
//
// Idempotency: a consumed token produces ErrNotFound on lookup (the
// hash is cleared by MarkIdentityVerified). The user's mail client may
// retry the link after a stale response; in that retry case the SPA
// already shows the verified state via Identity/changes push, so
// returning the failure page is acceptable per REQ-IDENT-42 ("the most
// recently delivered link is the only valid one"). When the row is
// CURRENTLY verified and the token still matches (a redeem-and-refresh
// race), we treat that as a success no-op and redirect normally.
func (s *Server) handleVerifyIdentityLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		s.auditVerify(ctx, r, "", 0, verifyResultMalformedToken, "link",
			"empty or missing token query parameter")
		renderVerifyFailure(w,
			"This verification link is missing its token. Please request a new one from Settings.")
		return
	}

	tokenHash := hashTokenOrCode([]byte(token))
	identity, err := s.store.Meta().GetIdentityByVerificationTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.auditVerify(ctx, r, "", 0, verifyResultInvalidToken, "link",
				"no identity row matches the supplied token hash")
			renderVerifyFailure(w,
				"This verification link is invalid or has already been used. If your email address is not yet verified, you can request a new link from Settings.")
			return
		}
		// Storage failure: log and surface a generic failure page;
		// the user may retry from Settings.
		s.loggerFrom(ctx).Error("protoadmin.verify_identity.link.store_failed",
			"activity", observe.ActivityInternal, "err", err)
		renderVerifyFailure(w,
			"We could not verify your identity right now. Please try again from Settings.")
		return
	}

	now := s.clk.Now().UnixMicro()
	if identity.VerificationTokenExpiresAtUs > 0 && identity.VerificationTokenExpiresAtUs < now {
		s.auditVerify(ctx, r, identity.ID, identity.PrincipalID, verifyResultExpired, "link",
			"verification token expired")
		renderVerifyFailure(w,
			"This verification link has expired. Please request a new one from Settings.")
		return
	}

	// REQ-IDENT-42: idempotent success. When the row is already
	// verified AND we still matched the token (some store
	// implementations may retain the hash temporarily), redirect
	// without rewriting state. The reverse-lookup contract is that
	// MarkIdentityVerified clears the trio, so this branch normally
	// fires only on a race between two concurrent redeems.
	if identity.VerifiedAtUs != 0 {
		s.auditVerify(ctx, r, identity.ID, identity.PrincipalID, verifyResultOK, "link",
			"already verified (idempotent retry)")
		http.Redirect(w, r, "/#/settings", http.StatusFound)
		return
	}

	if err := s.store.Meta().MarkIdentityVerified(ctx, identity.ID); err != nil {
		s.loggerFrom(ctx).Error("protoadmin.verify_identity.link.mark_failed",
			"activity", observe.ActivityInternal, "err", err,
			"identity_id", identity.ID)
		renderVerifyFailure(w,
			"We could not verify your identity right now. Please try again from Settings.")
		return
	}

	s.auditVerify(ctx, r, identity.ID, identity.PrincipalID, verifyResultOK, "link", "")
	http.Redirect(w, r, "/#/settings", http.StatusFound)
}

// verifyCodeRequest is the JSON body for POST /api/v1/identities/{id}/verify.
type verifyCodeRequest struct {
	Code string `json:"code"`
}

// isValidVerificationCode reports whether s is exactly 6 ASCII digits.
// Leading zeros are preserved (REQ-IDENT-32).
func isValidVerificationCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// handleVerifyIdentityCode implements POST /api/v1/identities/{id}/verify
// on the public listener (REQ-IDENT-41). CSRF-checked by requireAuth's
// cookie path; Bearer callers bypass CSRF as usual. Self-only: the
// caller must own the identity (no admin impersonation in v1, mirroring
// the submission endpoints).
func (s *Server) handleVerifyIdentityCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"identity id is required", "")
		return
	}
	caller, _ := principalFrom(ctx)

	// Synthesised default identity has no persisted row and therefore
	// no verification trio; treat as 404 so the SPA does not loop.
	if isSyntheticDefault(identityID) {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"identity not found", identityID)
		return
	}

	ownerID, err := resolveIdentityOwner(ctx, s.store.Meta(), identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"identity not found", identityID)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	if !requireSelfOnly(w, r, caller, ownerID) {
		// requireSelfOnly wrote the 403; emit audit so cross-principal
		// probes leave a trail (REQ-IDENT-43).
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultForbidden, "code",
			"caller does not own identity")
		return
	}

	var body verifyCodeRequest
	if !decodeJSONBody(w, r, &body) {
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultMalformedCode, "code",
			"request body could not be parsed")
		return
	}
	if !isValidVerificationCode(body.Code) {
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultMalformedCode, "code",
			"code must be exactly 6 ASCII digits")
		writeProblem(w, r, http.StatusBadRequest, "invalid_code",
			"code must be exactly 6 ASCII digits", "")
		return
	}

	codeHash := hashTokenOrCode([]byte(body.Code))
	identity, err := s.store.Meta().GetIdentityByVerificationCodeHash(ctx, identityID, codeHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.auditVerify(ctx, r, identityID, caller.ID, verifyResultInvalidCode, "code",
				"code did not match the active verification token")
			writeProblem(w, r, http.StatusBadRequest, "invalid_code",
				"code did not match", "")
			return
		}
		s.writeStoreError(w, r, err)
		return
	}

	now := s.clk.Now().UnixMicro()
	if identity.VerificationTokenExpiresAtUs > 0 && identity.VerificationTokenExpiresAtUs < now {
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultExpired, "code",
			"verification code expired")
		writeProblem(w, r, http.StatusBadRequest, "expired_code",
			"this verification code has expired; request a new one from Settings", "")
		return
	}

	// REQ-IDENT-42: idempotent success when the row is already
	// verified. The code-scoped lookup is identity-scoped, so a
	// matching codeHash on an already-verified row means the same
	// caller is redeeming twice. Return 204 without rewriting state.
	if identity.VerifiedAtUs != 0 {
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultOK, "code",
			"already verified (idempotent retry)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := s.store.Meta().MarkIdentityVerified(ctx, identity.ID); err != nil {
		s.loggerFrom(ctx).Error("protoadmin.verify_identity.code.mark_failed",
			"activity", observe.ActivityInternal, "err", err,
			"identity_id", identity.ID)
		s.writeStoreError(w, r, err)
		return
	}

	s.auditVerify(ctx, r, identity.ID, caller.ID, verifyResultOK, "code", "")
	w.WriteHeader(http.StatusNoContent)
}
