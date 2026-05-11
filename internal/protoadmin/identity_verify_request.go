package protoadmin

// identity_verify_request.go implements POST
// /api/v1/identities/{id}/verify-request (REQ-IDENT-36, REQ-IDENT-41
// resend surface).
//
// The endpoint is self-only: the caller must own the Identity. CSRF is
// enforced by the cookie-auth path (Bearer skips, like everywhere
// else). The handler hands off to a VerificationResender (the
// *identityverify.Dispatcher in production wiring) which rotates the
// trio and applies the rate-limit gate atomically. On gate violation
// the store returns store.ErrRateLimited; the handler surfaces that as
// HTTP 429 with a Retry-After header and a problem-detail body.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// VerificationResender is the seam between the REST handler and the
// identityverify.Dispatcher. The handler invokes Resend with the
// Identity row; the dispatcher rotates the token+code, enqueues the
// verification email, and applies the rate-limit gate atomically. The
// returned error is either nil, store.ErrRateLimited, or some other
// internal failure (logged + 500). The interface deliberately omits
// the dispatcher's Tokens return value so protoadmin does not gain a
// circular dependency on identityverify (which already imports
// queue / clock / store).
type VerificationResender interface {
	Resend(ctx context.Context, row store.JMAPIdentity) error
}

// handleVerifyIdentityRequest implements POST
// /api/v1/identities/{id}/verify-request. Behaviour:
//   - 503 when no VerificationResender is wired (sysconfig disabled).
//   - 404 when the Identity row is missing.
//   - 403 when the caller does not own the Identity (audited).
//   - 409 conflict when the Identity is already verified -- a resend
//     of an already-verified identity is a no-op the SPA should not
//     trigger (spec: the SPA hides the Resend affordance for verified
//     rows, REQ-IDENT-41).
//   - 429 ErrRateLimited from the dispatcher: Retry-After + problem
//     detail naming the offending arm (cooldown vs daily cap).
//   - 204 success: the dispatcher rotated the token and enqueued the
//     email; the audit-log entry was emitted by the dispatcher.
func (s *Server) handleVerifyIdentityRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identityID := r.PathValue("id")
	if identityID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"identity id is required", "")
		return
	}
	caller, _ := principalFrom(ctx)

	if isSyntheticDefault(identityID) {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"identity not found", identityID)
		return
	}

	row, err := s.store.Meta().GetJMAPIdentity(ctx, identityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not_found",
				"identity not found", identityID)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	if !requireSelfOnly(w, r, caller, row.PrincipalID) {
		s.auditVerify(ctx, r, identityID, caller.ID, verifyResultForbidden, "resend",
			"caller does not own identity")
		return
	}
	if row.VerifiedAtUs != 0 {
		// REQ-IDENT-41: the SPA must hide the Resend affordance for
		// verified Identities. A defence-in-depth 409 covers the case
		// of a stale client.
		writeProblem(w, r, http.StatusConflict, "already_verified",
			"identity is already verified", identityID)
		return
	}

	if s.opts.VerificationResender == nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "resend_disabled",
			"verification resend is not configured", "")
		return
	}

	err = s.opts.VerificationResender.Resend(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrRateLimited) {
			s.writeRateLimitedResponse(w, r, identityID, row.PrincipalID)
			return
		}
		s.loggerFrom(ctx).Error("protoadmin.verify_identity.request.resend_failed",
			"activity", observe.ActivityInternal, "err", err,
			"identity_id", identityID)
		writeProblem(w, r, http.StatusInternalServerError, "resend_failed",
			"could not enqueue verification email; try again later", "")
		return
	}
	// Audit-log success is emitted by the dispatcher with action
	// identity.resend (REQ-IDENT-90). The handler returns 204 -- the
	// SPA polls Identity/changes to surface the rotation.
	w.WriteHeader(http.StatusNoContent)
}

// writeRateLimitedResponse renders a 429 with Retry-After and a
// problem-detail body for a verification resend rejected by
// store.ErrRateLimited. The Retry-After value is computed from the
// stored resend bookkeeping and the configured cooldown / daily cap:
// the larger of (a) cooldown_remaining and (b) window_remaining (when
// the daily cap is at the ceiling) is used. The handler keeps the
// math defensive so an unexpected stats shape never panics.
func (s *Server) writeRateLimitedResponse(w http.ResponseWriter, r *http.Request, identityID string, pid store.PrincipalID) {
	ctx := r.Context()
	cooldown := s.opts.VerificationResendCooldown
	dailyCap := s.opts.VerificationResendDailyCap
	stats, _ := s.store.Meta().GetVerificationResendStats(ctx, identityID)
	now := s.clk.Now()

	var retryAfter time.Duration
	classification := "cooldown"
	if cooldown > 0 && stats.LastIssuedAtUs > 0 {
		remaining := time.UnixMicro(stats.LastIssuedAtUs).Add(cooldown).Sub(now)
		if remaining > 0 {
			retryAfter = remaining
		}
	}
	// Daily-cap path: window_count would push >= dailyCap+1 on the
	// next rotate, so we wait until the window's 24h anchor lapses.
	if dailyCap > 0 && stats.WindowCount >= dailyCap && stats.WindowStartedAtUs > 0 {
		capRemaining := time.UnixMicro(stats.WindowStartedAtUs).Add(24 * time.Hour).Sub(now)
		if capRemaining > retryAfter {
			retryAfter = capRemaining
			classification = "daily_cap"
		}
	}
	if retryAfter <= 0 {
		// Defensive: the store said rate-limited but the stats say
		// otherwise (clock skew or a TOCTOU race with the GC pass).
		// Surface the cooldown value as a sane lower bound.
		retryAfter = cooldown
		if retryAfter <= 0 {
			retryAfter = time.Minute
		}
	}
	secs := int(retryAfter.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	// Audit entry for the rejection is emitted by the dispatcher
	// (action identity.resend, outcome failure, classification
	// rate_limited). We also write a verify-namespace entry so a
	// caller tailing identity.verify.failure still sees the event.
	s.auditVerify(ctx, r, identityID, pid, verifyResultClass("rate_limited"), "resend",
		fmt.Sprintf("rate_limited:%s:retry_after=%ds", classification, secs))
	writeProblemWithExtras(w, r, http.StatusTooManyRequests, "rate_limited",
		fmt.Sprintf("too many verification resend attempts; retry in %d seconds", secs),
		"", map[string]any{
			"retry_after_seconds": secs,
			"limit_kind":          classification,
			"daily_cap":           dailyCap,
			"window_count":        stats.WindowCount,
		})
}
