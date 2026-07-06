package protoadmin

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// OperatorScope describes what a caller is allowed to see.
//
// Super-admins (PrincipalFlagAdmin + PrincipalFlagSuperAdmin) receive an
// unrestricted scope: SuperAdmin == true, Domains is nil. Callers must check
// SuperAdmin first and skip the Domains filter entirely when it is true.
//
// Domain-scoped operators (PrincipalFlagAdmin only) receive a scope whose
// Domains set is the principal's managed-domain list. An operator whose
// managed-domain set is empty (never assigned any domains, or the lookup
// failed) gets an empty Domains slice — the surface MUST return nothing rather
// than leak cross-domain data (REQ-ADM-307).
//
// A principal that is not an admin at all yields the empty non-super-admin
// scope; callers should gate access with requireAdmin before invoking
// ResolveOperatorScope.
type OperatorScope struct {
	// SuperAdmin is true when the caller is a global super-admin. When
	// true, Domains is nil and callers must not filter by domain.
	SuperAdmin bool
	// Domains is the caller's managed-domain set. Meaningful only when
	// SuperAdmin is false. An empty (nil or zero-length) slice means the
	// caller has no managed domains and the observability surface MUST
	// return an empty result set rather than leak data (fail-closed per
	// REQ-ADM-307).
	Domains []string
}

// ResolveOperatorScope resolves the effective authorization scope for caller
// against the metadata store.
//
// Returns:
//   - {SuperAdmin: true}  when caller holds both PrincipalFlagAdmin and
//     PrincipalFlagSuperAdmin.
//   - {Domains: [...]string}  when caller holds PrincipalFlagAdmin only; the
//     slice is the result of store.ListManagedDomains for caller.ID.
//   - {Domains: nil}  when caller is not an admin, or when the
//     ListManagedDomains call fails (fail-closed: empty domain set).
//
// The store call is a single indexed SELECT; it is safe to call on every
// request in the admin hot path.
func ResolveOperatorScope(ctx context.Context, meta store.Metadata, caller store.Principal) OperatorScope {
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		// Not an admin at all — zero scope.
		return OperatorScope{}
	}
	if caller.Flags.Has(store.PrincipalFlagSuperAdmin) {
		return OperatorScope{SuperAdmin: true}
	}
	// Domain-scoped operator: resolve managed domains. Fail closed on error.
	domains, err := meta.ListManagedDomains(ctx, caller.ID)
	if err != nil {
		// Log-worthy but not request-fatal: return empty domain set so the
		// surface shows nothing rather than leaking data or surfacing an
		// internal error to the client.
		return OperatorScope{}
	}
	return OperatorScope{Domains: domains}
}
