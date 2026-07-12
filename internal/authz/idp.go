package authz

import (
	"context"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// idpStore is the slice of store.Metadata ReconcileIdP depends on. Taking
// an interface keeps ReconcileIdP testable without a full store.
type idpStore interface {
	grantReader
	GetPrincipalByID(ctx context.Context, id store.PrincipalID) (store.Principal, error)
	GetOIDCProvider(ctx context.Context, name string) (store.OIDCProvider, error)
	ListClaimAllowlist(ctx context.Context, providerName string) ([]string, error)
	ListClaimMappingRules(ctx context.Context, providerName string) ([]store.ClaimMappingRule, error)
	ReconcileIdPGrants(ctx context.Context, subjectID store.PrincipalID, provider string, desired []store.GrantDesired, asOf time.Time) (added, removed []store.Grant, err error)
	AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error
}

// ReconcileIdP evaluates providerName's claim-mapping rules against claims
// (the validated ID token's claim set, keyed by claim name) and reconciles
// principal's "idp:<providerName>" grants to match (epic #188, REQ-AC-62).
// It is the function internal/directoryoidc calls after every successful
// OIDC login.
//
// Safety rails, evaluated per rule before it can contribute a grant:
//
//   - The provider must be AuthzTrusted (REQ-AC-66) -- checked once, up
//     front; an untrusted provider makes this call a complete no-op.
//   - The rule's Claim must be on the provider's authorization-claim
//     allowlist (REQ-AC-67); a claim absent from the allowlist can never
//     satisfy a rule regardless of its value.
//   - The rule must not target GrantResourceServer (REQ-AC-64):
//     server:superadmin is never IdP-derivable, full stop.
//   - The rule's author must currently hold delegable authority (the top
//     tier of the target resource's kind, or server:superadmin) over the
//     target resource, re-checked at evaluation time, not trusted from
//     rule-creation time (REQ-AC-68); an author who has lost that
//     authority, or been deleted (CreatedBy zero), makes the rule inert.
//   - When isNewPrincipal is true (this login also auto-provisioned a
//     brand-new principal, REQ-AUTH-56), only the mailbox:read tier may be
//     applied; every operator/owner/moderator/admin/write grant is
//     dropped for this evaluation (REQ-AC-69).
//
// Surviving rules are reduced to at most one (resource, level) per
// resource -- the highest level among matching rules, per REQ-AC-03's
// total order -- and handed to store.Metadata.ReconcileIdPGrants, which
// diffs them against the principal's current idp:<providerName> grants in
// one transaction: new resources are inserted, resources no longer
// entitled are deleted, and survivors have LastAssertedAt refreshed.
// local grants and idp: grants of any other provider are never read or
// written (REQ-AC-61).
//
// Fail-safe (REQ-AC-70): if the provider, allowlist, or rules cannot be
// loaded, ReconcileIdP returns the error without calling
// ReconcileIdPGrants -- the principal's existing idp: grants are left
// exactly as they were, never mass-revoked on a transient store outage.
// A rule whose author lookup fails, or whose author authority cannot be
// resolved, is simply skipped (treated as not matching) rather than
// aborting the whole reconciliation -- one bad rule does not block every
// other rule's grants.
//
// Every added and removed grant is audited with actor "idp:<providerName>"
// (REQ-AC-65); a load failure is audited too. Audit writes are
// best-effort: a logging failure does not change the returned result.
func ReconcileIdP(ctx context.Context, meta idpStore, clk clock.Clock, principal store.Principal, providerName string, claims map[string]any, isNewPrincipal bool) (added, removed []store.Grant, err error) {
	now := clk.Now().UTC()

	provider, err := meta.GetOIDCProvider(ctx, providerName)
	if err != nil {
		auditIdPFailure(ctx, meta, clk, providerName, principal.ID, "load provider", err)
		return nil, nil, fmt.Errorf("authz: reconcile idp: load provider %q: %w", providerName, err)
	}
	if !provider.AuthzTrusted {
		// REQ-AC-66: authorization trust is separate from authentication
		// trust. A provider not flagged authz_trusted never confers or
		// revokes a grant here -- inert, not an error.
		return nil, nil, nil
	}

	allowlist, err := meta.ListClaimAllowlist(ctx, providerName)
	if err != nil {
		auditIdPFailure(ctx, meta, clk, providerName, principal.ID, "load claim allowlist", err)
		return nil, nil, fmt.Errorf("authz: reconcile idp: load allowlist for %q: %w", providerName, err)
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, c := range allowlist {
		allowed[c] = true
	}

	rules, err := meta.ListClaimMappingRules(ctx, providerName)
	if err != nil {
		auditIdPFailure(ctx, meta, clk, providerName, principal.ID, "load mapping rules", err)
		return nil, nil, fmt.Errorf("authz: reconcile idp: load rules for %q: %w", providerName, err)
	}

	type resKey struct {
		kind store.GrantResourceKind
		id   string
	}
	desiredByKey := make(map[resKey]store.GrantLevel)

	for _, rule := range rules {
		if rule.ResourceKind == store.GrantResourceServer {
			continue // REQ-AC-64: never IdP-derivable.
		}
		if !allowed[rule.Claim] {
			continue // REQ-AC-67: claim not on the allowlist.
		}
		val, ok := claims[rule.Claim]
		if !ok || !claimMatches(val, rule.MatchValue) {
			continue
		}
		if rule.CreatedBy == 0 {
			continue // Authorless rule (author deleted): always inert (REQ-AC-68).
		}
		author, aerr := meta.GetPrincipalByID(ctx, rule.CreatedBy)
		if aerr != nil {
			continue // Author gone or unresolvable: treat this rule as inert.
		}
		authorLevel, rerr := Resolve(ctx, meta, author, Resource{Kind: rule.ResourceKind, ID: rule.ResourceID})
		if rerr != nil {
			continue // Fail-safe: an unresolved author authority skips the rule, not the whole reconciliation.
		}
		if !AtLeast(rule.ResourceKind, authorLevel, topLevel(rule.ResourceKind)) {
			continue // REQ-AC-68: author no longer holds delegable authority over the target.
		}
		if isNewPrincipal && !isEndUserLevel(rule.ResourceKind, rule.Level) {
			continue // REQ-AC-69: at most end-user access on an auto-provisioning login.
		}
		k := resKey{rule.ResourceKind, rule.ResourceID}
		if cur, exists := desiredByKey[k]; !exists || rank(rule.ResourceKind, rule.Level) > rank(rule.ResourceKind, cur) {
			desiredByKey[k] = rule.Level
		}
	}

	desired := make([]store.GrantDesired, 0, len(desiredByKey))
	for k, level := range desiredByKey {
		desired = append(desired, store.GrantDesired{ResourceKind: k.kind, ResourceID: k.id, Level: level})
	}

	added, removed, err = meta.ReconcileIdPGrants(ctx, principal.ID, providerName, desired, now)
	if err != nil {
		auditIdPFailure(ctx, meta, clk, providerName, principal.ID, "reconcile grants", err)
		return nil, nil, fmt.Errorf("authz: reconcile idp: %w", err)
	}
	for _, g := range added {
		auditIdPGrantChange(ctx, meta, clk, providerName, principal.ID, "idp.grant.add", g)
	}
	for _, g := range removed {
		auditIdPGrantChange(ctx, meta, clk, providerName, principal.ID, "idp.grant.remove", g)
	}
	return added, removed, nil
}

// isEndUserLevel reports whether level on kind is at or below the
// mailbox-reading tier -- the only tier REQ-AC-69 permits to be applied on
// the same login that auto-provisions a new principal. Every domain/list
// level is administrative by construction (operator/owner/moderator all
// grant more than an ordinary end user has on their own resources);
// mailbox:write and mailbox:admin are excluded, leaving only
// mailbox:read.
func isEndUserLevel(kind store.GrantResourceKind, level store.GrantLevel) bool {
	return kind == store.GrantResourceMailbox && level == store.GrantLevelRead
}

// claimMatches reports whether value -- claims[rule.Claim], as decoded
// from JSON (or supplied directly by a test) -- satisfies match: membership
// for an array-valued claim (the common "groups"/"roles" shape), equality
// for a scalar string claim. Any other shape (number, bool, object, nil)
// never matches -- REQ-AC-70's "a claim that cannot be evaluated" is
// treated as a non-match rather than an error, so one oddly-shaped claim
// cannot abort reconciliation of every other rule.
func claimMatches(value any, match string) bool {
	switch v := value.(type) {
	case string:
		return v == match
	case []string:
		for _, s := range v {
			if s == match {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == match {
				return true
			}
		}
	}
	return false
}

// auditIdPFailure records a fail-safe reconciliation abort (REQ-AC-70): the
// principal's idp: grants are left unchanged and the failure is audited
// with actor "idp:<provider>" so an operator can see why no grants
// changed. Best-effort: the audit write's own error is discarded.
func auditIdPFailure(ctx context.Context, meta idpStore, clk clock.Clock, provider string, principalID store.PrincipalID, step string, cause error) {
	_ = meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        clk.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "idp:" + provider,
		Action:    "idp.reconcile.failed",
		Subject:   fmt.Sprintf("principal:%d", principalID),
		Outcome:   store.OutcomeFailure,
		Message:   fmt.Sprintf("%s: %s", step, cause.Error()),
	})
}

// auditIdPGrantChange records one grant added or removed by reconciliation
// (REQ-AC-65), actor "idp:<provider>". Best-effort: the audit write's own
// error is discarded.
func auditIdPGrantChange(ctx context.Context, meta idpStore, clk clock.Clock, provider string, principalID store.PrincipalID, action string, g store.Grant) {
	_ = meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        clk.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "idp:" + provider,
		Action:    action,
		Subject:   fmt.Sprintf("principal:%d", principalID),
		Outcome:   store.OutcomeSuccess,
		Metadata: map[string]string{
			"resource_kind": string(g.ResourceKind),
			"resource_id":   g.ResourceID,
			"level":         string(g.Level),
		},
	})
}

// staleSweepStore is the slice of store.Metadata SweepStale depends on.
type staleSweepStore interface {
	SweepStaleIdPGrants(ctx context.Context, olderThan time.Time) ([]store.Grant, error)
	AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error
}

// SweepStale deletes every idp:<provider> grant across all principals whose
// LastAssertedAt is older than staleness (REQ-AC-63b, deprovisioning
// trigger (b): "for members who stop logging in, a periodic sweep removes
// idp: grants ... so a revoked-at-the-IdP user does not retain access
// indefinitely"). It is the function a periodic maintenance worker calls
// on its own schedule (the worker itself -- registration, interval,
// scheduling -- is deferred; see the epic #188 analysis for why). Returns
// the removed grants; each is audited individually with actor
// "idp:<provider>" (the provider parsed from the grant's own Provenance
// column), action "idp.grant.sweep".
func SweepStale(ctx context.Context, meta staleSweepStore, clk clock.Clock, staleness time.Duration) ([]store.Grant, error) {
	cutoff := clk.Now().UTC().Add(-staleness)
	removed, err := meta.SweepStaleIdPGrants(ctx, cutoff)
	if err != nil {
		return nil, fmt.Errorf("authz: sweep stale idp grants: %w", err)
	}
	for _, g := range removed {
		_ = meta.AppendAuditLog(ctx, store.AuditLogEntry{
			At:        clk.Now(),
			ActorKind: store.ActorSystem,
			ActorID:   g.Provenance,
			Action:    "idp.grant.sweep",
			Subject:   fmt.Sprintf("principal:%d", store.PrincipalID(g.SubjectID)),
			Outcome:   store.OutcomeSuccess,
			Metadata: map[string]string{
				"resource_kind": string(g.ResourceKind),
				"resource_id":   g.ResourceID,
				"level":         string(g.Level),
			},
		})
	}
	return removed, nil
}
